package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/modfin/zdap/internal/api"
	"github.com/modfin/zdap/internal/config"
	"github.com/modfin/zdap/internal/core"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
	"github.com/urfave/cli/v2"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {

	var err error
	var app *core.Core
	var docker *client.Client
	var z *zfs.ZFS

	load := func(c *cli.Context) error {
		cfg := config.FromCli(c)

		configDir := cfg.ConfigDir
		z = zfs.NewZFS(cfg.ZPool)
		docker, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return err
		}

		app, err = core.NewCore(configDir, cfg.NetworkAddress, cfg.APIPort, docker, z)
		if err != nil {
			return err
		}
		return nil
	}

	cliapp := &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "zpool",
				Usage: "The zpool used for the zdap, can also be set by env ZFS_POOL=... ",
			},
			&cli.StringFlag{
				Name:  "config-dir",
				Usage: "The dir where all the resource config is stored, can also be set by env CONFIG_DIR=...",
			},
			&cli.StringFlag{
				Name:  "network-address",
				Usage: "The network address of which clients shall connect to, a ip address or a domain, can also be set by env NETWORK_ADDRESS=...",
			},
			&cli.IntFlag{
				Name:  "api-port",
				Usage: "The port used to open a http api server, can also be set by env API_PORT=...",
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "serve",
				Usage: "starts http daemon for clients to interact with",
				Action: func(c *cli.Context) error {
					return api.Start(config.Get(), app, z)
				},
			},
			{
				Name:  "create",
				Usage: "create things",
				Subcommands: []*cli.Command{
					{
						Name:  "snap",
						Usage: "creates a snap of a resource",
						Flags: []cli.Flag{
							&cli.BoolFlag{
								Name:  "use-existing",
								Usage: "only create snap if base exists",
							},
						},
						Action: func(c *cli.Context) error {
							if !c.Args().Present() {
								return errors.New("resources to create snaps from must be provided")
							}
							for _, resource := range c.Args().Slice() {
								err := app.CreateBaseAndSnap(resource, c.Bool("use-existing"))
								if err != nil {
									return err
								}
							}
							return nil
						},
					},
					{
						Name: "clone",
						Flags: []cli.Flag{
							&cli.TimestampFlag{
								Name:   "from",
								Layout: utils.TimestampFormat,
							},
							&cli.StringFlag{
								Name:        "owner",
								Usage:       "the owner of the clone",
								DefaultText: "host",
							},
						},
						Usage: "creates a snap of a resource",
						Action: func(c *cli.Context) error {
							if !c.Args().Present() {
								return errors.New("resources to create a clone from must be provided")
							}
							if c.Args().Len() != 1 {
								return errors.New("resources to create a clone from must be provided")
							}

							from := c.Timestamp("from")

							resource := c.Args().First()

							dss, err := z.Open()
							if err != nil {
								return err
							}
							defer dss.Close()

							if from == nil {
								snaps, err := app.GetResourceSnaps(dss, resource)
								if err != nil {
									return err
								}
								if len(snaps) == 0 {
									return errors.New("there seems to be no snaps available for resource")
								}
								sort.Slice(snaps, func(i, j int) bool {
									return snaps[i].CreatedAt.After(snaps[j].CreatedAt)
								})
								from = &snaps[0].CreatedAt
							}

							clone, err := app.CloneResource(dss, c.String("owner"), resource, *from)
							if err != nil {
								return err
							}
							fmt.Println("=== Clone ===")
							fmt.Println(clone)

							return nil
						},
					},
				},
			},
			{
				Name:  "list",
				Usage: "lists things",
				Subcommands: []*cli.Command{
					{
						Name:  "resources",
						Usage: "lists available resources / services that can be cloned",
						Action: func(c *cli.Context) error {
							fmt.Printf("== Resources ==\n%s\n", strings.Join(app.GetResourcesNames(), "\n"))
							return nil
						},
					},
					{
						Name:  "snaps",
						Usage: "lists available snaps of resources services that can be cloned",
						Action: func(c *cli.Context) error {

							printSnap := func(resource string) error {
								dss, err := z.Open()
								if err != nil {
									return err
								}
								defer dss.Close()

								snaps, err := app.GetResourceSnaps(dss, resource)
								if err != nil {
									return err
								}
								if len(snaps) == 0 {
									return nil
								}
								sort.Slice(snaps, func(i, j int) bool {
									return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
								})
								fmt.Println(resource)
								for j, snap := range snaps {
									ochar := "├"
									if j == len(snaps)-1 {
										ochar = "└"
									}

									fmt.Printf("%s @ %s\n", ochar, snap.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
								}
								return nil
							}

							fmt.Printf("== Snaps ==\n")
							if c.Args().Present() {
								return printSnap(c.Args().First())
							}
							resources := app.GetResourcesNames()
							sort.Strings(resources)
							for _, resource := range resources {
								err = printSnap(resource)
								if err != nil {
									return err
								}
							}

							return nil
						},
					},
					{
						Name:  "clones",
						Usage: "lists clones that exist",
						Action: func(c *cli.Context) error {

							printClone := func(resource string) error {
								dss, err := z.Open()
								if err != nil {
									return err
								}
								defer dss.Close()

								times, err := app.GetResourceClones(dss, resource)
								if err != nil {
									return err
								}
								if len(times) == 0 {
									return nil
								}
								var keys []time.Time
								for k, arr := range times {
									keys = append(keys, k)
									sort.Slice(arr, func(i, j int) bool {
										return arr[i].CreatedAt.Before(arr[j].CreatedAt)
									})
								}

								sort.Slice(keys, func(i, j int) bool {
									return keys[i].Before(keys[j])
								})
								fmt.Println(resource)
								for j, t := range keys {
									ochar := "├"
									ochar2 := "│"
									if j == len(keys)-1 {
										ochar = "└"
										ochar2 = " "
									}
									fmt.Printf("%s @ %s\n", ochar, t.In(time.UTC).Format(utils.TimestampFormat))
									for i, c := range times[t] {
										char := "├"
										if i == len(times[t])-1 {
											char = "└"
										}

										fmt.Printf("%s %s %s - %s\n", ochar2, char, c.CreatedAt.In(time.UTC).Format(utils.TimestampFormat), c.Owner)
									}
								}
								return nil
							}

							fmt.Printf("== Clones ==\n")
							if c.Args().Present() {
								return printClone(c.Args().First())
							}
							resources := app.GetResourcesNames()
							sort.Strings(resources)
							for _, resource := range resources {
								err = printClone(resource)
								if err != nil {
									return err
								}
							}

							return nil
						},
					},
				},
			},
			{
				Name: "destroy",
				Subcommands: []*cli.Command{
					{
						Name:  "all",
						Usage: "destroys all bases, snaps and clones along with any associated docker images",
						Action: func(context *cli.Context) error {
							return destroyAll(docker, z)
						},
					},
					{
						Name:  "clones",
						Usage: "destroys all clones along with any associated docker images",
						Action: func(context *cli.Context) error {
							return destroyClones(docker, z)
						},
					},
					{
						Name:  "clone",
						Usage: "destroys a clone specific clone along with any associated docker images",
						Action: func(context *cli.Context) error {
							clone := context.Args().First()
							if !strings.Contains(clone, "-clone-") {
								return errors.New("'destroy clone <name>' must contain a valid name")
							}
							return destroyClone(clone, docker, z)
						},
					},
				},
			},
		},
	}
	cliapp.Before = load
	err = cliapp.Run(os.Args)

	if err != nil {
		fmt.Printf("[Error] %v\n", err)
	}
}

