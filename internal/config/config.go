package config

import (
	"github.com/caarlos0/env"
	"log"
	"sync"
)


type Config struct {
	ZFSPool string `env:"ZFS_POOL"`
	ConfigDir string`env:"CONFIG_DIR"`
}

var (
	once sync.Once
	cfg  Config
)

func Get() *Config {
	once.Do(func() {
		cfg = Config{}
		if err := env.Parse(&cfg); err != nil {
			log.Panic("Couldn't parse Config from env: ", err)
		}
	})
	return &cfg
}
