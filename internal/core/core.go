package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
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
)

type Core struct {
	docker    *client.Client
	z         *zfs.ZFS
	configDir string

	networkAddress string
	apiPort        int

	ttlCache *cache.Cache

	// resources is the generation of the resource files currently in service.
	// It is replaced wholesale and never written to in place, so a reader
	// loads the pointer once and then holds a set nothing can change under it.
	resources atomic.Pointer[resourceSet]

	// reloadMu serialises Start and Reload, which both rebuild the cron and
	// the clone pools, and guards cron itself.
	reloadMu sync.Mutex
	cron     *cron.Cron

	poolsMu    sync.RWMutex
	clonePools map[string]*clonepool.ClonePool
}

// resourceSet is one loaded generation of the resource files.
//
// It is built complete and then published, and nothing writes to it
// afterwards. Work that runs for a while -- a restore, a clone -- takes the
// one Resource it needs by value, so a job keeps working from the
// configuration it started with even after a reload has published a newer
// generation. The old set stays alive for as long as something refers to it
// and is then collected; there is nothing to free and nothing to wait for.
type resourceSet struct {
	list   []internal.Resource
	byName map[string]internal.Resource
}

func newResourceSet(list []internal.Resource) *resourceSet {
	s := &resourceSet{
		list:   list,
		byName: make(map[string]internal.Resource, len(list)),
	}
	for _, r := range list {
		s.byName[r.Name] = r
	}
	return s
}

// set is the generation in service right now. A caller that makes more than
// one lookup should keep what this returns rather than calling it again, so
// that it sees a single consistent generation.
func (c *Core) set() *resourceSet {
	return c.resources.Load()
}

func NewCore(configDir string, networkAddress string, apiPort int, docker *client.Client, z *zfs.ZFS) (*Core, error) {

	c := &Core{
		docker:         docker,
		z:              z,
		configDir:      configDir,
		networkAddress: networkAddress,
		apiPort:        apiPort,
		ttlCache:       cache.New(10*time.Second, time.Minute),
		clonePools:     map[string]*clonepool.ClonePool{},
	}

	list, err := loadResources(configDir)
	if err != nil {
		return nil, err
	}
	c.resources.Store(newResourceSet(list))
	return c, nil
}

// Start puts the loaded resources into service: cron entries for the ones with
// a schedule, and a clone pool for the ones that want one.
func (c *Core) Start() error {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	return c.applyLocked()
}

// Reload re-reads the resource files and puts the new generation into service.
//
// It never disturbs work that is already running. A restore or a clone holds
// its Resource by value for its whole life, so all a reload changes is what
// the next job will use. In exchange it waits for nothing: a base restore can
// run for hours, and holding a configuration change behind one would buy
// nothing, since that restore ought to finish on the configuration it started
// with regardless.
//
// If the files on disk do not parse, or cannot be read, the running
// configuration is kept and the error returned: a half-written file must not
// be able to empty the cron.
//
// The returned bool says whether anything actually changed. A reload of
// identical files is a no-op rather than a rebuild, so a touched file, an
// editor writing the same content back, or the periodic check behind the file
// watch does not reset every cron schedule for nothing.
func (c *Core) Reload() (bool, error) {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()

	list, err := loadResources(c.configDir)
	if err != nil {
		return false, fmt.Errorf("resources not reloaded, keeping the running configuration: %w", err)
	}

	current := c.set().list
	if reflect.DeepEqual(list, current) {
		return false, nil
	}

	if len(list) == 0 && len(current) > 0 {
		// Legitimate if every resource file really was removed, and a sign of
		// a wrong or unmounted config directory otherwise. Loud either way.
		fmt.Println("[CORE] WARNING: reloaded", c.configDir, "and found no resources, where there were", len(current))
	}

	c.resources.Store(newResourceSet(list))
	return true, c.applyLocked()
}

