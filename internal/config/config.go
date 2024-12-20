package config

import (
	"log"
	"sync"

	"github.com/caarlos0/env"
	"github.com/urfave/cli/v2"
)

type Config struct {
	NetworkAddress string `env:"NETWORK_ADDRESS"`
	ZPool          string `env:"ZPOOL"`
	ConfigDir      string `env:"CONFIG_DIR"`

	APIPort int `env:"API_PORT" envDefault:"43210"`
}

var (
	once    sync.Once
	onceCli sync.Once
	cfg     Config
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
func FromCli(c *cli.Context) *Config {
	Get()
	onceCli.Do(func() {
		if c.IsSet("zpool") {
			cfg.ZPool = c.String("zpool")
		}
		if c.IsSet("config-dir") {
			cfg.ConfigDir = c.String("config-dir")
		}
		if c.IsSet("network-address") {
			cfg.NetworkAddress = c.String("network-address")
		}
		if c.IsSet("api-port") {
			cfg.APIPort = c.Int("api-port")
		}
	})
	return &cfg
}
