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
	"time"
)

type ClonePool struct {
	resource internal.Resource
}

func NewClonePool(resource internal.Resource) ClonePool {
	return ClonePool{resource: resource}
}

func (c *ClonePool) Start(z *zfs.ZFS, ctx *cloning.CloneContext) {
	go func() {
		for {
			log.Info("Running clonepool for %s", c.resource.Name)
			dss, err := z.Open()
			if err != nil {
				panic("could not open dataset")
			}

			allClones, err := c.readPooled(z, dss)
			if err != nil {
				fmt.Printf("could not read pooled clones, error %s", err)
				continue
			}
			clones := pruneExpired(ctx, dss, allClones)

			nbrClones := len(clones)
			missingClones := c.resource.ClonePool.MinClones - nbrClones
			log.Infof("min: %d", c.resource.ClonePool.MinClones)
			log.Infof("nbr: %d", nbrClones)
			log.Infof("missing: %d", missingClones)
			for i := 0; i < missingClones; i++ {
				log.Info("Adding clones")
				c.addCloneToPool(ctx, dss, c.resource)
			}

			log.Info("Finished running clone pool")

			time.Sleep(time.Second)
		}

	}()
}

func (c *ClonePool) addCloneToPool(cc *cloning.CloneContext, dataset *zfs.Dataset, resource internal.Resource) {
	snaps, err := cc.GetResourceSnaps(dataset, resource.Name)
	if err != nil {
		panic(err)
	}

	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
	})
	latestDate := snaps[0].CreatedAt

	_, err = cc.CloneResourcePooled(dataset, "zdapd", resource.Name, latestDate)
	if err != nil {
		panic(err)
	}
}

func (c *ClonePool) readPooled(z *zfs.ZFS, dss *zfs.Dataset) ([]zdap.PublicClone, error) {
	clones, err := z.ListClones(dss)
	if err != nil {
		return nil, fmt.Errorf("could not list clones")
	}
	return slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ClonePooled && clone.Resource == c.resource.Name
	}), nil
}

func pruneExpired(cc *cloning.CloneContext, dss *zfs.Dataset, clones []zdap.PublicClone) []zdap.PublicClone {
	t := time.Now()
	expired := slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ExpiresAt != nil && clone.ExpiresAt.Before(t)
	})

	for _, e := range expired {
		err := cc.DestroyClone(dss, e.Name)
		fmt.Printf("could not destroy clone %s, error: %s", e.Name, err.Error())
	}

	return slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ExpiresAt == nil || !clone.ExpiresAt.Before(t)
	})
}
