package clonepool

import (
	"github.com/modfin/henry/slicez"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/zfs"
)

type ClonePool struct {
	clones   []zdap.PublicClone
	resource internal.Resource
}

func NewClonePool(resource internal.Resource) ClonePool {
	return ClonePool{resource: resource}
}

func (c *ClonePool) Start(z *zfs.ZFS) {
	dss, err := z.Open()
	if err != nil {
		panic("could not open dataset")
	}
	clones, err := z.ListClones(dss)
	if err != nil {
		panic("could not list clones")
	}
	c.clones = slicez.Filter(clones, func(clone zdap.PublicClone) bool {
		return clone.ClonePooled && clone.Name == c.resource.Name
	})
}
