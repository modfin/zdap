package core

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"zdap"
	"zdap/internal"
	"zdap/internal/zfs"
)

type Core struct {
	docker    *client.Client
	z         *zfs.ZFS
	configDir string

	cron      *cron.Cron
	resources []internal.Resource
}

func NewCore(configDir string, docker *client.Client, z *zfs.ZFS) (*Core, error) {

	c := &Core{
		docker:    docker,
		z:         z,
		configDir: configDir,
	}
	err := c.reload()
	return c, err
}
func (c *Core) Start() error{
	for _, r := range c.resources {
		r := r
		fmt.Println("[CRON] Adding cron job", r.Name, "base resource,", r.Cron)

		_, err := c.cron.AddFunc(r.Cron, func() {
			fmt.Println("[CRON] Starting cron job to create", r.Name, "base resource")
			err := createBaseAndSnap(c.configDir, &r, c.docker, c.z)
			if err != nil {
				fmt.Println("[CRON] Error: could not run cronjob to create base,", err)
			}
		})
		if err != nil {
			return fmt.Errorf("could not create cron for '%s', %w", r.Cron, err)
		}
	}

	c.cron.Start()
	return nil
}


func (c *Core) ExecAllCronjobs(){
	fmt.Println("[CRON] Executing all cron jobs now")
	c.cron.Stop()
	for _, e := range c.cron.Entries(){
		fmt.Println(fmt.Sprintf("[CRON] Starting job %d now, was scheduled for %v ", e.ID, e.Next))
		e.Job.Run()
	}
	fmt.Println("[CRON] Done executing all cronjobs, starting crontab")
	c.cron.Start()
}


func (c *Core) reload() error {
	if c.cron != nil {
		c.cron.Stop()
		defer c.Start()
	}
	newCron := cron.New()

	newResources, err := loadResources(c.configDir)
	if err != nil {
		return err
	}

	c.cron = newCron
	c.resources = newResources

	return nil
}

func loadResources(dir string) ([]internal.Resource, error) {

	//fmt.Println("[CORE] Loading resource from", dir)
	var paths []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if strings.HasSuffix(path, ".resource.yml") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var resources []internal.Resource
	for _, path := range paths {
		//fmt.Println("[CORE] Adding resource", path)
		b, err := ioutil.ReadFile(path)
		if err != nil {
			return nil, err
		}

		var r internal.Resource
		err = yaml.Unmarshal(b, &r)
		if err != nil {
			return nil, err
		}
		//TODO ensure uniq resource names for r.Name
		resources = append(resources, r)
	}

	return resources, nil
}


func (c *Core) ResourcesExists(resource string ) bool {
	for _, r := range c.GetResources(){
		if r.Name == resource{
			return true
		}
	}
	return false
}

func (c *Core) GetResourcesNames() []string {
	var l []string
	for _, r := range c.resources {
		l = append(l, r.Name)
	}
	return l
}
func (c *Core) GetResources() []internal.Resource {
	return c.resources
}

func (c *Core) GetCloneContainers(cloneName string) ([]types.Container, error){
	containers, err := c.docker.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil{
		return nil, err
	}
	var cc []types.Container
	for _, c := range containers{
		for _, name := range c.Names{
			if strings.HasPrefix(name, "/"+ cloneName){
				cc = append(cc, c)
				break
			}
		}
	}
	return cc, nil

}

func (c *Core) GetResourceClones(resourceName string)  (map[time.Time][]zdap.PublicClone, error) {
	clones, err := c.z.ListClones()
	if err != nil {
		return nil, err
	}
	fmt.Println(clones)
	var rclone = map[time.Time][]zdap.PublicClone{}
	for _, clone := range clones {
		if !strings.HasPrefix(clone.Name, fmt.Sprintf("zdap-%s-",resourceName)){
			continue
		}
		timeStrings := zfs.TimeReg.FindAllString(clone.Name, -1)
		if len(timeStrings) != 2{
			return nil, fmt.Errorf("clone name did not have 2 dates, %s", clone)
		}
		snaped, err := time.Parse(zfs.TimestampFormat, timeStrings[0])
		if err != nil {
			return nil, err
		}
		arr := rclone[snaped]
		arr = append(arr, clone)
		rclone[snaped] = arr
	}
	return rclone, nil
}

func (c *Core) GetResourceSnaps(resourceName string) ([]zdap.PublicSnap, error) {
	snaps, err := c.z.ListSnaps()
	if err != nil {
		return nil, err
	}
	snapReg, err := regexp.Compile(fmt.Sprintf("^zdap-%s-base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}@snap$", resourceName))
	if err != nil{
		return nil, err
	}
	var rsnap []zdap.PublicSnap
	for _, snap := range snaps{
		if !snapReg.MatchString(snap.Name){
			continue
		}
		rsnap = append(rsnap, snap)
	}
	return rsnap, nil
}

func (c *Core) getResource(resourceName string) *internal.Resource {
	for _, r := range c.resources{
		if r.Name == resourceName{
			return &r
		}
	}
	return nil
}

func (c *Core) CreateBaseAndSnap(resourceName string) error {
	r := c.getResource(resourceName)
	if r == nil{
		return fmt.Errorf("could not find resource %s", resourceName)
	}
	return createBaseAndSnap(c.configDir, r, c.docker, c.z)
}

func (c *Core) CloneResource(owner string, resourceName string, at time.Time) (*zdap.Clone, error) {

	r := c.getResource(resourceName)
	if r == nil{
		return nil, fmt.Errorf("could not find resource %s", resourceName)
	}

	snapName := c.z.GetDatasetSnapNameAt(resourceName, at)

	return createClone(owner, snapName, r, c.docker, c.z)
}

func (c *Core) DestroyClone(cloneName string) error {

	clones, err := c.z.ListClones()
	if err != nil{
		return err
	}
	var contain bool
	for _, c := range clones{
		if c.Name == cloneName{
			contain = true
			break
		}
	}
	if !contain{
		return fmt.Errorf("clone, %s, does not exist", cloneName)
	}


	return destroyClone(cloneName, c.docker, c.z)
}
