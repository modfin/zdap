package core

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/modfin/henry/slicez"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/bases"
	"github.com/modfin/zdap/internal/clonepool"
	"github.com/modfin/zdap/internal/cloning"
	"github.com/modfin/zdap/internal/servermodel"
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

	clonePools map[string]*clonepool.ClonePool
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
	var ids []cron.EntryID
	c.clonePools = make(map[string]*clonepool.ClonePool)
	for _, r := range c.resources {
		r := r

		var clonePool *clonepool.ClonePool
		if r.ClonePool.MinClones != 0 {
			cloneContext := cloning.CloneContext{
				Resource:       &r,
				Docker:         c.docker,
				Z:              c.z,
				ConfigDir:      c.configDir,
				NetworkAddress: c.networkAddress,
				ApiPort:        c.apiPort,
			}
			clonePool = clonepool.NewClonePool(r, &cloneContext)
			clonePool.Start()
			clonePool.TriggerGC() // initial trigger needed to collect up-to-date stats
			c.clonePools[r.Name] = clonePool
		}

		if r.Cron != "" {
			id, err := c.cron.AddFunc(r.Cron, func() {
				fmt.Println("[CRON] Starting cron job to create", r.Name, "base resource")
				err := bases.CreateBaseAndSnap(c.configDir, &r, c.docker, c.z, func() {
					if clonePool != nil {
						clonePool.TriggerGC()
					}
				})
				if err != nil {
					fmt.Println("[CRON] Error: could not run cronjob to create base,", err)
				}
			})
			if err != nil {
				return fmt.Errorf("could not create cron for '%s', %w", r.Cron, err)
			}
			ids = append(ids, id)
		}

	}
	c.cron.Start()
	for i, r := range c.resources {
		next := time.Time{}
		if i < len(ids) && ids != nil {
			next = c.cron.Entry(ids[i]).Next
		}
		fmt.Println("[CRON] Adding cron job", r.Name, "base resource,", r.Cron, ". Next exec at", next)
	}

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
		if r.ClonePool.ClaimMaxTimeoutSeconds == 0 {
			r.ClonePool.ClaimMaxTimeoutSeconds = internal.DefaultClaimMaxTimeoutSeconds
		}
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

