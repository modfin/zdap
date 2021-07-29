package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/urfave/cli/v2"
	"io/ioutil"
	"os"
	"os/user"
	"sort"
	"github.com/modfin/zdap/internal/utils"
)

func Init(c *cli.Context) error {

	settings, err := LoadSettings()
	if err != nil{
		if os.IsNotExist(err){
			err = nil
		}
		if err != nil{
			return err
		}
	}

	compose := c.String("compose")
	override := c.String("override")

	if _, err := os.Stat(compose); os.IsNotExist(err) {
		return fmt.Errorf("docker compose file, %s, does not exist", compose)
	}
	if _, err := os.Stat(override); os.IsNotExist(err) {
		file, err := os.Create(override)
		if err != nil {
			return err
		}
		defer file.Close()
	}
	settings.Compose = compose
	settings.Override = override

	return settings.Save()
}


func SetUserCompletion(c *cli.Context) {
	if c.Args().Len() == 0{
		u, err := user.Current()
		if err != nil{
			return
		}
		h, err := os.Hostname()
		if err != nil{
			h = "local"
		}
		fmt.Println(u.Username + "@" + h)
	}
}
func SetUser(c *cli.Context) error {
	conffile, err := ensureConfig()
	data, err := ioutil.ReadFile(conffile)
	if err != nil {
		return err
	}
	var conf Config
	err = json.Unmarshal(data, &conf)

	if c.Args().Len() != 1{
		return errors.New("there must be exactly 1 argument")
	}
	conf.User = c.Args().First()

	return conf.Save()
}

func AddOrigin(c *cli.Context) error {
	conffile, err := ensureConfig()
	data, err := ioutil.ReadFile(conffile)
	if err != nil {
		return err
	}
	var conf Config
	err = json.Unmarshal(data, &conf)

	if c.Args().Len() == 0{
		return errors.New("there must be exactly 1 argument")
	}

	conf.Servers = append(conf.Servers, c.Args().Slice()...)
	var m = map[string]struct{}{}
	for _, s := range conf.Servers{
		m[s] = struct{}{}
	}
	conf.Servers = nil
	for s, _ := range m{
		conf.Servers = append(conf.Servers,s)
	}
	sort.Strings(conf.Servers)

	return conf.Save()

}
func RemoveOriginCompletion(c *cli.Context) {
	conf, err := getConfig()
	if err != nil{
		return
	}

	for _, s := range conf.Servers{
		if utils.StringSliceContains(c.Args().Slice(), s){
			continue
		}
		fmt.Println(s)
	}
}

func RemoveOrigin(c *cli.Context) error {
	conffile, err := ensureConfig()
	data, err := ioutil.ReadFile(conffile)
	if err != nil {
		return err
	}
	var conf Config
	err = json.Unmarshal(data, &conf)

	if c.Args().Len() == 0{
		return errors.New("there must be exactly 1 argument")
	}
	conf.Servers = append(conf.Servers, c.Args().Slice()...)
	var m = map[string]struct{}{}
	for _, s := range conf.Servers{
		if utils.StringSliceContains(c.Args().Slice(), s){
			continue
		}
		m[s] = struct{}{}
	}


	conf.Servers = nil
	for s, _ := range m{
		conf.Servers = append(conf.Servers,s)
	}
	sort.Strings(conf.Servers)

	return conf.Save()

}