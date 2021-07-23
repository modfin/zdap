package main

import (
	"fmt"
	"github.com/docker/docker/client"
	"time"
	"zdap/internal/config"
	"zdap/internal/core"
	"zdap/internal/zfs"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {

	configDir := config.Get().ConfigDir
	z := zfs.NewZFS(config.Get().ZFSPool)
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	check(err)

	app, err := core.NewCore(configDir, docker, z)
	check(err)
	app.ExecAllCronjobs()

	resources := app.GetResources()
	for _, r := range resources{
		snaps, err := app.GetResourceSnaps(r)
		check(err)
		var max time.Time
		for _, s := range snaps{
			if s.After(max) {
				max = s
			}
		}
		clone, err := app.CloneResource(r, max)
		check(err)
		fmt.Printf("[Clone] %+v", clone)
	}


	//if len(os.Args) > 1 {
	//	switch os.Args[1] {
	//	case "destroy":
	//		Destroy(cli, z)
	//		os.Exit(0)
	//	case "list":
	//		var l []string
	//		switch len(os.Args) {
	//		case 2:
	//			l, err = z.List()
	//			check(err)
	//		case 3:
	//			switch os.Args[2] {
	//			case "bases":
	//				l, err = z.ListBases()
	//				check(err)
	//			case "snaps":
	//				l, err = z.ListSnaps()
	//				check(err)
	//			case "clones":
	//				l, err = z.ListClones()
	//				check(err)
	//			}
	//		}
	//		sort.Strings(l)
	//		fmt.Println(strings.Join(l, "\n"))
	//		os.Exit(0)
	//	case "test-bases":
	//		for _, r := range resources {
	//			CreateBase(r, cli, z)
	//		}
	//		os.Exit(0)
	//	case "test-clones":
	//		for _, r := range resources {
	//			CreateClone(r, cli, z)
	//		}
	//		os.Exit(0)
	//	}
	//
	//}

}

//func Destroy(cli *client.Client, z *zfs.ZFS) {
//
//	var singel bool
//	var toDestroy = "zdap-"
//	if len(os.Args) > 2 {
//		toDestroy = os.Args[2]
//		singel = true
//	}
//
//	if singel {
//		fmt.Println("Destroying Container", toDestroy)
//	} else {
//		fmt.Println("Destroying Containers")
//	}
//
//	cs, err := cli.ContainerList(context.Background(), types.ContainerListOptions{All: true})
//	check(err)
//	for _, c := range cs {
//		for _, name := range c.Names {
//
//
//
//			if strings.HasPrefix(name, "/"+toDestroy) || (toDestroy == "clones" && strings.Contains(name, "-clone-")) {
//				if c.State == "running" {
//					fmt.Println("- Killing", name)
//					d := time.Millisecond
//					check(cli.ContainerStop(context.Background(), c.ID, &d))
//					w, e := cli.ContainerWait(context.Background(), c.ID, container.WaitConditionNotRunning)
//
//					select {
//					case <-w:
//					case err = <-e:
//						check(err)
//					}
//				}
//				fmt.Println("- Removing", name)
//				check(cli.ContainerRemove(context.Background(), c.ID, types.ContainerRemoveOptions{
//					Force: true,
//				}))
//			}
//		}
//
//	}
//
//	if toDestroy ==  "clones"{
//		clones, err := z.ListClones()
//		check(err)
//		for _, c := range clones{
//			check(z.Destroy(c))
//		}
//	}else if singel {
//		fmt.Println("Destroying Volume", toDestroy)
//		check(z.Destroy(toDestroy))
//	} else {
//		fmt.Println("Destroying Volumes")
//		check(z.DestroyAll())
//	}
//
//}

