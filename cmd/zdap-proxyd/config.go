package main

import (
	"log"
	"sync"

	"github.com/caarlos0/env/v11"
)

type conf struct {
	TargetAddress  string   `env:"TARGET_ADDRESS"`
	APIPort        int      `env:"ZDAP_API_PORT" envDefault:"43210"`
	CloneOwnerName string   `env:"ZDAP_CLONE_OWNER_NAME"`
	ListenPort     int      `env:"LISTEN_PORT" envDefault:"5432"`
	Resource       string   `env:"ZDAP_RESOURCE"`
	ResourceFilter string   `env:"ZDAP_RESOURCE_FILTER"`
	Servers        []string `env:"ZDAP_SERVERS"`
	ResetAtHhMm    string   `env:"ZDAP_RESET_AT_HH_MM"`
}

var (
	once sync.Once
	cfg  conf
)

func Config() *conf {
	once.Do(func() {
		cfg = conf{}
		if err := env.Parse(&cfg); err != nil {
			log.Panic("Couldn't parse Config from env: ", err)
		}
	})
	return &cfg
}
