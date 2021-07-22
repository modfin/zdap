package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"zdap/internal"
	"zdap/internal/config"
	"zdap/internal/utils"
	"zdap/internal/zfs"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {

	z := zfs.NewZFS(config.Get().ZFSPool)

	var resourcesPaths []string
	err := filepath.Walk(config.Get().ConfigDir, func(path string, info os.FileInfo, err error) error {
		if strings.HasSuffix(path, ".resource.yml") {
			resourcesPaths = append(resourcesPaths, path)
		}
		return nil
	})
	check(err)

	var resources []internal.Resource
	for _, path := range resourcesPaths {
		b, err := ioutil.ReadFile(path)
		check(err)

		var r internal.Resource
		err = yaml.Unmarshal(b, &r)
		check(err)
		resources = append(resources, r)
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	check(err)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "destroy":
			Destroy(cli, z)
			os.Exit(0)
		case "list":
			var l []string
			switch len(os.Args) {
			case 2:
				l, err = z.List()
				check(err)
			case 3:
				switch os.Args[2] {
				case "bases":
					l, err = z.ListBases()
					check(err)
				case "snaps":
					l, err = z.ListSnaps()
					check(err)
				case "clones":
					l, err = z.ListClones()
					check(err)
				}
			}
			sort.Strings(l)
			fmt.Println(strings.Join(l, "\n"))
			os.Exit(0)
		case "test-bases":
			for _, r := range resources {
				CreateBase(r, cli, z)
			}
			os.Exit(0)
		case "test-clones":
			for _, r := range resources {
				CreateClone(r, cli, z)
			}
			os.Exit(0)
		}

	}

}

func Destroy(cli *client.Client, z *zfs.ZFS) {

	var singel bool
	var toDestroy = "zdap-"
	if len(os.Args) > 2 {
		toDestroy = os.Args[2]
		singel = true
	}

	if singel {
		fmt.Println("Destroying Container", toDestroy)
	} else {
		fmt.Println("Destroying Containers")
	}

	cs, err := cli.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	check(err)
	for _, c := range cs {
		for _, name := range c.Names {

			if strings.HasPrefix(name, "/"+toDestroy) {
				if c.State == "running" {
					fmt.Println("- Killing", name)
					d := time.Millisecond
					check(cli.ContainerStop(context.Background(), c.ID, &d))
					w, e := cli.ContainerWait(context.Background(), c.ID, container.WaitConditionNotRunning)

					select {
					case <-w:
					case err = <-e:
						check(err)
					}
				}
				fmt.Println("- Removing", name)
				check(cli.ContainerRemove(context.Background(), c.ID, types.ContainerRemoveOptions{
					Force: true,
				}))
			}
		}

	}

	if singel {
		fmt.Println("Destroying Volume", toDestroy)
		check(z.Destroy(toDestroy))
	} else {
		fmt.Println("Destroying Volumes")
		check(z.DestroyAll())
	}

}

