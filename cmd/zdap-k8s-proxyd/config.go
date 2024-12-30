package main

import (
	"log"

	"github.com/caarlos0/env"
)

type (
	PodConfig struct {
		APIPort        int      `env:"ZDAP_API_PORT" envDefault:"43210"`
		CloneOwnerName string   `env:"ZDAP_CLONE_OWNER_NAME"`
		ListenPort     int      `env:"LISTEN_PORT" envDefault:"5432"`
		Resource       string   `env:"ZDAP_RESOURCE"`
		Servers        []string `env:"ZDAP_SERVERS"`
	}
)

var (
	PodCfg PodConfig
)

func init() {
	if err := env.Parse(&PodCfg); err != nil {
		log.Fatalf("ERROR: could not parse Config from env: %v\n", err)
	}
	if PodCfg.CloneOwnerName == "" {
		log.Fatal("ERROR: ZDAP_CLONE_OWNER_NAME environment variable must be set\n")
	}
	if PodCfg.Resource == "" {
		log.Fatal("ERROR: ZDAP_RESOURCE environment variable must be set to a valid resource name\n")
	}
	if len(PodCfg.Servers) == 0 {
		log.Fatal("ERROR: ZDAP_SERVERS environment variable must be set\n")
	}
}
