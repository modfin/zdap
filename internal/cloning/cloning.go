package cloning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/modfin/henry/slicez"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/bases"
	"github.com/modfin/zdap/internal/servermodel"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
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

func (c *CloneContext) GetResourceSnaps(dss *zfs.Dataset, resourceName string) ([]servermodel.ServerInternalSnapshot, error) {
	snaps, err := c.Z.ListSnaps(dss)
	if err != nil {
		return nil, err
	}
	snapReg, err := regexp.Compile(fmt.Sprintf("^zdap-%s-base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}@snap$", resourceName))
	if err != nil {
		return nil, err
	}
	var rsnap []servermodel.ServerInternalSnapshot
	for _, snap := range snaps {
		if !snapReg.MatchString(snap.Name) {
			continue
		}
		rsnap = append(rsnap, snap)
	}
	return rsnap, nil
}

func (c *CloneContext) GetLatestResourceSnap(dss *zfs.Dataset, resourceName string) (servermodel.ServerInternalSnapshot, error) {
	snaps, err := c.GetResourceSnaps(dss, resourceName)
	if err != nil || snaps == nil {
		return servermodel.ServerInternalSnapshot{}, err
	}

	sort.Slice(snaps, func(i, j int) bool {
		return snaps[j].CreatedAt.Before(snaps[i].CreatedAt)
	})

	if len(snaps) == 0 {
		return servermodel.ServerInternalSnapshot{}, fmt.Errorf("unable to find snap for %s", resourceName)
	}

	return snaps[0], nil
}

func createClone(dss *zfs.Dataset, owner string, snap string, r *internal.Resource, docker *client.Client, z *zfs.ZFS, clonePooled bool) (*zdap.PublicClone, error) {
	net, err := bases.EnsureNetwork(docker)
	if err != nil {
		return nil, err
	}

	networkConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			net.Name: {},
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

	port, err := utils.GetFreePort()
	if err != nil {
		return nil, err
	}
	cloneName, path, err := z.CloneDataset(owner, candidate, port, clonePooled, r)
	if err != nil {
		return nil, err
	}
	fmt.Println(" - clone name", cloneName)

	// Pull zdap-proxy image
	proxyImageName := "modfin/zdap-proxy:latest"
	reader, err := docker.ImagePull(context.Background(), proxyImageName, image.PullOptions{})
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(os.Stdout, reader)
	if err != nil {
		return nil, err
	}

	resp, err := docker.ContainerCreate(context.Background(), &container.Config{
		Image:      r.Docker.Image,
		Entrypoint: r.CloneEntrypoint(),
		Cmd:        r.CloneCmd(),
		Env:        r.CloneEnv(),
		Tty:        false,
		Labels:     map[string]string{"owner": owner},
		Domainname: cloneName,
		ExposedPorts: nat.PortSet{
			nat.Port(fmt.Sprintf("%d/tcp", r.Docker.Port)): struct{}{},
		},
		Healthcheck: &container.HealthConfig{
			Test:        []string{"CMD-SHELL", r.Docker.Healthcheck},
			Interval:    1 * time.Second,
			Timeout:     1 * time.Second,
			StartPeriod: 1 * time.Second,
			Retries:     5,
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
	}, networkConfig, nil, cloneName)
	if err != nil {
		return nil, err
	}
	err = docker.ContainerStart(context.Background(), resp.ID, container.StartOptions{})
	if err != nil {
		return nil, err
	}

	fmt.Println(" - db container name", cloneName)

	resp, err = docker.ContainerCreate(context.Background(), &container.Config{
		Image: proxyImageName,
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
	err = docker.ContainerStart(context.Background(), resp.ID, container.StartOptions{})
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

	dss2, err := z.Open()
	if err != nil {
		return nil, err
	}
	defer dss2.Close()

	clones, err := z.ListClones(dss2)
	if err != nil {
		return nil, err
	}

	matchingClones := slicez.Filter(clones, func(c servermodel.ServerInternalClone) bool {
		return c.Name == cloneName
	})
	if len(matchingClones) > 0 {
		fmt.Printf("Setting healthy for %s\n", cloneName)
		m := matchingClones[0]
		err := z.SetUserProperty(*m.Dataset, zfs.PropHealthy, "true")
		if err != nil {
			fmt.Printf("Error when setting healthy prop %s", err)
			return nil, err
		}
		defer m.Dataset.Close()
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

func (c *CloneContext) DestroyClone(dss *zfs.Dataset, cloneName string) error {
	clones, err := c.Z.ListClones(dss)
	if err != nil {
		return err
	}
	var contain bool
	for _, c := range clones {
		if c.Name == cloneName {
			contain = true
			break
		}
	}
	if !contain {
		return fmt.Errorf("clone, %s, does not exist", cloneName)
	}

	return bases.DestroyClone(cloneName, c.Docker, c.Z)
}
