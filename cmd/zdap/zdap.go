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

					for _, arg := range c.Args().Slice(){
						if strings.HasPrefix(arg, "@"){
							server = arg[1:]
							continue
						}
						if utils.TimestampFormatRegexp.MatchString(arg){
							snap, err = time.Parse(utils.TimestampFormat, arg)
							if err != nil{
								return err
							}
							continue
						}
						resource = arg
					}

					if server == ""{
						server = cfg.Servers[0] // TODO intelligent selection
					}
					client := zdap.NewClient(cfg.User, server)
					clone, err := client.CloneSnap(resource, snap)
					if err != nil{
						return err
					}
					address := strings.Split(server, ":")[0]
					fmt.Printf("== Override ==\n%s\n", clone.YAML(address, 5432))

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

							for _, arg := range c.Args().Slice(){
								if strings.HasPrefix(arg, "@"){
									servers = append(servers, arg[1:])
									continue
								}
								if utils.TimestampFormatRegexp.MatchString(arg) {
									clone, err = time.Parse(utils.TimestampFormat, arg)
									if err != nil{
										return err
									}
									continue
								}
								resource = arg
							}

							if len(servers) == 0{
								servers = cfg.Servers
							}

							if len(resource) == 0{
								return errors.New("clone resource must be provided")
							}

							plural := "clones"
							if !clone.IsZero(){
								plural = "clone " + clone.Format(utils.TimestampFormat)
							}

							for _, s := range servers{
								fmt.Printf("Destroying %s of %s @%s \n", plural, resource, s)
								client := zdap.NewClient(cfg.User, s)
								err = client.DestroyClone(resource, clone)
								if err != nil{
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

								fmt.Println("@", s)
								for _, resource := range resources {
									fmt.Println(" ", resource.Name)
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
							if c.Args().Len() > 0{
								name = c.Args().First()
							}

							for _, s := range cfg.Servers {
								client := zdap.NewClient(cfg.User, s)
								resources, err := client.GetResources()
								if err != nil {
									return err
								}

								fmt.Println("@", s)
								for _, resource := range resources {
									if name != "" && name != resource.Name{
										continue
									}

									fmt.Println(" ", resource.Name)
									for _, snap := range resource.Snaps {
										fmt.Println("  ", snap.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
									}
								}
							}
							return nil
						},
					},
					{
						Name: "clones",
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

								fmt.Println("@", s)
								for _, resource := range resources {
									fmt.Println(" ", resource.Name)
									for _, snap := range resource.Snaps {
										for _, clone := range snap.Clones {
											fmt.Println("  ", clone.CreatedAt.In(time.UTC).Format(utils.TimestampFormat))
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