// applyLocked rebuilds the cron and the clone pools for the current set.
// reloadMu must be held.
func (c *Core) applyLocked() error {
	set := c.set()

	newCron := cron.New()
	ids := map[string]cron.EntryID{}
	for _, r := range set.list {
		if r.Cron == "" {
			continue
		}
		// Captured by value: this job keeps the resource it was registered
		// with, whatever a later reload does to the file.
		r := r
		id, err := newCron.AddFunc(r.Cron, func() {
			fmt.Println("[CRON] Starting cron job to create", r.Name, "base resource")
			err := bases.CreateBaseAndSnap(c.configDir, r, c.docker, c.z, func() {
				// Looked up when the job runs rather than captured here: a
				// reload may have replaced the pool in the meantime.
				if pool := c.poolFor(r.Name); pool != nil {
					pool.TriggerGC()
				}
			})
			if err != nil {
				fmt.Println("[CRON] Error: could not run cronjob to create base,", err)
			}
		})
		if err != nil {
			return fmt.Errorf("could not create cron for '%s', %w", r.Cron, err)
		}
		ids[r.Name] = id
	}

	c.syncClonePools(set)

	oldCron := c.cron
	c.cron = newCron
	newCron.Start()
	if oldCron != nil {
		// Stop hands back a context that closes once running jobs finish. We
		// do not wait on it: a job already running has its own copy of its
		// resource and is correct on its own terms.
		oldCron.Stop()
	}

	for _, r := range set.list {
		if r.Cron == "" {
			fmt.Println("[CRON] No schedule for", r.Name, "base resource")
			continue
		}
		fmt.Println("[CRON] Adding cron job", r.Name, "base resource,", r.Cron, ". Next exec at", newCron.Entry(ids[r.Name]).Next)
	}

	return nil
}

// syncClonePools brings the running pools in line with the set: start one for
// a resource that wants a pool and has none, stop one whose resource no longer
// wants it or is gone, and replace one whose resource changed.
//
// Replacing is safe because a pool holds no authoritative state -- action()
// rebuilds everything from the ZFS listing on every pass -- so a new pool
// adopts the clones the old one leaves behind. Pools whose resource is
// unchanged are left running, so a reload does not disturb resources it has
// nothing to say about.
//
// poolsMu is taken here; reloadMu is already held by the caller.
func (c *Core) syncClonePools(set *resourceSet) {
	c.poolsMu.Lock()
	defer c.poolsMu.Unlock()

	wanted := map[string]bool{}
	for _, r := range set.list {
		if r.ClonePool.MinClones == 0 {
			continue
		}
		wanted[r.Name] = true

		if old, ok := c.clonePools[r.Name]; ok {
			if reflect.DeepEqual(old.Resource(), r) {
				continue
			}
			fmt.Println("[POOL]", r.Name, "changed, replacing its clone pool")
			old.Stop()
		} else {
			fmt.Println("[POOL]", r.Name, "wants", r.ClonePool.MinClones, "clones, starting its clone pool")
		}

		pool := clonepool.NewClonePool(r, c.cloneContext(r))
		pool.Start()
		pool.TriggerGC() // initial trigger needed to collect up-to-date stats
		c.clonePools[r.Name] = pool
	}

	for name, pool := range c.clonePools {
		if wanted[name] {
			continue
		}
		fmt.Println("[POOL]", name, "no longer wants a clone pool, stopping it")
		pool.Stop()
		delete(c.clonePools, name)
	}
}

// poolFor is the clone pool for a resource, or nil if it has none.
func (c *Core) poolFor(name string) *clonepool.ClonePool {
	c.poolsMu.RLock()
	defer c.poolsMu.RUnlock()
	return c.clonePools[name]
}

// StopClonePools stops every pool and waits for each to leave, including an
// action already in flight. For shutdown.
func (c *Core) StopClonePools() {
	c.poolsMu.Lock()
	defer c.poolsMu.Unlock()
	for name, pool := range c.clonePools {
		pool.StopAndWait()
		delete(c.clonePools, name)
	}
}

