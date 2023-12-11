package cloning

import (
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
	"github.com/modfin/zdap/internal/bases"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
	"regexp"
	"sync"
	"time"
)

type CloneContext struct {
	Resource  *internal.Resource
	Docker    *client.Client
	Z         *zfs.ZFS
	ConfigDir string

	NetworkAddress string
	ApiPort        int
}

func (c *CloneContext) CloneResource(dss *zfs.Dataset, owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {
	return c.CloneResourceHandlePooling(dss, owner, resourceName, at, false)
}

func (c *CloneContext) CloneResourcePooled(dss *zfs.Dataset, owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {
	return c.CloneResourceHandlePooling(dss, owner, resourceName, at, true)
}

func (c *CloneContext) CloneResourceHandlePooling(dss *zfs.Dataset, owner string, resourceName string, at time.Time, pooled bool) (*zdap.PublicClone, error) {

	r := c.Resource
	if r == nil {
		return nil, fmt.Errorf("could not find resource %s", resourceName)
	}

	snapName := c.Z.GetDatasetSnapNameAt(resourceName, at)

	clone, err := createClone(dss, owner, snapName, r, c.Docker, c.Z, pooled)
	if err != nil {
		return nil, err
	}
	clone.Server = c.NetworkAddress
	clone.APIPort = c.ApiPort

	return clone, nil
}

func (c *CloneContext) GetResourceSnaps(dss *zfs.Dataset, resourceName string) ([]zdap.PublicSnap, error) {
	snaps, err := c.Z.ListSnaps(dss)
	if err != nil {
		return nil, err
	}
	snapReg, err := regexp.Compile(fmt.Sprintf("^zdap-%s-base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}@snap$", resourceName))
	if err != nil {
		return nil, err
	}
	var rsnap []zdap.PublicSnap
	for _, snap := range snaps {
		if !snapReg.MatchString(snap.Name) {
			continue
		}
		rsnap = append(rsnap, snap)
	}
	return rsnap, nil
}

var cloneCreationMutex = sync.Mutex{}

func createClone(dss *zfs.Dataset, owner string, snap string, r *internal.Resource, docker *client.Client, z *zfs.ZFS, connectionPooled bool) (*zdap.PublicClone, error) {
	cloneCreationMutex.Lock()
	defer cloneCreationMutex.Unlock()

	net, err := bases.EnsureNetwork(docker)
	if err != nil {
		return nil, err
	}

	networkConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			net.Name: &network.EndpointSettings{},
		},
	}

	snaps, err := z.ListSnaps(dss)
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

	cloneName, path, err := z.CloneDataset(owner, candidate, connectionPooled)
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