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

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
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
	resp, err := docker.ContainerCreate(ctx, &container.Config{
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
	}, &container.HostConfig{
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
	}, nil, nil, name)
	if err != nil {
		return err
	}

	err = docker.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		return err
	}

	fmt.Println("Waiting for container", name, "to become healthy")
	for {
		t, err := docker.ContainerInspect(context.Background(), resp.ID)
		if err != nil {
			return err
		}
		if t.State.Health != nil && strings.ToLower(t.State.Health.Status) == "healthy" {
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
	err = docker.ContainerStop(context.Background(), resp.ID, container.StopOptions{Timeout: &d})
	if err != nil {
		return err
	}
	w, e := docker.ContainerWait(context.Background(), resp.ID, container.WaitConditionNotRunning)
	select {
	case <-w:
	case err = <-e:
		if err != nil {
			return err
		}
	}

	err = docker.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
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
	networks, err := cli.NetworkList(context.Background(), network.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, n := range networks {
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
	_, err = cli.NetworkCreate(context.Background(), networkName, network.CreateOptions{
		Attachable: true,
	})

	if err != nil {
		return nil, err
	}

	return findNetwork(cli)
}

func DestroyClone(cloneName string, docker *client.Client, z *zfs.ZFS) error {

	fmt.Println("Destroying clone", cloneName)

	cs, err := docker.ContainerList(context.Background(), container.ListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs {
		for _, name := range c.Names {
			if strings.HasPrefix(name, "/"+cloneName) {
				if c.State == "running" {
					fmt.Println(" - Killing", name)
					d := 0
					err = docker.ContainerStop(context.Background(), c.ID, container.StopOptions{Timeout: &d})
					if err != nil {
						return err
					}
					w, e := docker.ContainerWait(context.Background(), c.ID, container.WaitConditionNotRunning)

					select {
					case <-w:
					case err = <-e:
						if err != nil {
							return err
						}
					}
				}
				fmt.Println(" - Removing", name)
				err = docker.ContainerRemove(context.Background(), c.ID, container.RemoveOptions{
					Force: true,
				})
				if err != nil {
					return err
				}
			}
		}

	}

	return z.Destroy(cloneName)

}