func (c *Core) cloneContext(r internal.Resource) *cloning.CloneContext {
	return &cloning.CloneContext{
		Resource:       r,
		Docker:         c.docker,
		Z:              c.z,
		ConfigDir:      c.configDir,
		NetworkAddress: c.networkAddress,
		ApiPort:        c.apiPort,
	}
}

// ExecAllCronjobs runs every scheduled job now, in sequence.
//
// It works on whichever cron is in service when it starts; a reload that lands
// while it is running affects the next invocation, not this one.
func (c *Core) ExecAllCronjobs() {
	c.reloadMu.Lock()
	cr := c.cron
	c.reloadMu.Unlock()
	if cr == nil {
		return
	}

	fmt.Println("[CRON] Executing all cron jobs now")
	cr.Stop()
	for _, e := range cr.Entries() {
		fmt.Printf("[CRON] Starting job %d now, was scheduled for %v\n", e.ID, e.Next)
		e.Job.Run()
	}
	fmt.Println("[CRON] Done executing all cronjobs, starting crontab")
	cr.Start()
}

// loadResources reads every *.resource.yml under dir. It either returns a
// complete, valid set or an error: a caller that is reloading keeps what it
// already had rather than putting a partial set into service.
func loadResources(dir string) ([]internal.Resource, error) {

	var paths []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		// Propagated rather than swallowed. A config directory that has gone
		// missing, or a file that cannot be read, has to be an error: silently
		// returning the resources found so far would let a reload empty the
		// cron and stop every clone pool.
		if err != nil {
			return err
		}
		if isResourceFile(path) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("could not read resources from %s, %w", dir, err)
	}

	var resources []internal.Resource
	definedIn := map[string]string{}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		var r internal.Resource
		err = yaml.Unmarshal(b, &r)
		if err != nil {
			return nil, fmt.Errorf("could not parse %s, %w", path, err)
		}
		if r.Name == "" {
			return nil, fmt.Errorf("%s has no name", path)
		}
		// Names address pools, cron entries and datasets, so a duplicate is
		// not a merge: it would leave two clone pools driving one resource.
		if prev, taken := definedIn[r.Name]; taken {
			return nil, fmt.Errorf("resource %q is defined twice, in %s and %s", r.Name, prev, path)
		}
		definedIn[r.Name] = path

		if r.ClonePool.ClaimMaxTimeoutSeconds == 0 {
			r.ClonePool.ClaimMaxTimeoutSeconds = internal.DefaultClaimMaxTimeoutSeconds
		}
		resources = append(resources, r)
	}

	return resources, nil
}

func (c *Core) ResourcesExists(resource string) bool {
	_, exists := c.set().byName[resource]
	return exists
}

func (c *Core) GetResourcesNames() []string {
	set := c.set()
	l := make([]string, 0, len(set.list))
	for _, r := range set.list {
		l = append(l, r.Name)
	}
	return l
}

// GetResources returns a copy, so that a caller cannot write into the set that
// is in service.
func (c *Core) GetResources() []internal.Resource {
	return slices.Clone(c.set().list)
}

func (c *Core) GetCloneContainers(cloneName string) ([]container.Summary, error) {

	cons, found := c.ttlCache.Get("current_containers")
	if !found {
		containers, err := c.docker.ContainerList(context.Background(), client.ContainerListOptions{})
		if err != nil {
			return nil, err
		}
		cons = containers.Items
		c.ttlCache.Set("current_containers", cons, 2*time.Second)
	}
	containers := cons.([]container.Summary)

	var cc []container.Summary
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

func (c *Core) GetResourceClones(lst *zfs.Listing, resourceName string) (map[time.Time][]servermodel.ServerInternalClone, error) {
	clones := lst.Clones()
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
		snaped, err := zfs.ParseTimestamp(timeStrings[0])
		if err != nil {
			return nil, err
		}
		arr := rclone[snaped]
		arr = append(arr, clone)
		rclone[snaped] = arr
	}
	return rclone, nil
}

