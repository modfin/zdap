package commands

import (
	"encoding/json"
	"errors"
	"github.com/modfin/zdap/internal/compose"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"os"
	"path/filepath"
)

func ensureConfig() (string, error) {
	conffile := os.Getenv("ZDAP_CONF")
	if conffile == "" {
		dirname, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		err = os.MkdirAll(filepath.Join(dirname, ".zdap"), 755)
		if err != nil {
			return "", err
		}
		conffile = filepath.Join(dirname, ".zdap", "zdap-config")
	}

	if _, err := os.Stat(conffile); os.IsNotExist(err) {
		file, err := os.Create(conffile)
		if err != nil {
			return "", err
		}
		defer file.Close()
	}
	return conffile, nil
}

func getConfig() (*Config, error) {
	conffile, err := ensureConfig()

	data, err := ioutil.ReadFile(conffile)
	if err != nil {
		return nil, err
	}
	var conf Config
	err = json.Unmarshal(data, &conf)

	if conf.User == "" {
		return nil, errors.New("you must set the user, eg. zdap set user <name>")
	}
	if len(conf.Servers) == 0 {
		return nil, errors.New("you must add atleast 1 origin, eg. zdap add origin <ip:port>")
	}

	return &conf, err
}

type Config struct {
	User    string   `json:"user"`
	Servers []string `json:"servers"`
}

func (c *Config) Save() error {
	d, err := json.Marshal(c)
	if err != nil {
		return err
	}
	path, err := ensureConfig()
	if err != nil {
		return err
	}
	return ioutil.WriteFile(path, d, 0644)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type Settings struct {
	path     string `json:"-"`
	Compose  string `json:"compose"`
	Override string `json:"override"`
}

func (s *Settings) Save() error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(s.path, data, 0644)
}

func LoadSettings() (*Settings, error) {
	s := Settings{
		path: "./.zdap",
	}

	if _, err := os.Stat(s.path); err != nil {
		return &s, err
	}
	data, err := ioutil.ReadFile(s.path)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(data, &s)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func GetLocalResources() ([]string, error) {
	s, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	data, err := ioutil.ReadFile(s.Compose)
	if err != nil {
		return nil, err
	}
	var docker compose.DockerCompose
	err = yaml.Unmarshal(data, &docker)
	if err != nil {
		return nil, err
	}
	var services []string
	for k, _ := range docker.Services {
		services = append(services, k)
	}

	return services, nil
}
