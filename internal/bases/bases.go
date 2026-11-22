package bases

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/zfs"
)

// baseCreationMutex serializes the part of a base build that touches the pool:
// dataset creation, the restore itself and the snapshot. Only one base is ever
// built at a time.
var baseCreationMutex sync.Mutex

// retrievalMutex serializes retrieval scripts against each other. Retrieval
// deliberately does not run under baseCreationMutex: a download touches neither
// the pool's datasets nor docker, and holding the global lock for its whole
// duration left the pool idle for minutes on every job. Downloads still go one
// at a time, so a batch of crons firing together cannot stampede the network or
// the staging area they all write into.
var retrievalMutex sync.Mutex

// prefetchDepth bounds how far retrieval may run ahead of restore. Retrieval is
// far quicker than a restore, so without a bound every resource in a batch would
// pull its dump to disk while the first one was still restoring. Two slots give
// exactly one job of lookahead: one restoring, one retrieving behind it.
const prefetchDepth = 2

var prefetchSlots = make(chan struct{}, prefetchDepth)

// runScript is a package variable so that tests can stand in for the real
// retrieval and creation scripts.
var runScript = func(script string, args ...string) (string, error) {
	cmd := exec.Command(script, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return out.String(), err
}

// retrieve runs the resource's retrieval script, serialized against other
// retrievals but not against a restore that is already running.
func retrieve(resourcePath string, r internal.Resource) (string, error) {
	retrievalMutex.Lock()
	defer retrievalMutex.Unlock()

	fmt.Println("Retrieving data")
	return runScript(filepath.Join(resourcePath, r.Retrieval))
}

// CreateBaseAndSnap restores a resource into a new base dataset and snapshots
// it, returning the name of the base it created so the caller can replicate it.
//
// r is taken by value on purpose: this runs for as long as the restore takes,
// and a resource file may be reloaded while it does. The job keeps the
// configuration it started with, so a reload can neither change the image,
// the scripts nor the ZFS properties underneath a restore that is already
// running.
func CreateBaseAndSnap(resourcePath string, r internal.Resource, docker *client.Client, z *zfs.ZFS, snapCompletedCallback func()) (string, error) {
	// Resolved first of all: an invalid bind should fail the job before it
	// downloads anything or creates a dataset, and a retrieval script may write
	// straight into a bind source directory that this creates.
	extraMounts, err := restoreMounts(r)
	if err != nil {
		return "", err
	}

	// Held for the whole job, so the number of resources that have downloaded a
	// dump but not yet restored it stays bounded.
	prefetchSlots <- struct{}{}
	defer func() { <-prefetchSlots }()

	file, err := retrieve(resourcePath, r)
	if err != nil {
		return "", err
	}

	baseCreationMutex.Lock()
	defer baseCreationMutex.Unlock()

	// Timed from here rather than from the top of the job, so that a base
	// dataset's creation timestamp marks the start of the restore and not the
	// start of a download that may have finished long before the lock came free.
	t := time.Now()
	name := z.NewDatasetBaseName(r.Name, t)

	path, err := z.CreateDataset(name, r.Name, t, r.BaseZfsProperties())
	if err != nil {
		return "", err
	}

	ctx := context.Background()
	resp, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      r.Docker.Image,
			Entrypoint: r.BaseEntrypoint(),
			Cmd:        r.BaseCmd(),
			Env:        r.BaseEnv(),
			Tty:        false,
			Healthcheck: &container.HealthConfig{
				Test:        []string{"CMD-SHELL", r.Docker.Healthcheck},
				Interval:    1 * time.Second,
				Timeout:     1 * time.Second,
				StartPeriod: 1 * time.Second,
				Retries:     1,
			},
		},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{
				Name:              "unless-stopped",
				MaximumRetryCount: 0,
			},
			ShmSize: r.Docker.Shm,
			Mounts: append([]mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: path,
					Target: r.Docker.Volume,
				},
			}, extraMounts...),
		},
		Name: name,
	})
	if err != nil {
		return "", err
	}

	// Ignore ContainerStartResult since it currently is an empty struct
	_, err = docker.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{})
	if err != nil {
		return "", err
	}

	fmt.Println("Waiting for container", name, "to become healthy")
	for {
		t, err := docker.ContainerInspect(context.Background(), resp.ID, client.ContainerInspectOptions{})
		if err != nil {
			return "", err
		}
		if t.Container.State != nil && t.Container.State.Health != nil && t.Container.State.Health.Status == container.Healthy {
			break
		}
		time.Sleep(time.Second)
	}
	fmt.Println("Container", name, "is healthy")

	fmt.Print("Creating database...")
	defer fmt.Println(" done")
	_, err = runScript(filepath.Join(resourcePath, r.Creation), file, name)
	if err != nil {
		return "", err
	}

	d := 60 // seconds
	err = stopContainer(docker, resp.ID, d)
	if err != nil {
		return "", err
	}

	err = removeContainer(docker, resp.ID)
	if err != nil {
		return "", err
	}

	err = z.SnapDataset(name, r.Name, t)
	if err != nil {
		return "", err
	}
	if snapCompletedCallback != nil {
		snapCompletedCallback()
	}
	return name, err
}

