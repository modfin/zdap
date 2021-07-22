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
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"zdap/internal"
	"zdap/internal/config"
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

	fmt.Println(resourcesPaths)

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
		if os.Args[1] == "destroy" {
			Destroy(cli, z)
			os.Exit(0)
		}
	}

	fmt.Println("List", fmt.Sprint(z.List()))
	fmt.Println("Bases", fmt.Sprint(z.ListBases()))
	fmt.Println("Snaps", fmt.Sprint(z.ListSnaps()))
	fmt.Println("Clones", fmt.Sprint(z.ListClones()))

	for _, r := range resources{
		CreateBase(r, cli, z)
	}

	for _, r := range resources {
		CreateClone(r, cli, z)
	}

	for _, r := range resources {
		CreateClone(r, cli, z)
	}


}

func Destroy(cli *client.Client, z *zfs.ZFS) {

	var singel bool
	var toDestroy string = "zdap-"
	if len(os.Args) > 2 {
		toDestroy = os.Args[2]
		singel = true
	}


	if singel{
		fmt.Println("Destroying Container", toDestroy)
	}else{
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

	if singel{
		fmt.Println("Destroying Volume", toDestroy)
		check(z.Destroy(toDestroy))
	}else{
		fmt.Println("Destroying Volumes")
		check(z.DestroyAll())
	}


}

func CreateClone(r internal.Resource, cli *client.Client, z *zfs.ZFS) {

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

	resp, err := cli.ContainerCreate(context.Background(), &container.Config{
		Image: r.Docker.Image,
		Env:   r.Docker.Env,
		Tty:   false,
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
	}, nil, nil, cloneName)
	check(err)

	err = cli.ContainerStart(context.Background(), resp.ID, types.ContainerStartOptions{})
	check(err)

	time.Sleep(2*time.Second)

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
