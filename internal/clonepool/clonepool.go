package clonepool

import (
	"fmt"
	"github.com/labstack/gommon/log"
	"github.com/modfin/henry/slicez"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/cloning"
	"github.com/modfin/zdap/internal/zfs"
	"sync"
	"time"
)

type ClonePool struct {
	resource        internal.Resource
	cloneContext    *cloning.CloneContext
	ClonesAvailable int
	claimLock       sync.Mutex
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

	dss, err := c.cloneContext.Z.Open()
	defer dss.Close()
	if err != nil {
		fmt.Printf("error trying to open z, error: %s\n", err.Error())
		return
	}

	allClones, err := c.readPooled(dss)
	if err != nil {
		fmt.Printf("could not read pooled clones, error %s", err)
		return
	}

	err = c.expireClonesFromOldSnaps(dss)
	if err != nil {
		fmt.Printf("could not expire old clones, error %s", err)
	}

	nonExpiredClones := c.pruneExpired(dss, allClones)
	available := slicez.Filter(nonExpiredClones, func(clone zdap.PublicClone) bool {
		return clone.ExpiresAt == nil && clone.Healthy
	})
	c.claimLock.Lock()
	c.ClonesAvailable = len(available)
	c.claimLock.Unlock()

	nbrClones := len(nonExpiredClones)
	clonesToAdd := c.resource.ClonePool.MinClones - len(available)
	if nbrClones+clonesToAdd > c.resource.ClonePool.MaxClones {
		clonesToAdd = c.resource.ClonePool.MaxClones - nbrClones
	}

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

}

func (c *ClonePool) expireClonesFromOldSnaps(dss *zfs.Dataset) error {
	c.claimLock.Lock()
	defer c.claimLock.Unlock()

	pooledClones, err := c.readPooled(dss)
	if err != nil {
		return err
	}

	latestSnap, err := c.cloneContext.GetLatestResourceSnap(dss, c.resource.Name)
	if err != nil {
		return err
	}

	slicez.Each(pooledClones, func(a zdap.PublicClone) {
		if a.SnappedAt != latestSnap.CreatedAt {
			err = c.Expire(a.Name)
			if err != nil {
				log.Errorf("Error when expiring clone %s", err)
			}
		}
	})

	return nil
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
	snap, err := c.cloneContext.GetLatestResourceSnap(dss, c.resource.Name)
	if err != nil {
		return nil, err
	}

	log.Infof("Adding clone to %s pool", c.resource.Name)
	return c.cloneContext.CloneResourcePooled(dss, "zdapd", c.resource.Name, snap.CreatedAt)
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
	dss, err := c.cloneContext.Z.Open()
	if err != nil {
		return err
	}
	defer dss.Close()

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

	c.cloneContext.Z.WriteLock()
	err = match[0].Dataset.SetUserProperty(zfs.PropExpires, time.Now().Format(zfs.TimestampFormat))
	c.cloneContext.Z.WriteUnlock()
	return err
}

func (c *ClonePool) Claim(timeout time.Duration) (zdap.PublicClone, error) {
	c.claimLock.Lock()
	defer c.claimLock.Unlock()

	dss, err := c.cloneContext.Z.Open()
	defer dss.Close()
	if err != nil {
		return zdap.PublicClone{}, err
	}
	claim, _ := c.getPooledClone(dss)
	if claim == nil {
		_ = c.addPooledClone(dss)
		dss.Close()

		// reset dataset and query new clones
		updatedDss, err := c.cloneContext.Z.Open()
		defer updatedDss.Close()
		if err != nil {
			return zdap.PublicClone{}, err
		}

		claim, _ = c.getPooledClone(updatedDss)
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
	c.cloneContext.Z.WriteLock()
	err = claim.Dataset.SetUserProperty(zfs.PropExpires, expires.Format(zfs.TimestampFormat))
	c.cloneContext.Z.WriteUnlock()
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