// restoreMounts parses restore_params.binds ("<host-path>:<container-path>[:ro|rw]") into bind mounts,
// creating any host directories that don't exist yet
func restoreMounts(r internal.Resource) ([]mount.Mount, error) {
	var mounts []mount.Mount
	for _, b := range r.RestoreParams.Binds {
		parts := strings.Split(b, ":")

		readOnly := false
		if len(parts) == 3 {
			switch parts[2] {
			case "ro":
				readOnly = true
			case "rw":
			default:
				return nil, fmt.Errorf("resource %s: invalid bind %q, unknown mode %q (expected ro or rw)", r.Name, b, parts[2])
			}
			parts = parts[:2]
		}
		if len(parts) != 2 || !filepath.IsAbs(parts[0]) || !filepath.IsAbs(parts[1]) {
			return nil, fmt.Errorf("resource %s: invalid bind %q, expected <abs-host-path>:<abs-container-path>[:ro]", r.Name, b)
		}
		source, target := parts[0], parts[1]

		if filepath.Clean(target) == filepath.Clean(r.Docker.Volume) {
			return nil, fmt.Errorf("resource %s: bind %q collides with docker.volume", r.Name, b)
		}
		if err := ensureDir(source); err != nil {
			return nil, fmt.Errorf("resource %s: could not create bind source %s: %w", r.Name, source, err)
		}

		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   source,
			Target:   target,
			ReadOnly: readOnly,
		})
	}
	return mounts, nil
}

// ensureDir creates path if it is missing, writable for everyone so that the non-root user inside the container can use it
func ensureDir(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0o777); err != nil {
		return err
	}
	// MkdirAll is subject to the umask, so the leaf directory is chmod'ed explicitly
	return os.Chmod(path, 0o777)
}

const networkName = "zdap_proxy_net"

func findNetwork(cli *client.Client) (*network.Summary, error) {
	networks, err := cli.NetworkList(context.Background(), client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	for _, n := range networks.Items {
		if n.Name == networkName {
			return &n, nil
		}
	}
	return nil, nil
}

func EnsureNetwork(cli *client.Client) (*network.Summary, error) {

	net, err := findNetwork(cli)
	if err != nil {
		return nil, err
	}
	if net != nil {
		return net, nil
	}

	fmt.Println("Creating network", networkName)
	_, err = cli.NetworkCreate(context.Background(), networkName, client.NetworkCreateOptions{
		Attachable: true,
	})

	if err != nil {
		return nil, err
	}

	return findNetwork(cli)
}

func DestroyClone(cloneName string, docker *client.Client, z *zfs.ZFS) error {

	fmt.Println("Destroying clone", cloneName)

	cs, err := docker.ContainerList(context.Background(), client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs.Items {
		for _, name := range c.Names {
			if strings.HasPrefix(name, "/"+cloneName) {
				if c.State == "running" {
					fmt.Println(" - Killing", name)
					d := 0
					err = stopContainer(docker, c.ID, d)
					if err != nil {
						return err
					}
				}
				fmt.Println(" - Removing", name)
				err = removeContainer(docker, c.ID)
				if err != nil {
					return err
				}
			}
		}

	}

	return z.Destroy(cloneName)
}

func removeContainer(docker *client.Client, id string) error {
	_, err := docker.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})
	return err
}

func stopContainer(docker *client.Client, id string, timeout int) error {
	// Ignore ContainerStopResult since it currently is an empty struct
	_, err := docker.ContainerStop(context.Background(), id, client.ContainerStopOptions{Timeout: &timeout})
	if err != nil {
		return err
	}
	w := docker.ContainerWait(context.Background(), id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case wr := <-w.Result:
		if wr.Error != nil {
			return fmt.Errorf("stopContainer error: %s", wr.Error.Message)
		}
		// The exit code (wr.StatusCode) is deliberately ignored: a stop that escalates to SIGKILL (timeout 0 in DestroyClone)
		// exits with 137, and that is still a successfully stopped container.
	case err = <-w.Error:
		if err != nil {
			return fmt.Errorf("stopContainer: %w", err)
		}
	}
	return nil
}