func (c *Core) GetResourceClones(dss *zfs.Dataset, resourceName string) (map[time.Time][]servermodel.ServerInternalClone, error) {
	clones, err := c.z.ListClones(dss)
	if err != nil {
		return nil, err
	}
	var rclone = map[time.Time][]servermodel.ServerInternalClone{}
	for _, clone := range clones {
		clone.Server = c.networkAddress
		clone.APIPort = c.apiPort
		if !strings.HasPrefix(clone.Name, fmt.Sprintf("zdap-%s-", resourceName)) {
			continue
		}
		timeStrings := zfs.TimeReg.FindAllString(clone.Name, -1)
		if len(timeStrings) != 2 {
			return nil, fmt.Errorf("clone name did not have 2 dates, %#v", clone)
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

func (c *Core) GetResourceSnaps(dss *zfs.Dataset, resourceName string) ([]servermodel.ServerInternalSnapshot, error) {
	snaps, err := c.z.ListSnaps(dss)
	if err != nil {
		return nil, err
	}
	snapReg, err := regexp.Compile(fmt.Sprintf("^zdap-%s-base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}@snap$", resourceName))
	if err != nil {
		return nil, err
	}
	var rsnap []servermodel.ServerInternalSnapshot
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

func (c *Core) CreateBaseAndSnap(resourceName string, useExistingBase bool) error {
	r := c.getResource(resourceName)
	if r == nil {
		return fmt.Errorf("could not find resource %s", resourceName)
	}
	if useExistingBase {
		dss, err := c.z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()
		bs, err := c.z.ListBases(dss)
		if err != nil {
			return err
		}
		resourceRx, err := regexp.Compile("^zdap-" + resourceName + "-base.*")
		if err != nil {
			return err
		}
		resourceBases := slicez.Filter(bs, func(b string) bool {
			return resourceRx.MatchString(b)
		})
		if len(resourceBases) == 0 {
			return fmt.Errorf("no bases for resource '%s' found", resourceName)
		}
		latestBase := slicez.Reverse(slicez.Sort(resourceBases))[0]
		t := time.Now()
		fmt.Printf("snapping %s at %s\n", latestBase, t.Format(zfs.TimestampFormat))
		return c.z.SnapDataset(latestBase, r.Name, t)
	}
	return bases.CreateBaseAndSnap(c.configDir, r, c.docker, c.z, func() {
		clonePool := c.clonePools[resourceName]
		if clonePool != nil {
			clonePool.TriggerGC()
		}
	})
}

func (c *Core) CloneResource(dss *zfs.Dataset, owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {
	return c.CloneResourceHandlePooling(dss, owner, resourceName, at, false)
}

func (c *Core) CloneResourcePooled(dss *zfs.Dataset, owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {
	return c.CloneResourceHandlePooling(dss, owner, resourceName, at, true)
}

func (c *Core) CloneResourceHandlePooling(dss *zfs.Dataset, owner string, resourceName string, at time.Time, pooled bool) (*zdap.PublicClone, error) {

	r := c.getResource(resourceName)
	if r == nil {
		return nil, fmt.Errorf("could not find resource %s", resourceName)
	}
	cc := cloning.CloneContext{
		Resource:       r,
		Docker:         c.docker,
		Z:              c.z,
		ConfigDir:      c.configDir,
		NetworkAddress: c.networkAddress,
		ApiPort:        c.apiPort,
	}

	return cc.CloneResourceHandlePooling(dss, owner, resourceName, at, pooled)
}

func (c *Core) DestroyClone(dss *zfs.Dataset, cloneName string) error {
	clones, err := c.z.ListClones(dss)
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

	return bases.DestroyClone(cloneName, c.docker, c.z)
}

func (c *Core) ServerStatus(dss *zfs.Dataset) (zdap.ServerStatus, error) {
	var s zdap.ServerStatus

	clones, err := c.z.ListClones(dss)
	if err != nil {
		return s, fmt.Errorf("could not list clones, %w", err)
	}
	snaps, err := c.z.ListSnaps(dss)
	if err != nil {
		return s, fmt.Errorf("could not list snaps, %w", err)
	}

	s.Clones = len(clones)
	s.Snaps = len(snaps)
	s.Address = c.networkAddress
	s.UsedDisk, err = c.z.UsedSpace(dss)
	if err != nil {
		return s, fmt.Errorf("could not get UsedSpace, %w", err)
	}
	s.FreeDisk, err = c.z.FreeSpace(dss)
	if err != nil {
		return s, fmt.Errorf("could not get FreeSpace, %w", err)
	}
	s.TotalDisk, err = c.z.TotalSpace(dss)
	if err != nil {
		return s, fmt.Errorf("could not get TotalSpace, %w", err)
	}

	mem, err := cmem.VirtualMemory()
	if err != nil {
		return s, fmt.Errorf("could not get VirtualMemory, %w", err)
	}
	load, err := cload.Avg()
	if err != nil {
		return s, fmt.Errorf("could not get load, %w", err)
	}

	s.Load1 = load.Load1
	s.Load5 = load.Load5
	s.Load15 = load.Load15

	s.FreeMem = mem.Free
	s.UsedMem = mem.Used
	s.CachedMem = mem.Cached
	s.TotalMem = mem.Total

	s.ResourceDetails = make(map[string]zdap.ServerResourceDetails)
	for _, r := range c.resources {
		s.Resources = append(s.Resources, r.Name)
		var available int
		if pool, ok := c.clonePools[r.Name]; ok {
			available = pool.ClonesAvailable
		}

		s.ResourceDetails[r.Name] = zdap.ServerResourceDetails{
			Name:                  r.Name,
			PooledClonesAvailable: available,
		}
	}

	return s, nil
}

func (c *Core) ClaimPooledClone(resource string, timeout time.Duration, owner string) (servermodel.ServerInternalClone, error) {
	if pool, exists := c.clonePools[resource]; exists {
		return pool.Claim(timeout, owner)
	}
	return servermodel.ServerInternalClone{}, fmt.Errorf("no clone pool exists for resource '%s'", resource)
}

func (c *Core) ExpirePooledClone(resource string, claimId string) error {
	if pool, exists := c.clonePools[resource]; exists {
		return pool.Expire(claimId)
	}
	return nil
}
