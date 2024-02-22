package clonepool

import (
	"fmt"
	"github.com/modfin/henry/slicez"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/cloning"
	"github.com/modfin/zdap/internal/zfs"
	"sort"
	"sync"
	"time"
)

type ClonePool struct {
	resource        internal.Resource
	cloneContext    *cloning.CloneContext
	ClonesAvailable int
	claimLock       sync.Mutex
	expireLock      sync.Mutex
}

func NewClonePool(resource internal.Resource, cloneContext *cloning.CloneContext) ClonePool {
	return ClonePool{resource: resource, cloneContext: cloneContext}
}

func (c *ClonePool) Start() {
	go func() {
		for {
			time.Sleep(time.Second)
			c.action()
		}
	}()
}

func (c *ClonePool) action() {
	c.expireLock.Lock()
	defer c.expireLock.Unlock()

	dss, err := c.cloneContext.Z.Open()
	defer dss.Close()
	if err != nil {
		fmt.Printf("error trying to open z, error: %s\n", err.Error())
		return
	}
	//log.Info("Running clonepool for %s", c.resource.Name)

	allClones, err := c.readPooled(dss)
	if err != nil {
		fmt.Printf("could not read pooled clones, error %s", err)
		return
	}

	clones := c.pruneExpired(dss, allClones)
	available := slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ExpiresAt == nil && clone.Healthy
	})
	c.claimLock.Lock()
	c.ClonesAvailable = len(available)
	c.claimLock.Unlock()

	nbrClones := len(clones)
	clonesToAdd := c.resource.ClonePool.MinClones - len(available)
	if nbrClones+clonesToAdd > c.resource.ClonePool.MaxClones {
		clonesToAdd = c.resource.ClonePool.MaxClones - nbrClones
	}
	//log.Infof("min: %d", c.resource.ClonePool.MinClones)
	//log.Infof("max: %d", c.resource.ClonePool.MaxClones)
	//log.Infof("nbr: %d", nbrClones)
	//log.Infof("adding: %d", clonesToAdd)
	for i := 0; i < clonesToAdd; i++ {
		_, err := c.addCloneToPool(dss)
		if err != nil {
			fmt.Printf("error adding clone to pool %s\n", err.Error())
			continue
		}
		// may be off a tiny bit of time
		c.claimLock.Lock()
		c.ClonesAvailable++
		c.claimLock.Unlock()
	}

	//log.Info("Finished running clone pool")
}

func (c *ClonePool) getAvailableClones(dss *zfs.Dataset) ([]zdap.PublicClone, error) {
	pooled, err := c.readPooled(dss)
	if err != nil {
		return nil, err
	}

	filtered := slicez.Filter(pooled, func(clone zdap.PublicClone) bool {
		return clone.ExpiresAt == nil && clone.Healthy
	})

	return filtered, nil
}

func (c *ClonePool) addCloneToPool(dss *zfs.Dataset) (*zdap.PublicClone, error) {
	snaps, err := c.cloneContext.GetResourceSnaps(dss, c.resource.Name)
	if err != nil || snaps == nil {
		return nil, err
	}

	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
	})
	latestDate := snaps[0].CreatedAt

	//log.Infof("Adding clone to %s pool", c.resource.Name)
	return c.cloneContext.CloneResourcePooled(dss, "zdapd", c.resource.Name, latestDate)
}

func (c *ClonePool) readPooled(dss *zfs.Dataset) ([]zdap.PublicClone, error) {
	clones, err := c.cloneContext.Z.ListClones(dss)
	if err != nil {
		return nil, fmt.Errorf("could not list clones")
	}
	return slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ClonePooled && clone.Resource == c.resource.Name
	}), nil
}

func (c *ClonePool) pruneExpired(dss *zfs.Dataset, clones []zdap.PublicClone) []zdap.PublicClone {
	c.claimLock.Lock()
	defer c.claimLock.Unlock()

	t := time.Now()
	expired := slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ExpiresAt != nil && clone.ExpiresAt.Before(t)
	})

	for _, e := range expired {
		err := c.cloneContext.DestroyClone(dss, e.Name)
		if err != nil {
			fmt.Printf("could not destroy clone %s, error: %s", e.Name, err.Error())
		}
	}

	return slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ExpiresAt == nil || !clone.ExpiresAt.Before(t)
	})
}

func (c *ClonePool) Expire(claimId string) error {
	c.expireLock.Lock()
	defer c.expireLock.Unlock()
	dss, err := c.cloneContext.Z.Open()
	if err != nil {
		return err
	}

	pooled, err := c.readPooled(dss)
	if err != nil {
		return err
	}

	match := slicez.Filter(pooled, func(a zdap.PublicClone) bool {
		return a.Name == claimId
	})

	if len(match) == 0 {
		return fmt.Errorf("found no matching clones")
	}

	return match[0].Dataset.SetUserProperty(zfs.PropExpires, time.Now().Format(zfs.TimestampFormat))
}

func (c *ClonePool) Claim(timeout time.Duration) (zdap.PublicClone, error) {
	c.claimLock.Lock()
	defer c.claimLock.Unlock()

	dss, err := c.cloneContext.Z.Open()
	if err != nil {
		return zdap.PublicClone{}, err
	}
	claim, _ := c.getPooledClone(dss)
	if claim == nil {
		_ = c.addPooledClone(dss)
		dss.Close()

		// reset dataset and query new clones
		dss, err = c.cloneContext.Z.Open()
		if err != nil {
			return zdap.PublicClone{}, err
		}

		claim, _ = c.getPooledClone(dss)
	}

	if claim == nil {
		return zdap.PublicClone{}, fmt.Errorf("could not find available clone %w", err)
	}
	defer claim.Dataset.Close()

	maxTimeout := time.Duration(c.resource.ClonePool.ClaimMaxTimeoutSeconds) * time.Second
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	expires := time.Now().Add(timeout)
	fmt.Println(claim.Dataset)
	err = claim.Dataset.SetUserProperty(zfs.PropExpires, expires.Format(zfs.TimestampFormat))
	if err != nil {
		return zdap.PublicClone{}, err
	}
	c.ClonesAvailable--

	claim.APIPort = c.cloneContext.ApiPort
	claim.Server = c.cloneContext.NetworkAddress
	return *claim, nil
}

func (c *ClonePool) getPooledClone(dss *zfs.Dataset) (*zdap.PublicClone, error) {
	clones, err := c.getAvailableClones(dss)
	if err != nil {
		return nil, err
	}
	if len(clones) == 0 {
		return nil, fmt.Errorf("could not find any available clones")
	} else {
		return &clones[0], nil
	}
}

func (c *ClonePool) addPooledClone(dss *zfs.Dataset) error {
	_, err := c.addCloneToPool(dss)
	return err
}
