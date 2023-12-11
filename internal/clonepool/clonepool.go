package clonepool

import (
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
	clones   []zdap.PublicClone
	resource internal.Resource
}

func NewClonePool(resource internal.Resource) ClonePool {
	return ClonePool{resource: resource}
}

func (c *ClonePool) Start(z *zfs.ZFS, ctx *cloning.CloneContext) {
	go func() {
		for {
			log.Info("Running clonepool")
			dss, err := z.Open()
			if err != nil {
				panic("could not open dataset")
			}
			clones, err := z.ListClones(dss)
			if err != nil {
				panic("could not list clones")
			}
			c.clones = slicez.Filter(clones, func(clone zdap.PublicClone) bool {
				log.Info(clone.ClonePooled)
				log.Info(clone.Name)
				return clone.ClonePooled && clone.Name == c.resource.Name
			})

			nbrClones := len(c.clones)
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