func destroyContainer(c types.Container, docker *client.Client) error {
	name := c.ID
	if len(c.Names) > 0 {
		name = c.Names[0]
	}

	if c.State == "running" {
		fmt.Println("- Killing", name)
		//d := time.Millisecond
		d := 1
		err := docker.ContainerStop(context.Background(), c.ID, container.StopOptions{Timeout: &d})
		if err != nil {
			return err
		}
		w, e := docker.ContainerWait(context.Background(), c.ID, container.WaitConditionNotRunning)

		select {
		case <-w:
		case err = <-e:
			return err
		}
	}
	fmt.Println("- Removing", name)
	return docker.ContainerRemove(context.Background(), c.ID, types.ContainerRemoveOptions{
		Force: true,
	})
}

func destroyAll(docker *client.Client, z *zfs.ZFS) error {
	fmt.Println("Destroying Containers")

	cs, err := docker.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs {
		for _, name := range c.Names {
			if strings.HasPrefix(name, "/zdap-") {
				err = destroyContainer(c, docker)
				if err != nil {
					return err
				}
				break
			}
		}

	}

	fmt.Println("Destroying DataSets")
	return z.DestroyAll()
}

func destroyClones(docker *client.Client, z *zfs.ZFS) error {
	fmt.Println("Destroying Containers")

	dss, err := z.Open()
	if err != nil {
		return err
	}
	defer dss.Close()

	clones, err := z.ListClones(dss)
	if err != nil {
		return err
	}
	isClone := map[string]bool{}
	for _, c := range clones {
		isClone[c.Name] = true
	}

	cs, err := docker.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs {
		for _, name := range c.Names {
			name := strings.TrimSuffix(name, "-proxy")
			name = strings.TrimPrefix(name, "/")

			if isClone[name] {
				err = destroyContainer(c, docker)
				if err != nil {
					return err
				}
				break
			}
		}

	}
	fmt.Println("Destroying DataSets")
	for _, c := range clones {
		err = z.Destroy(c.Name)
		if err != nil {
			return err
		}
	}
	return nil
}

func destroyClone(clone string, docker *client.Client, z *zfs.ZFS) error {
	fmt.Println("Destroying Containers")

	dss, err := z.Open()
	if err != nil {
		return err
	}
	defer dss.Close()

	clones, err := z.ListClones(dss)
	if err != nil {
		return err
	}
	isClone := map[string]bool{}
	for _, c := range clones {
		isClone[c.Name] = true
	}
	if !isClone[clone] {
		return fmt.Errorf("could not find clone %s", clone)
	}

	cs, err := docker.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs {
		for _, name := range c.Names {
			if strings.HasPrefix(name, "/"+clone) {
				err = destroyContainer(c, docker)
				if err != nil {
					return err
				}
				break
			}
		}

	}
	return z.Destroy(clone)
}
