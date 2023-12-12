package clonepool

import (
	"fmt"
	"github.com/labstack/gommon/log"
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
}

func NewClonePool(resource internal.Resource, cloneContext *cloning.CloneContext) ClonePool {
	return ClonePool{resource: resource, cloneContext: cloneContext}
}

func (c *ClonePool) Start() {
	go func() {
		for {
			time.Sleep(time.Second)
			dss, err := c.cloneContext.Z.Open()
			if err != nil {
				continue
			}
			log.Info("Running clonepool for %s", c.resource.Name)

			allClones, err := c.readPooled(dss)
			if err != nil {
				fmt.Printf("could not read pooled clones, error %s", err)
				continue
			}

			clones := c.pruneExpired(dss, allClones)
			available := slicez.Filter(clones, func(clone zdap.PublicClone) bool {
				return clone.ExpiresAt == nil
			})
			c.claimLock.Lock()
			c.ClonesAvailable = len(available)
			c.claimLock.Unlock()

			nbrClones := len(clones)
			clonesToAdd := c.resource.ClonePool.MinClones - len(available)
			if nbrClones+clonesToAdd > c.resource.ClonePool.MaxClones {
				clonesToAdd = c.resource.ClonePool.MaxClones - nbrClones
			}
			log.Infof("min: %d", c.resource.ClonePool.MinClones)
			log.Infof("max: %d", c.resource.ClonePool.MaxClones)
			log.Infof("nbr: %d", nbrClones)
			log.Infof("adding: %d", clonesToAdd)
			for i := 0; i < clonesToAdd; i++ {
				_, err := c.addCloneToPool(dss)
				if err != nil {
					fmt.Printf("error adding clone to pool %s", err.Error())
					continue
				}
				// may be off a tiny bit of time
				c.claimLock.Lock()
				c.ClonesAvailable++
				c.claimLock.Unlock()
			}

			log.Info("Finished running clone pool")
			dss.Close()
		}

	}()
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

	log.Infof("Adding clone to %s pool", c.resource.Name)
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

func (c *ClonePool) Claim(timeout time.Duration) (zdap.PublicClone, error) {
	c.claimLock.Lock()
	defer c.claimLock.Unlock()

	dss, err := c.cloneContext.Z.Open()
	defer dss.Close()

	clones, err := c.readPooled(dss)
	if err != nil {
		return zdap.PublicClone{}, err
	}
	available := slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ExpiresAt == nil
	})
	var claim *zdap.PublicClone
	if len(available) == 0 {
		claim, err = c.addCloneToPool(dss)
	} else {
		claim = &available[0]
	}
	maxTimeout := time.Duration(c.resource.ClonePool.ClaimMaxTimeoutSeconds) * time.Second
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	expires := time.Now().Add(timeout)
	err = claim.Dataset.SetUserProperty(zfs.PropExpires, expires.Format(zfs.TimestampFormat))
	if err != nil {
		return zdap.PublicClone{}, err
	}
	c.ClonesAvailable = len(available) - 1

	claim.APIPort = c.cloneContext.ApiPort
	claim.Server = c.cloneContext.NetworkAddress
	return *claim, nil
}