func CreateClone(r internal.Resource, cli *client.Client, z *zfs.ZFS) {

	networks, err := cli.NetworkList(context.Background(), types.NetworkListOptions{})
	check(err)
	fmt.Println(networks)
	var network types.NetworkResource
	for _, n := range networks{
		if n.Name == "zdap"{
			network = n
			break
		}
	}
	fmt.Println(network)

	name := r.Name
	snaps, err := z.ListSnaps()
	check(err)

	var candidate string
	for _, snap := range snaps {
		if strings.HasPrefix(snap, fmt.Sprintf("zdap-%s-base", name)) {
			if snap > candidate {
				candidate = snap
			}
		}
	}
	if len(candidate) == 0 {
		check(errors.New("could not find snap"))
	}

	fmt.Println("Creating clone from", candidate)

	cloneName, path, err := z.CloneDataset(candidate)
	check(err)
	fmt.Println(" - clone name", cloneName)

	resp, err := cli.ContainerCreate(context.Background(), &container.Config{
		Image: r.Docker.Image,
		Env:   r.Docker.Env,
		Tty:   false,
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
	}, nil,nil, cloneName)
	check(err)

	check(cli.ContainerStart(context.Background(), resp.ID, types.ContainerStartOptions{}))
	check(cli.NetworkConnect(context.Background(), network.ID, resp.ID, nil))
	fmt.Println(" - db container name", cloneName)

	freeport, err := utils.GetFreePort()
	check(err)

	resp, err = cli.ContainerCreate(context.Background(), &container.Config{
		Image: "crholm/zdap-proxy:latest",
		Env: []string{
			fmt.Sprintf("LISTEN_PORT=%d", freeport),
			fmt.Sprintf("TARGET_ADDRESS=%s:%d", cloneName, r.Docker.Port),
		},
		ExposedPorts: nat.PortSet{
			nat.Port(fmt.Sprintf("%d/tcp", freeport)): struct{}{},
			nat.Port(fmt.Sprintf("%d/udp", freeport)): struct{}{},
		},
		Domainname: fmt.Sprintf("%s-proxy", cloneName),

	}, &container.HostConfig{
		RestartPolicy: container.RestartPolicy{
			Name:              "unless-stopped",
			MaximumRetryCount: 0,
		},
		PortBindings: nat.PortMap{
			nat.Port(fmt.Sprintf("%d/tcp", freeport)): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d/tcp", freeport)}},
			nat.Port(fmt.Sprintf("%d/udp", freeport)): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d/udp", freeport)}},
		},
	}, nil, nil, fmt.Sprintf("%s-proxy", cloneName))
	check(err)
	check(cli.ContainerStart(context.Background(), resp.ID, types.ContainerStartOptions{}))
	check(cli.NetworkConnect(context.Background(), network.ID, resp.ID, nil))
	fmt.Println(" - db proxy name", fmt.Sprintf("tcp://%s-proxy:%d", cloneName, freeport))

}

func CreateBase(r internal.Resource, cli *client.Client, z *zfs.ZFS) {

	name := fmt.Sprintf("zdap-%s-base-%s", r.Name, time.Now().Format("2006-01-02T15_04_05"))

	path, err := z.CreateDataset(name)
	check(err)

	ctx := context.Background()
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: r.Docker.Image,
		Env:   r.Docker.Env,
		Tty:   false,
		Healthcheck: &container.HealthConfig{
			Test:        []string{"CMD-SHELL", r.Docker.Healthcheck},
			Interval:    10 * time.Second,
			Timeout:     2 * time.Second,
			StartPeriod: 3 * time.Second,
			Retries:     2,
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
	check(err)

	err = cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	check(err)

	fmt.Println("Waiting for container", name, "to become healthy")
	for {
		t, err := cli.ContainerInspect(context.Background(), resp.ID)
		check(err)
		if t.State.Health != nil && strings.ToLower(t.State.Health.Status) == "healthy" {
			break
		}
		time.Sleep(time.Second)
	}
	fmt.Println("Container", name, "is healthy")

	fmt.Println("Retrieving data")
	file, err := RunScript(filepath.Join(config.Get().ConfigDir, r.Retrieval))
	check(err)
	fmt.Println(file)

	fmt.Println("Creating database")
	out, err := RunScript(filepath.Join(config.Get().ConfigDir, r.Creation), file, name)
	check(err)
	fmt.Println(out)

	d := time.Millisecond
	check(cli.ContainerStop(context.Background(), resp.ID, &d))
	w, e := cli.ContainerWait(context.Background(), resp.ID, container.WaitConditionNotRunning)
	select {
	case <-w:
	case err = <-e:
		check(err)
	}

	check(cli.ContainerRemove(context.Background(), resp.ID, types.ContainerRemoveOptions{Force: true}))

	check(z.SnapDataset(name))
}

func RunScript(script string, args ...string) (string, error) {

	cmd := exec.Command(script, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Run()

	return out.String(), err
}
