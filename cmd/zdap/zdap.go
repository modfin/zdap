package main

import (
	"fmt"
	"github.com/urfave/cli/v2"
	"os"
	"zdap/cmd/zdap/commands"
)

func main() {

	cliapp := &cli.App{
		Flags: []cli.Flag{

		},
		Commands: []*cli.Command{
			{
				Name:   "auto-complete",
				Usage:  "auto complete installation scripts",
				Hidden: true,
				Subcommands: []*cli.Command{
					{
						Name:   "bash",
						Action: func(context *cli.Context) error {
							fmt.Printf("%s\n", commands.BashCompletion)
							return nil
						},
					},
					{
						Name:   "zsh",
						Action: func(context *cli.Context) error {

							fmt.Printf("%s\n", commands.ZshCompletion)
							return nil
						},
					},
					{
						Name:   "fish",
						Action: func(context *cli.Context) error {

							fmt.Printf("%s\n", commands.ZshCompletion)
							return nil
						},
					},

				},
			},
			{
				Name:  "init",
				Usage: "initializes repo with zdap",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:        "override",
						Usage:       "the docker compose override file to be used",
						DefaultText: "./docker-compose.override.yml",
						Value:       "./docker-compose.override.yml",
					},
					&cli.StringFlag{
						Name:        "compose",
						Usage:       "the docker compose file to be used",
						DefaultText: "./docker-compose.yml",
						Value:       "./docker-compose.yml",
					},
				},
				Action: commands.Init,
			},
			{
				Name:  "attach",
				Usage: "attaches a remote clone to docker-compose.override file",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name: "new",
						DefaultText: "true",
						Usage: "if set, zdap will create a new clone of resource before attaching it",
						Value: true,
					},
					&cli.BoolFlag{
						Name: "force",
						Usage: "will attach to the override, even if there is no original service present in docker compose file",
					},
				},
				Action: commands.AttachClone,
				BashComplete: commands.AttachCloneCompletion,
			},
			{
				Name:   "detach",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "destroy",
						Usage: "destroys the clone at origin when being detached",
						DefaultText: "true",
						Value: true,
					},
					&cli.BoolFlag{
						Name:  "force",
					},
				},
				Usage:  "detaches a remote clone in docker-compose.override file",
				Action: commands.DetachClone,
				BashComplete: commands.DetachCloneCompletion,
			},

			{
				Name:  "set",
				Usage: "set things",
				Subcommands: []*cli.Command{
					{
						Name:   "user",
						Usage:  "set the user",
						Action: commands.SetUser,
						BashComplete: commands.SetUserCompletion,
					},
				},
			},
			{
				Name:  "add",
				Usage: "add things",
				Subcommands: []*cli.Command{
					{
						Name:   "origin",
						Usage:  "add one or more origin servers",
						Action: commands.AddOrigin,
					},
				},
			},
			{
				Name:  "remove",
				Usage: "remove things",
				Subcommands: []*cli.Command{
					{
						Name:   "origin",
						Usage:  "removes one or more origin servers",
						Action: commands.RemoveOrigin,
						BashComplete:  commands.RemoveOriginCompletion,
					},
				},
			},
			{
				Name:   "clone",
				Usage:  "clone a snapshot",
				Action: commands.CloneResource,
				BashComplete:  commands.CloneResourceCompletion,
			},
			{
				Name:   "destroy",
				Usage:  "destroys a clone",
				Action: commands.DestroyClone,
				BashComplete: commands.DestroyCloneCompletion,
			},
			{
				Name:  "list",
				Usage: "List things",
				Subcommands: []*cli.Command{
					{
						Name: "origins",
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "verbose"},
						},
						Action: commands.ListOrigins,
					},
					{
						Name: "resources",
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "all"},
							&cli.BoolFlag{Name: "attached"},
						},
						Action: commands.ListResources,
					},
					{
						Name: "snaps",
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "all"},
						},
						Action: commands.ListSnaps,
						BashComplete: commands.ResourceListCompletion,
					},
					{
						Name: "clones",
						Flags: []cli.Flag{
							&cli.BoolFlag{Name: "all"},
							&cli.BoolFlag{Name: "attached"},
							&cli.StringFlag{Name: "format"},
						},
						Action: commands.ListClones,
						BashComplete: commands.ResourceListCompletion,
					},
				},
			},

		},
	}
	cliapp.EnableBashCompletion = true
	err := cliapp.Run(os.Args)
	if err != nil {
		fmt.Println("[Error]", err)
		os.Exit(1)
	}
}
