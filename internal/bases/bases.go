package bases

import (
	"bytes"
	"context"
	"fmt"
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

var baseCreationMutex sync.Mutex

func CreateBaseAndSnap(resourcePath string, r *internal.Resource, docker *client.Client, z *zfs.ZFS, snapCompletedCallback func()) error {
	baseCreationMutex.Lock()
	defer baseCreationMutex.Unlock()

	runScript := func(script string, args ...string) (string, error) {
		cmd := exec.Command(script, args...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		return out.String(), err
	}

	t := time.Now()
	name := z.NewDatasetBaseName(r.Name, t)

	path, err := z.CreateDataset(name, r.Name, t, r.BaseZfsProperties())
	if err != nil {
		return err
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
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeBind,
					Source: path,
					Target: r.Docker.Volume,
				},
			},
		},
		Name: name,
	})
	if err != nil {
		return err
	}

	// Ignore ContainerStartResult since it currently is an empty struct
	_, err = docker.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{})
	if err != nil {
		return err
	}

	fmt.Println("Waiting for container", name, "to become healthy")
	for {
		t, err := docker.ContainerInspect(context.Background(), resp.ID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if t.Container.State != nil && t.Container.State.Health != nil && t.Container.State.Health.Status == container.Healthy {
			break
		}
		time.Sleep(time.Second)
	}
	fmt.Println("Container", name, "is healthy")

	fmt.Println("Retrieving data")
	file, err := runScript(filepath.Join(resourcePath, r.Retrieval))
	if err != nil {
		return err
	}

	fmt.Print("Creating database...")
	defer fmt.Println(" done")
	_, err = runScript(filepath.Join(resourcePath, r.Creation), file, name)
	if err != nil {
		return err
	}

	d := 60 // seconds
	err = stopContainer(docker, resp.ID, d)
	if err != nil {
		return err
	}

	err = removeContainer(docker, resp.ID)
	if err != nil {
		return err
	}

	err = z.SnapDataset(name, r.Name, t)
	if err != nil {
		return err
	}
	if snapCompletedCallback != nil {
		snapCompletedCallback()
	}
	return err
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
