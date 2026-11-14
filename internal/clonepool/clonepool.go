package clonepool

import (
	"context"
	"fmt"
	"github.com/labstack/gommon/log"
	"github.com/modfin/henry/slicez"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/cloning"
	"github.com/modfin/zdap/internal/servermodel"
	"github.com/modfin/zdap/internal/zfs"
	"sync"
	"time"
)

type ClonePool struct {
	resource        internal.Resource
	cloneContext    *cloning.CloneContext
	ClonesAvailable int
	claimLock       sync.Mutex
	gc              chan struct{}
}

func NewClonePool(resource internal.Resource, cloneContext *cloning.CloneContext) *ClonePool {
	return &ClonePool{resource: resource, cloneContext: cloneContext, gc: make(chan struct{}, 1)}
}

func (c *ClonePool) Start() {
	go func() {
		for {
			select {
			case <-time.After(time.Hour):
			case <-c.gc:
			}
			c.action()
		}
	}()
}

func (c *ClonePool) TriggerGC() {
	select {
	case c.gc <- struct{}{}:
	case <-time.After(time.Millisecond):
	}
}

func (c *ClonePool) action() {

	lst, err := c.cloneContext.Z.Load(context.Background())
	if err != nil {
		fmt.Printf("error trying to open z, error: %s\n", err.Error())
		return
	}

	allClones, err := c.readPooled(lst)
	if err != nil {
		fmt.Printf("could not read pooled clones, error %s", err)
		return
	}

	if c.resource.ClonePool.ResetOnNewSnap {
		err = c.expireClonesFromOldSnaps(lst)
		if err != nil {
			fmt.Printf("could not expire old clones, error %s", err)
		}
	}

	nonExpiredClones := c.pruneExpired(lst, allClones)
	available := slicez.Filter(nonExpiredClones, func(clone servermodel.ServerInternalClone) bool {
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
		_, err := c.addCloneToPool(lst)
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

func (c *ClonePool) expireClonesFromOldSnaps(lst *zfs.Listing) error {
	c.claimLock.Lock()
	defer c.claimLock.Unlock()

	pooledClones, err := c.readPooled(lst)
	if err != nil {
		return err
	}

	latestSnap, err := c.cloneContext.GetLatestResourceSnap(lst, c.resource.Name)
	if err != nil {
		return err
	}

	slicez.ForEach(pooledClones, func(a servermodel.ServerInternalClone) {
		if a.SnappedAt != latestSnap.CreatedAt {
			err = c.expire(lst, a.Name)
			if err != nil {
				log.Errorf("Error when expiring clone %s", err)
			}
		}
	})

	return nil
}

func (c *ClonePool) getAvailableClones(lst *zfs.Listing) ([]servermodel.ServerInternalClone, error) {
	pooled, err := c.readPooled(lst)
	if err != nil {
		return nil, err
	}

	filtered := slicez.Filter(pooled, func(clone servermodel.ServerInternalClone) bool {
		return clone.ExpiresAt == nil && clone.Healthy
	})

	return filtered, nil
}

func (c *ClonePool) addCloneToPool(lst *zfs.Listing) (*zdap.PublicClone, error) {
	snap, err := c.cloneContext.GetLatestResourceSnap(lst, c.resource.Name)
	if err != nil {
		return nil, err
	}

	log.Infof("Adding clone to %s pool", c.resource.Name)
	return c.cloneContext.CloneResourcePooled(lst, "zdapd", c.resource.Name, snap.CreatedAt)
}

func (c *ClonePool) readPooled(lst *zfs.Listing) ([]servermodel.ServerInternalClone, error) {
	clones := lst.Clones()
	return slicez.Filter(clones, func(clone servermodel.ServerInternalClone) bool {
		return clone.ClonePooled && clone.Resource == c.resource.Name
	}), nil
}

func (c *ClonePool) pruneExpired(lst *zfs.Listing, clones []servermodel.ServerInternalClone) []servermodel.ServerInternalClone {
	t := time.Now()
	expired := slicez.Filter(clones, func(clone servermodel.ServerInternalClone) bool {
		return clone.ExpiresAt != nil && clone.ExpiresAt.Before(t)
	})

	for _, e := range expired {
		err := c.cloneContext.DestroyClone(lst, e.Name)
		if err != nil {
			fmt.Printf("could not destroy clone %s, error: %s", e.Name, err.Error())
		}
	}

	return slicez.Filter(clones, func(clone servermodel.ServerInternalClone) bool {
		return clone.ExpiresAt == nil || !clone.ExpiresAt.Before(t)
	})
}

func (c *ClonePool) Expire(claimId string) error {
	lst, err := c.cloneContext.Z.Load(context.Background())
	if err != nil {
		return err
	}
	return c.expire(lst, claimId)
}

func (c *ClonePool) expire(lst *zfs.Listing, claimId string) error {
	pooled, err := c.readPooled(lst)
	if err != nil {
		return err
	}

	match := slicez.Filter(pooled, func(a servermodel.ServerInternalClone) bool {
		return a.Name == claimId
	})

	if len(match) == 0 {
		return fmt.Errorf("found no matching clones")
	}

	err = c.cloneContext.Z.SetUserProperty(match[0].Name, zfs.PropExpires, zfs.FormatTimestamp(time.Now()))
	if err != nil {
		return err
	}
	c.TriggerGC()
	return err
}

func (c *ClonePool) Claim(timeout time.Duration, owner string) (servermodel.ServerInternalClone, error) {
	c.claimLock.Lock()
	defer c.claimLock.Unlock()

	lst, err := c.cloneContext.Z.Load(context.Background())
	if err != nil {
		return servermodel.ServerInternalClone{}, err
	}
	claim, err := c.getPooledClone(lst)
	if err != nil {
		fmt.Printf("Failed to get pooled clone, will attempt to add one: %s\n", err.Error())
	}

	if claim == nil {
		err = c.addPooledClone(lst)
		if err != nil {
			return servermodel.ServerInternalClone{}, err
		}

		// A listing is a point in time, so the clone just added is not in the
		// one loaded above: reload before looking for it.
		updated, err := c.cloneContext.Z.Load(context.Background())
		if err != nil {
			return servermodel.ServerInternalClone{}, err
		}

		claim, _ = c.getPooledClone(updated)
	}

	if claim == nil {
		return servermodel.ServerInternalClone{}, fmt.Errorf("could not find available clone %w", err)
	}

	maxTimeout := time.Duration(c.resource.ClonePool.ClaimMaxTimeoutSeconds) * time.Second
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	expires := time.Now().Add(timeout)
	err = c.cloneContext.Z.SetUserProperty(claim.Name, zfs.PropExpires, zfs.FormatTimestamp(expires))
	c.triggerGCAfterDelay(timeout)
	if err != nil {
		return servermodel.ServerInternalClone{}, err
	}
	c.ClonesAvailable--
	err = c.cloneContext.Z.SetUserProperty(claim.Name, zfs.PropOwner, owner)
	if err != nil {
		return servermodel.ServerInternalClone{}, err
	}

	claim.APIPort = c.cloneContext.ApiPort
	claim.Server = c.cloneContext.NetworkAddress
	c.TriggerGC()
	return *claim, nil
}

func (c *ClonePool) triggerGCAfterDelay(delay time.Duration) {
	go func() {
		time.Sleep(delay)
		c.TriggerGC()
	}()
}

func (c *ClonePool) getPooledClone(lst *zfs.Listing) (*servermodel.ServerInternalClone, error) {
	clones, err := c.getAvailableClones(lst)
	if err != nil {
		return nil, err
	}
	if len(clones) == 0 {
		return nil, fmt.Errorf("could not find any available clones")
	} else {
		return &clones[0], nil
	}
}

func (c *ClonePool) addPooledClone(lst *zfs.Listing) error {
	_, err := c.addCloneToPool(lst)
	return err
}
