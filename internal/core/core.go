package core

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/zfs"
	"github.com/patrickmn/go-cache"
	"github.com/robfig/cron/v3"
	cload "github.com/shirou/gopsutil/v3/load"
	cmem "github.com/shirou/gopsutil/v3/mem"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Core struct {
	docker    *client.Client
	z         *zfs.ZFS
	configDir string

	networkAddress string
	apiPort        int

	cron      *cron.Cron
	resources []internal.Resource
	ttlCache  *cache.Cache
}

func NewCore(configDir string, networkAddress string, apiPort int, docker *client.Client, z *zfs.ZFS) (*Core, error) {

	c := &Core{
		docker:         docker,
		z:              z,
		configDir:      configDir,
		networkAddress: networkAddress,
		apiPort:        apiPort,
		ttlCache:       cache.New(10*time.Second, time.Minute),
	}
	err := c.reload()
	return c, err
}
func (c *Core) Start() error {
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

func (c *Core) ExecAllCronjobs() {
	fmt.Println("[CRON] Executing all cron jobs now")
	c.cron.Stop()
	for _, e := range c.cron.Entries() {
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

func (c *Core) ResourcesExists(resource string) bool {
	for _, r := range c.GetResources() {
		if r.Name == resource {
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

func (c *Core) GetCloneContainers(cloneName string) ([]types.Container, error) {

	cons, found := c.ttlCache.Get("current_containers")
	if !found {
		containers, err := c.docker.ContainerList(context.Background(), types.ContainerListOptions{})
		if err != nil {
			return nil, err
		}
		cons = containers
		c.ttlCache.Set("current_containers", cons, 2*time.Second)
	}
	containers := cons.([]types.Container)

	var cc []types.Container
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.HasPrefix(name, "/"+cloneName) {
				cc = append(cc, c)
				break
			}
		}
	}
	return cc, nil

}

func (c *Core) GetResourceClones(resourceName string) (map[time.Time][]zdap.PublicClone, error) {
	clones, err := c.z.ListClones()
	if err != nil {
		return nil, err
	}
	var rclone = map[time.Time][]zdap.PublicClone{}
	for _, clone := range clones {
		clone.Server = c.networkAddress
		clone.APIPort = c.apiPort
		if !strings.HasPrefix(clone.Name, fmt.Sprintf("zdap-%s-", resourceName)) {
			continue
		}
		timeStrings := zfs.TimeReg.FindAllString(clone.Name, -1)
		if len(timeStrings) != 2 {
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
	if err != nil {
		return nil, err
	}
	var rsnap []zdap.PublicSnap
	for _, snap := range snaps {
		if !snapReg.MatchString(snap.Name) {
			continue
		}
		rsnap = append(rsnap, snap)
	}
	return rsnap, nil
}

func (c *Core) getResource(resourceName string) *internal.Resource {
	for _, r := range c.resources {
		if r.Name == resourceName {
			return &r
		}
	}
	return nil
}

func (c *Core) CreateBaseAndSnap(resourceName string) error {
	r := c.getResource(resourceName)
	if r == nil {
		return fmt.Errorf("could not find resource %s", resourceName)
	}
	return createBaseAndSnap(c.configDir, r, c.docker, c.z)
}

func (c *Core) CloneResource(owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {

	r := c.getResource(resourceName)
	if r == nil {
		return nil, fmt.Errorf("could not find resource %s", resourceName)
	}

	snapName := c.z.GetDatasetSnapNameAt(resourceName, at)

	clone, err := createClone(owner, snapName, r, c.docker, c.z)
	if err != nil {
		return nil, err
	}
	clone.Server = c.networkAddress
	clone.APIPort = c.apiPort

	return clone, nil
}

func (c *Core) DestroyClone(cloneName string) error {

	clones, err := c.z.ListClones()
	if err != nil {
		return err
	}
	var contain bool
	for _, c := range clones {
		if c.Name == cloneName {
			contain = true
			break
		}
	}
	if !contain {
		return fmt.Errorf("clone, %s, does not exist", cloneName)
	}

	return destroyClone(cloneName, c.docker, c.z)
}

func (c *Core) ServerStatus() (zdap.ServerStatus, error) {
	var s zdap.ServerStatus

	clones, err := c.z.ListClones()
	if err != nil {
		return s, err
	}
	snaps, err := c.z.ListSnaps()
	if err != nil {
		return s, err
	}

	s.Clones = len(clones)
	s.Snaps = len(snaps)
	s.Address = c.networkAddress
	s.UsedDisk, err = c.z.UsedSpace()
	if err != nil {
		return s, err
	}
	s.FreeDisk, err = c.z.FreeSpace()
	if err != nil {
		return s, err
	}
	s.TotalDisk, err = c.z.TotalSpace()
	if err != nil {
		return s, err
	}

	mem, err := cmem.VirtualMemory()
	if err != nil {
		return s, err
	}
	load, err := cload.Avg()
	if err != nil {
		return s, err
	}

	s.Load1 = load.Load1
	s.Load5 = load.Load5
	s.Load15 = load.Load15

	s.FreeMem = mem.Free
	s.UsedMem = mem.Used
	s.CachedMem = mem.Cached
	s.TotalMem = mem.Total

	for _, r := range c.resources {
		s.Resources = append(s.Resources, r.Name)
	}

	return s, nil
}
