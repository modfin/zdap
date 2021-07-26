package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/urfave/cli/v2"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
	"zdap"
	"zdap/internal/utils"
)

type Config struct {
	User    string   `json:"user"`
	Servers []string `json:"servers"`
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {

	cliapp := &cli.App{
		Flags: []cli.Flag{

		},
		Commands: []*cli.Command{
			{
				Name:  "init",
				Usage: "initializes zdap, init <user> <servers>... ",
				Action: func(c *cli.Context) error {

					if c.Args().Len() == 0 {
						return errors.New("you must supply the users and zdap servers as arguments")
					}

					return zdapInit(c.Args().First(), c.Args().Tail()...)
				},
			},
			{
				Name:  "clone",
				Usage: "clone a snapshot",
				Action: func(c *cli.Context) error {
					var err error

					cfg, err := getConfig()
					if err != nil {
						return err
					}

					var server string
					var resource string
					var snap time.Time

					for _, arg := range c.Args().Slice() {
						if strings.HasPrefix(arg, "@") {
							server = arg[1:]
							continue
						}
						if utils.TimestampFormatRegexp.MatchString(arg) {
							snap, err = time.Parse(utils.TimestampFormat, arg)
							if err != nil {
								return err
							}
							continue
						}
						resource = arg
					}

					if server == "" {
						server = cfg.Servers[0] // TODO intelligent selection
					}
					client := zdap.NewClient(cfg.User, server)
					clone, err := client.CloneSnap(resource, snap)
					if err != nil {
						return err
					}
					fmt.Printf("== Override ==\n%s\n", clone.YAML(5432))

					return nil
				},
			},
			{
				Name:  "destroy",
				Usage: "destroy things",
				Subcommands: []*cli.Command{
					{
						Name: "clone",
						Action: func(c *cli.Context) error {

							var err error

							cfg, err := getConfig()
							if err != nil {
								return err
							}

							var servers []string
							var resource string
							var clone time.Time

							for _, arg := range c.Args().Slice() {
								if strings.HasPrefix(arg, "@") {
									servers = append(servers, arg[1:])
									continue
								}
								if utils.TimestampFormatRegexp.MatchString(arg) {
									clone, err = time.Parse(utils.TimestampFormat, arg)
									if err != nil {
										return err
									}
									continue
								}
								resource = arg
							}

							if len(servers) == 0 {
								servers = cfg.Servers
							}

							if len(resource) == 0 {
								return errors.New("clone resource must be provided")
							}

							plural := "clones"
							if !clone.IsZero() {
								plural = "clone " + clone.Format(utils.TimestampFormat)
							}

							for _, s := range servers {
								fmt.Printf("Destroying %s of %s @%s \n", plural, resource, s)
								client := zdap.NewClient(cfg.User, s)
								err = client.DestroyClone(resource, clone)
								if err != nil {
									fmt.Println("[Err]", err)
								}

							}

							return nil
						},
					},
				},
			},
			{
				Name:  "list",
				Usage: "List things",
				Subcommands: []*cli.Command{
					{
						Name: "resources",
						Action: func(c *cli.Context) error {
							cfg, err := getConfig()
							if err != nil {
								return err
							}

							for _, s := range cfg.Servers {
								client := zdap.NewClient(cfg.User, s)
								resources, err := client.GetResources()
								if err != nil {
									return err
								}

								r1 := "├"
								fmt.Printf("@%s\n", s)
								for i, resource := range resources {
									if i == len(resources)-1 {
										r1 = "└"
									}
									fmt.Printf("%s %s\n", r1, resource.Name)
								}
							}
							return nil
						},
					},
					{
						Name: "snaps",
						Action: func(c *cli.Context) error {
							cfg, err := getConfig()
							if err != nil {
								return err
							}

							var name string
							if c.Args().Len() > 0 {
								name = c.Args().First()
							}

							for _, s := range cfg.Servers {
								client := zdap.NewClient(cfg.User, s)
								resources, err := client.GetResources()
								if err != nil {
									return err
								}

								fmt.Printf("@%s\n", s)
								r1 := "├"
								rPipe := "│"
								for i, resource := range resources {
									if name != "" && name != resource.Name {
										continue
									}
									if i == len(resources)-1 {
										r1 = "└"
										rPipe = " "
									}
									s1 := "├"
									fmt.Printf("%s %s\n", r1, resource.Name)
									for j, snap := range resource.Snaps {
										if j == len(resource.Snaps)-1 {
											s1 = "└"
										}
										fmt.Printf("%s %s %s\n", rPipe, s1, snap.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
									}
								}
							}
							return nil
						},
					},
					{
						Name: "clones",
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "format"},
						},
						Action: func(c *cli.Context) error {
							cfg, err := getConfig()
							if err != nil {
								return err
							}

							var servers []string
							var lookingForResources []string
							var cloneName time.Time

							for _, arg := range c.Args().Slice() {
								if strings.HasPrefix(arg, "@") {
									servers = append(servers, arg[1:])
									continue
								}
								if utils.TimestampFormatRegexp.MatchString(arg) {
									cloneName, err = time.Parse(utils.TimestampFormat, arg)
									if err != nil {
										return err
									}
									continue
								}
								lookingForResources = append(lookingForResources, arg)
							}
							if len(servers) == 0 {
								servers = cfg.Servers
							}

							for _, s := range cfg.Servers {
								client := zdap.NewClient(cfg.User, s)
								resources, err := client.GetResources()
								if err != nil {
									return err
								}

								var res []zdap.PublicResource
								for _, resource := range resources {
									if len(lookingForResources) > 0 && !utils.StringSliceContains(lookingForResources, resource.Name) {
										continue
									}

									var snaps []zdap.PublicSnap
									for _, snap := range resource.Snaps {
										var clones []zdap.PublicClone
										for _, clone := range snap.Clones {
											if !cloneName.IsZero() && !cloneName.Equal(clone.CreatedAt) {
												continue
											}
											clones = append(clones, clone)
										}
										snap.Clones = clones

										if len(snap.Clones) > 0 {
											snaps = append(snaps, snap)
										}
									}
									resource.Snaps = snaps

									res = append(res, resource)
								}

								switch c.String("format") {
								case "yaml":
									for _, resource := range res {
										for _, snaps := range resource.Snaps {
											for _, clone := range snaps.Clones {
												fmt.Println(clone.YAML(5432))
												fmt.Println()
											}
										}
									}

								default:
									fmt.Printf("@%s\n", s)

									r1 := "├"
									rPipe := "│"
									sPipe := "│"

									for r, resource := range res {
										lastR := r == len(res)-1
										if lastR {
											r1 = "└"
											rPipe = " "
										}
										fmt.Printf("%s %s\n", r1, resource.Name)
										s1 := "├"
										for s, snaps := range resource.Snaps {
											lastS := s == len(resource.Snaps)-1
											if lastS {
												s1 = "└"
												sPipe = rPipe
											}
											fmt.Printf("%s %s %s\n", rPipe, s1, snaps.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
											c1 := "├"
											for c, clone := range snaps.Clones {
												if c == len(snaps.Clones)-1 {
													c1 = "└"
												}
												fmt.Printf("%s %s %s %s\n", rPipe, sPipe, c1, clone.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
											}
										}
									}
								}

							}
							return nil
						},
					},
				},
			},
		},
	}

	check(cliapp.Run(os.Args))
}

func zdapInit(name string, servers ...string) error {
	dirname, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dirname, ".zdap")
	confFile := filepath.Join(path, "zdap-config")
	err = os.MkdirAll(path, 0755)
	if err != nil {
		return err
	}

	conf := Config{
		User:    name,
		Servers: servers,
	}
	d, err := json.Marshal(conf)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(confFile, d, 0644)
}

func getConfig() (*Config, error) {
	dirname, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	conffile := filepath.Join(dirname, ".zdap", "zdap-config")

	data, err := ioutil.ReadFile(conffile)
	if err != nil {
		return nil, err
	}
	var conf Config
	err = json.Unmarshal(data, &conf)
	return &conf, err
}
