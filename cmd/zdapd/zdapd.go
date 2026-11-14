package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/modfin/zdap/internal/api"
	"github.com/modfin/zdap/internal/config"
	"github.com/modfin/zdap/internal/core"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
	"github.com/urfave/cli/v2"
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
		docker, err = client.New(client.FromEnv)
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
				Name:  "check-zfs",
				Usage: "checks the zfs channel program and zpool paths that replace go-libzfs, and prints what they return",
				Action: func(c *cli.Context) error {
					// Exit non-zero on failure, so the check can be scripted.
					// zdapd's other commands only print their errors.
					if err := checkZFS(c.Context, z); err != nil {
						return cli.Exit(fmt.Sprintf("[Error] %v", err), 1)
					}
					return nil
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
								err := app.CreateBaseAndSnap(c.Context, resource, c.Bool("use-existing"))
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

							lst, err := z.Load(c.Context)
							if err != nil {
								return err
							}

							if from == nil {
								snaps, err := app.GetResourceSnaps(lst, resource)
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

							clone, err := app.CloneResource(lst, c.String("owner"), resource, *from)
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
								lst, err := z.Load(c.Context)
								if err != nil {
									return err
								}

								snaps, err := app.GetResourceSnaps(lst, resource)
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
								lst, err := z.Load(c.Context)
								if err != nil {
									return err
								}

								times, err := app.GetResourceClones(lst, resource)
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
						Action: func(c *cli.Context) error {
							return destroyClones(c.Context, docker, z)
						},
					},
					{
						Name:  "clone",
						Usage: "destroys a clone specific clone along with any associated docker images",
						Action: func(c *cli.Context) error {
							clone := c.Args().First()
							if !strings.Contains(clone, "-clone-") {
								return errors.New("'destroy clone <name>' must contain a valid name")
							}
							return destroyClone(c.Context, clone, docker, z)
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

func destroyContainer(c container.Summary, docker *client.Client) error {
	name := c.ID
	if len(c.Names) > 0 {
		name = c.Names[0]
	}

	if c.State == "running" {
		fmt.Println("- Killing", name)
		d := 0
		// Ignore ContainerStopResult since it currently is an empty struct
		_, err := docker.ContainerStop(context.Background(), c.ID, client.ContainerStopOptions{Timeout: &d})
		if err != nil {
			return err
		}
		w := docker.ContainerWait(context.Background(), c.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
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
	}
	fmt.Println("- Removing", name)
	_, err := docker.ContainerRemove(context.Background(), c.ID, client.ContainerRemoveOptions{Force: true})
	return err
}

func destroyAll(docker *client.Client, z *zfs.ZFS) error {
	fmt.Println("Destroying Containers")

	cs, err := docker.ContainerList(context.Background(), client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs.Items {
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

func destroyClones(ctx context.Context, docker *client.Client, z *zfs.ZFS) error {
	fmt.Println("Destroying Containers")

	lst, err := z.Load(ctx)
	if err != nil {
		return err
	}

	clones := lst.Clones()
	isClone := map[string]bool{}
	for _, c := range clones {
		isClone[c.Name] = true
	}

	cs, err := docker.ContainerList(context.Background(), client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs.Items {
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

func destroyClone(ctx context.Context, clone string, docker *client.Client, z *zfs.ZFS) error {
	fmt.Println("Destroying Containers")

	lst, err := z.Load(ctx)
	if err != nil {
		return err
	}

	clones := lst.Clones()
	isClone := map[string]bool{}
	for _, c := range clones {
		isClone[c.Name] = true
	}
	if !isClone[clone] {
		return fmt.Errorf("could not find clone %s", clone)
	}

	cs, err := docker.ContainerList(context.Background(), client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range cs.Items {
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

// checkZFS exercises the paths that are being migrated off go-libzfs and
// prints what they return, so they can be verified without the API.
//
// The channel program needs root, like the rest of zdapd, so this is the
// quickest way to tell a permission problem from a broken program:
//
//	sudo env ZPOOL=zdap/1/databases CONFIG_DIR=/zdap/1/config go run ./cmd/zdapd check-zfs
func checkZFS(ctx context.Context, z *zfs.ZFS) error {
	fmt.Println("== channel program: list datasets ==")
	lst, err := z.Load(ctx)
	if err != nil {
		return fmt.Errorf("channel program failed: %w", err)
	}

	var bases, other []string
	for _, name := range lst.Names() {
		switch {
		case zfs.IsCloneName(name), zfs.IsSnapName(name):
			continue // listed with their parsed properties below
		case zfs.IsBaseName(name):
			bases = append(bases, name)
		default:
			other = append(other, name)
		}
	}

	snaps := lst.Snaps()
	clones := lst.Clones()
	fmt.Printf(" %d bases, %d snapshots, %d clones, %d unrecognised\n\n",
		len(bases), len(snaps), len(clones), len(other))

	for _, name := range bases {
		fmt.Println(" base    ", name)
	}
	// The properties are the part worth looking at: they come back from
	// zfs.list.user_properties in the channel program, and a dataset that does
	// not carry one is not an error. A snapshot with an empty resource or a
	// zero created_at means the program or the parsing is wrong.
	for _, s := range snaps {
		fmt.Printf(" snapshot %s resource=%q created=%s\n",
			s.Name, s.Resource, fmtTime(s.CreatedAt))
	}
	now := time.Now()
	for _, c := range clones {
		// Whether the clone pool considers it expired, which is the one place
		// a zdap timestamp is compared against wall clock time.
		expiry := fmtTimePtr(c.ExpiresAt)
		if c.ExpiresAt != nil && c.ExpiresAt.Before(now) {
			expiry += " (EXPIRED, due for pruning)"
		}
		fmt.Printf(" clone    %s resource=%q owner=%q created=%s snapped=%s pooled=%t healthy=%t port=%d expires=%s\n",
			c.Name, c.Resource, c.Owner, fmtTime(c.CreatedAt), fmtTime(c.SnappedAt),
			c.ClonePooled, c.Healthy, c.Port, expiry)
	}
	for _, name := range other {
		fmt.Println(" ?       ", name)
	}

	fmt.Println()
	fmt.Println("== zpool list: pool space ==")
	space, err := z.Space(ctx)
	if err != nil {
		return fmt.Errorf("zpool list failed: %w", err)
	}
	fmt.Printf(" total %d\n used  %d\n free  %d\n", space.Total, space.Used, space.Free)
	if space.Used+space.Free != space.Total {
		return fmt.Errorf("used + free (%d) does not add up to total (%d)", space.Used+space.Free, space.Total)
	}

	fmt.Println()
	fmt.Println("both paths work; check the resource and created values above against")
	fmt.Println("zfs get -r all " + z.Pool() + " before trusting them")
	return nil
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "unset"
	}
	return zfs.FormatTimestamp(t)
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "none"
	}
	return fmtTime(*t)
}
