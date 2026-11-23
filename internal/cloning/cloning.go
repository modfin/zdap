package cloning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"
	"sort"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/bases"
	"github.com/modfin/zdap/internal/servermodel"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
)

type CloneContext struct {
	// Resource is held by value: a clone keeps the configuration it started
	// with, so a resource file reloaded mid-clone cannot change it.
	Resource  internal.Resource
	Docker    *client.Client
	Z         *zfs.ZFS
	ConfigDir string

	NetworkAddress string
	ApiPort        int
}

func (c *CloneContext) CloneResource(lst *zfs.Listing, owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {
	return c.CloneResourceHandlePooling(lst, owner, resourceName, at, false)
}

func (c *CloneContext) CloneResourcePooled(lst *zfs.Listing, owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {
	return c.CloneResourceHandlePooling(lst, owner, resourceName, at, true)
}

func (c *CloneContext) CloneResourceHandlePooling(lst *zfs.Listing, owner string, resourceName string, at time.Time, pooled bool) (*zdap.PublicClone, error) {

	r := c.Resource
	if r.Name == "" {
		return nil, fmt.Errorf("could not find resource %s", resourceName)
	}

	snapName := c.Z.GetDatasetSnapNameAt(resourceName, at)

	clone, err := createClone(lst, owner, snapName, r, c.Docker, c.Z, pooled)
	if err != nil {
		return nil, err
	}
	clone.Server = c.NetworkAddress
	clone.APIPort = c.ApiPort

	return clone, nil
}

func (c *CloneContext) GetResourceSnaps(lst *zfs.Listing, resourceName string) ([]servermodel.ServerInternalSnapshot, error) {
	snaps := lst.Snaps()
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

func (c *CloneContext) GetLatestResourceSnap(lst *zfs.Listing, resourceName string) (servermodel.ServerInternalSnapshot, error) {
	snaps, err := c.GetResourceSnaps(lst, resourceName)
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

// createClone builds a clone: the ZFS dataset, the database container on top
// of it and the proxy in front of that. It is all-or-nothing. If any step
// after the dataset exists fails, the dataset and whatever containers were
// created are torn down again before returning.
func createClone(lst *zfs.Listing, owner string, snap string, r internal.Resource, docker *client.Client, z *zfs.ZFS, clonePooled bool) (clone *zdap.PublicClone, err error) {
	net, err := bases.EnsureNetwork(docker)
	if err != nil {
		return nil, err
	}

	networkConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			net.Name: {},
		},
	}

	snaps := lst.Snaps()

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
	cloneName, path, origin, err := z.CloneDataset(owner, candidate, port, clonePooled, r.CloneZfsProperties())
	if err != nil {
		return nil, err
	}
	fmt.Println(" - clone name", cloneName)
	rt := runtimeFor(origin, r)

	// The dataset exists from here on, so every later failure has to take it
	// down again. Nothing records a clone anywhere else: zdap discovers clones
	// by listing the pool, so a half-built one is indistinguishable from a real
	// one to /resources/:resource/clones and to the clone pool, and nothing
	// ever reclaims it. That is how the pool accumulates datasets with no
	// container, and ports that look taken.
	//
	// This is a flag rather than a check on the returned error because echo
	// recovers panics in handlers: a panic in here does not stop zdapd, it just
	// returns a 500, and the dataset would be left behind all the same.
	done := false
	defer func() {
		if done {
			return
		}
		fmt.Println(" - clone failed, rolling back", cloneName)
		rerr := bases.DestroyClone(cloneName, docker, z)
		if rerr != nil {
			// The leak we were trying to avoid happened anyway. Say so in the
			// error rather than only on stdout, so the caller reports a name
			// someone can clean up by hand.
			fmt.Printf(" - rollback of %s failed: %v\n", cloneName, rerr)
			err = errors.Join(err, fmt.Errorf("rollback of %s failed, it is left on the pool: %w", cloneName, rerr))
		}
	}()

	if rt.Image != r.Docker.Image || rt.Volume != r.Docker.Volume {
		fmt.Printf(" - running %s with %s at %s as recorded on its base (resource file says %s at %s)\n",
			cloneName, rt.Image, rt.Volume, r.Docker.Image, r.Docker.Volume)
	}
	if err := bases.EnsureImage(context.Background(), docker, rt.Image); err != nil {
		return nil, err
	}

	// Pull zdap-proxy image
	proxyImageName := "modfin/zdap-proxy:latest"
	reader, err := docker.ImagePull(context.Background(), proxyImageName, client.ImagePullOptions{})
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	_, err = io.Copy(os.Stdout, reader)
	if err != nil {
		return nil, err
	}

	dbPort, err := network.ParsePort(fmt.Sprintf("%d/tcp", r.Docker.Port))
	if err != nil {
		return nil, err
	}
	proxyTCPPort, err := network.ParsePort(fmt.Sprintf("%d/tcp", port))
	if err != nil {
		return nil, err
	}
	proxyUDPPort, err := network.ParsePort(fmt.Sprintf("%d/udp", port))
	if err != nil {
		return nil, err
	}

	resp, err := docker.ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      rt.Image,
			Entrypoint: r.CloneEntrypoint(),
			Cmd:        r.CloneCmd(),
			Env:        r.CloneEnv(),
			Tty:        false,
			Labels:     map[string]string{"owner": owner},
			// No Domainname: setdomainname(2) caps it at 64 bytes and a clone
			// name is ~74, so runc fails container init with
			// "setdomainname: invalid argument". The container is reachable as
			// cloneName through Name below, which is what the proxy targets,
			// so the domain name was doing nothing anyway.
			ExposedPorts: network.PortSet{
				dbPort: struct{}{},
			},
			Healthcheck: &container.HealthConfig{
				Test:        []string{"CMD-SHELL", r.Docker.Healthcheck},
				Interval:    1 * time.Second,
				Timeout:     1 * time.Second,
				StartPeriod: 1 * time.Second,
				Retries:     5,
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
					Target: rt.Volume,
				},
			},
		},
		NetworkingConfig: networkConfig,
		Name:             cloneName,
	})
	if err != nil {
		return nil, err
	}
	// Ignore ContainerStartResult since it's currently is an empty struct
	_, err = docker.ContainerStart(context.Background(), resp.ID, client.ContainerStartOptions{})
	if err != nil {
		return nil, err
	}

	fmt.Println(" - db container name", cloneName)

	resp, err = docker.ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Config: &container.Config{
			Image: proxyImageName,
			Env: []string{
				fmt.Sprintf("LISTEN_PORT=%d", port),
				fmt.Sprintf("TARGET_ADDRESS=%s:%d", cloneName, r.Docker.Port),
			},
			ExposedPorts: network.PortSet{
				proxyTCPPort: struct{}{},
				proxyUDPPort: struct{}{},
			},
			Labels: map[string]string{"owner": owner},
		},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{
				Name:              "unless-stopped",
				MaximumRetryCount: 0,
			},
			PortBindings: network.PortMap{
				proxyTCPPort: []network.PortBinding{{HostIP: netip.IPv4Unspecified(), HostPort: fmt.Sprintf("%d/tcp", port)}},
				proxyUDPPort: []network.PortBinding{{HostIP: netip.IPv4Unspecified(), HostPort: fmt.Sprintf("%d/udp", port)}},
			},
		},
		NetworkingConfig: networkConfig,
		Name:             fmt.Sprintf("%s-proxy", cloneName),
	})
	if err != nil {
		return nil, err
	}
	_, err = docker.ContainerStart(context.Background(), resp.ID, client.ContainerStartOptions{})
	if err != nil {
		return nil, err
	}
	fmt.Println(" - db proxy name", fmt.Sprintf("tcp://%s-proxy:%d", cloneName, port))

	dates := zfs.TimeReg.FindAll([]byte(cloneName), -1)
	if len(dates) != 2 {
		return nil, fmt.Errorf("did not find 2 snap dates in clone name, got %d", len(dates))
	}
	snappedAt, err := zfs.ParseTimestamp(string(dates[0]))
	if err != nil {
		return nil, err
	}
	createdAt, err := zfs.ParseTimestamp(string(dates[1]))
	if err != nil {
		return nil, err
	}

	// Marking the clone healthy used to mean opening the whole pool, listing
	// every clone and keeping the matching handle; the property is written by
	// name instead.
	fmt.Printf("Setting healthy for %s\n", cloneName)
	err = z.SetUserProperty(cloneName, zfs.PropHealthy, "true")
	if err != nil {
		fmt.Printf("Error when setting healthy prop %s", err)
		return nil, err
	}

	done = true
	return &zdap.PublicClone{
		Name:      cloneName,
		Resource:  r.Name,
		SnappedAt: snappedAt,
		CreatedAt: createdAt,
		Owner:     owner,
		Port:      port,
	}, nil
}

func (c *CloneContext) DestroyClone(lst *zfs.Listing, cloneName string) error {
	clones := lst.Clones()
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

// runtimeFor is the image a clone runs and the path its dataset is mounted at
// inside it: what its base was restored with, wherever the base records it.
//
// The resource file is only the fallback, for bases built before zdap recorded
// either. It describes the next restore, not the bases already on the pool:
// it is changed to follow the source database to a new version, and a clone of
// an older base started on the new image fails to start at best. A changed
// volume is worse, since the image then finds no cluster where it looks for
// one and initialises an empty database over the bind instead of failing.
func runtimeFor(origin zfs.Runtime, r internal.Resource) zfs.Runtime {
	rt := zfs.Runtime{Image: r.Docker.Image, Volume: r.Docker.Volume}
	if origin.Image != "" {
		rt.Image = origin.Image
	}
	if origin.Volume != "" {
		rt.Volume = origin.Volume
	}
	return rt
}