func (c *Core) GetResourceSnaps(lst *zfs.Listing, resourceName string) ([]servermodel.ServerInternalSnapshot, error) {
	snaps := lst.Snaps()
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

// getResource copies one resource out of the set in service. The copy is what
// makes a job independent of later reloads, so hold on to it for the whole of
// a piece of work rather than looking the name up again part way through.
func (c *Core) getResource(resourceName string) (internal.Resource, bool) {
	r, ok := c.set().byName[resourceName]
	return r, ok
}

func (c *Core) CreateBaseAndSnap(ctx context.Context, resourceName string, useExistingBase bool) error {
	r, ok := c.getResource(resourceName)
	if !ok {
		return fmt.Errorf("could not find resource %s", resourceName)
	}
	if useExistingBase {
		lst, err := c.z.Load(ctx)
		if err != nil {
			return err
		}
		bs := lst.Bases()
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
		fmt.Printf("snapping %s at %s\n", latestBase, zfs.FormatTimestamp(t))
		return c.z.SnapDataset(latestBase, r.Name, t)
	}
	return bases.CreateBaseAndSnap(c.configDir, r, c.docker, c.z, func() {
		if pool := c.poolFor(resourceName); pool != nil {
			pool.TriggerGC()
		}
	})
}

func (c *Core) CloneResource(lst *zfs.Listing, owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {
	return c.CloneResourceHandlePooling(lst, owner, resourceName, at, false)
}

func (c *Core) CloneResourcePooled(lst *zfs.Listing, owner string, resourceName string, at time.Time) (*zdap.PublicClone, error) {
	return c.CloneResourceHandlePooling(lst, owner, resourceName, at, true)
}

func (c *Core) CloneResourceHandlePooling(lst *zfs.Listing, owner string, resourceName string, at time.Time, pooled bool) (*zdap.PublicClone, error) {

	r, ok := c.getResource(resourceName)
	if !ok {
		return nil, fmt.Errorf("could not find resource %s", resourceName)
	}

	return c.cloneContext(r).CloneResourceHandlePooling(lst, owner, resourceName, at, pooled)
}

func (c *Core) DestroyClone(lst *zfs.Listing, cloneName string) error {
	clones := lst.Clones()
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

// ServerStatus reports what the host has and how loaded it is.
//
// It needs counts and capacity, not dataset contents, so it does not open the
// pool: one channel program lists the dataset names and one zpool call gives
// the capacity. That is two kernel calls instead of a recursive open of every
// dataset and snapshot.
func (c *Core) ServerStatus(ctx context.Context) (zdap.ServerStatus, error) {
	var s zdap.ServerStatus

	lst, err := c.z.Load(ctx)
	if err != nil {
		return s, fmt.Errorf("could not list datasets, %w", err)
	}
	s.Clones = len(lst.Clones())
	s.Snaps = len(lst.Snaps())

	space, err := c.z.Space(ctx)
	if err != nil {
		return s, fmt.Errorf("could not get pool space, %w", err)
	}

	s.Address = c.networkAddress
	s.UsedDisk = space.Used
	s.FreeDisk = space.Free
	s.TotalDisk = space.Total

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
	for _, r := range c.set().list {
		s.Resources = append(s.Resources, r.Name)
		var available int
		if pool := c.poolFor(r.Name); pool != nil {
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
	if pool := c.poolFor(resource); pool != nil {
		return pool.Claim(timeout, owner)
	}
	return servermodel.ServerInternalClone{}, fmt.Errorf("no clone pool exists for resource '%s'", resource)
}

func (c *Core) ExpirePooledClone(resource string, claimId string) error {
	if pool := c.poolFor(resource); pool != nil {
		return pool.Expire(claimId)
	}
	return nil
}
