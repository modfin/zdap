package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var baseCreationMutex = sync.Mutex{}

func createBaseAndSnap(resourcePath string, r *internal.Resource, docker *client.Client, z *zfs.ZFS) error {
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

	path, err := z.CreateDataset(name, r.Name, t)
	if err != nil {
		return err
	}

	ctx := context.Background()
	resp, err := docker.ContainerCreate(ctx, &container.Config{
		Image: r.Docker.Image,
		Env:   r.Docker.Env,
		Tty:   false,
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

	err = docker.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
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

	d := time.Millisecond
	err = docker.ContainerStop(context.Background(), resp.ID, &d)
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

	err = docker.ContainerRemove(context.Background(), resp.ID, types.ContainerRemoveOptions{Force: true})
	if err != nil {
		return err
	}

	return z.SnapDataset(name, r.Name, t)
}

const networkName = "zdap_proxy_net"

func findNetwork(cli *client.Client) (*types.NetworkResource, error) {
	networks, err := cli.NetworkList(context.Background(), types.NetworkListOptions{})
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

func ensureNetwork(cli *client.Client) (*types.NetworkResource, error) {

	net, err := findNetwork(cli)
	if err != nil {
		return nil, err
	}
	if net != nil {
		return net, nil
	}

	fmt.Println("Creating network", networkName)
	_, err = cli.NetworkCreate(context.Background(), networkName, types.NetworkCreate{
		Attachable: true,
	})

	if err != nil {
		return nil, err
	}

	return findNetwork(cli)
}

var cloneCreationMutex = sync.Mutex{}

func createClone(owner string, snap string, r *internal.Resource, docker *client.Client, z *zfs.ZFS) (*zdap.PublicClone, error) {
	cloneCreationMutex.Lock()
	defer cloneCreationMutex.Unlock()

	net, err := ensureNetwork(docker)
	if err != nil {
		return nil, err
	}

	networkConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			net.Name: &network.EndpointSettings{},
		},
	}

	snaps, err := z.ListSnaps()
	if err != nil {
		return nil, err
	}

	var candidate string
	for _, s := range snaps {
		if s.Name == snap {
			candidate = s.Name
		}
	}
	if len(candidate) == 0 {
		return nil, errors.New("could not find snap")
	}

	fmt.Println("Creating clone from", candidate)

	cloneName, path, err := z.CloneDataset(owner, candidate)
	if err != nil {
		return nil, err
	}
	fmt.Println(" - clone name", cloneName)

	resp, err := docker.ContainerCreate(context.Background(), &container.Config{
		Image:      r.Docker.Image,
		Env:        r.Docker.Env,
		Tty:        false,
		Labels:     map[string]string{"owner": owner},
		Domainname: cloneName,
		ExposedPorts: nat.PortSet{
			nat.Port(fmt.Sprintf("%d/tcp", r.Docker.Port)): struct{}{},
		},
	}, &container.HostConfig{
		RestartPolicy: container.RestartPolicy{
			Name:              "unless-stopped",
			MaximumRetryCount: 0,
		},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: path,
				Target: r.Docker.Volume,
			},
		},
	}, networkConfig, nil, cloneName)
	if err != nil {
		return nil, err
	}
	err = docker.ContainerStart(context.Background(), resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return nil, err
	}

	fmt.Println(" - db container name", cloneName)

	port, err := utils.GetFreePort()
	if err != nil {
		return nil, err
	}

	resp, err = docker.ContainerCreate(context.Background(), &container.Config{
		Image: "crholm/zdap-proxy:latest",
		Env: []string{
			fmt.Sprintf("LISTEN_PORT=%d", port),
			fmt.Sprintf("TARGET_ADDRESS=%s:%d", cloneName, r.Docker.Port),
		},
		ExposedPorts: nat.PortSet{
			nat.Port(fmt.Sprintf("%d/tcp", port)): struct{}{},
			nat.Port(fmt.Sprintf("%d/udp", port)): struct{}{},
		},
		Labels:     map[string]string{"owner": owner},
		Domainname: fmt.Sprintf("%s-proxy", cloneName),
	}, &container.HostConfig{
		RestartPolicy: container.RestartPolicy{
			Name:              "unless-stopped",
			MaximumRetryCount: 0,
		},
		PortBindings: nat.PortMap{
			nat.Port(fmt.Sprintf("%d/tcp", port)): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d/tcp", port)}},
			nat.Port(fmt.Sprintf("%d/udp", port)): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d/udp", port)}},
		},
	}, networkConfig, nil, fmt.Sprintf("%s-proxy", cloneName))
	if err != nil {
		return nil, err
	}
	err = docker.ContainerStart(context.Background(), resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return nil, err
	}
	fmt.Println(" - db proxy name", fmt.Sprintf("tcp://%s-proxy:%d", cloneName, port))

	dates := zfs.TimeReg.FindAll([]byte(cloneName), -1)
	if len(dates) != 2 {
		return nil, fmt.Errorf("did not find 2 snap dates in clone name, got %d", len(dates))
	}
	snappedAt, err := time.Parse(zfs.TimestampFormat, string(dates[0]))
	if err != nil {
		return nil, err
	}
	createdAt, err := time.Parse(zfs.TimestampFormat, string(dates[1]))
	if err != nil {
		return nil, err
	}

	return &zdap.PublicClone{
		Name:      cloneName,
		Resource:  r.Name,
		SnappedAt: snappedAt,
		CreatedAt: createdAt,
		Owner:     owner,
		Port:      port,
	}, nil
}

func destroyClone(cloneName string, docker *client.Client, z *zfs.ZFS) error {

	fmt.Println("Destroying clone", cloneName)

	cs, err := docker.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs {
		for _, name := range c.Names {
			if strings.HasPrefix(name, "/"+cloneName) {
				if c.State == "running" {
					fmt.Println(" - Killing", name)
					d := time.Millisecond
					err = docker.ContainerStop(context.Background(), c.ID, &d)
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
				err = docker.ContainerRemove(context.Background(), c.ID, types.ContainerRemoveOptions{
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
