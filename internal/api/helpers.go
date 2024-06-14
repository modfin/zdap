package api

import (
	"fmt"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/core"
	"github.com/modfin/zdap/internal/servermodel"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
	"sort"
	"strings"
	"time"
)

func getStatus(dss *zfs.Dataset, app *core.Core) (zdap.ServerStatus, error) {
	return app.ServerStatus(dss)
}

func getResources(dss *zfs.Dataset, owner string, app *core.Core) ([]servermodel.ServerInternalResource, error) {
	var err error
	var resources []servermodel.ServerInternalResource
	for _, r := range app.GetResources() {
		res := servermodel.ServerInternalResource{
			PublicResource: zdap.PublicResource{
				Name:      r.Name,
				Alias:     r.Alias,
				ClonePool: r.ClonePool,
			},
		}
		res.Snaps, err = getSnaps(dss, owner, r.Name, app)
		if err != nil {
			return nil, err
		}
		resources = append(resources, res)
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Name < resources[j].Name
	})
	return resources, nil
}
func getResource(dss *zfs.Dataset, owner string, resource string, app *core.Core) (*servermodel.ServerInternalResource, error) {
	var err error
	for _, r := range app.GetResources() {
		if r.Name != resource {
			continue
		}
		res := servermodel.ServerInternalResource{
			PublicResource: zdap.PublicResource{
				Name:  r.Name,
				Alias: r.Alias,
			},
		}
		res.Snaps, err = getSnaps(dss, owner, r.Name, app)
		if err != nil {
			return nil, err
		}
		return &res, nil
	}
	return nil, fmt.Errorf("could not find resource")
}
func getSnap(dss *zfs.Dataset, owner string, createdAt time.Time, resource string, app *core.Core) (*servermodel.ServerInternalSnapshot, error) {
	ss, err := app.GetResourceSnaps(dss, resource)
	if err != nil {
		return nil, err
	}
	for _, t := range ss {
		if !t.CreatedAt.Equal(createdAt) {
			continue
		}
		t.Clones, err = getClones(dss, owner, t.CreatedAt, resource, app)
		return &t, nil
	}
	return nil, fmt.Errorf("could not find snap %s@%s", resource, createdAt.Format(utils.TimestampFormat))
}
func getSnaps(dss *zfs.Dataset, owner string, resource string, app *core.Core) ([]servermodel.ServerInternalSnapshot, error) {
	ss, err := app.GetResourceSnaps(dss, resource)
	if err != nil {
		return nil, err
	}
	var snaps []servermodel.ServerInternalSnapshot
	for _, t := range ss {
		t.Clones, err = getClones(dss, owner, t.CreatedAt, resource, app)
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, t)
	}
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
	})
	return snaps, nil
}

func getClone(dss *zfs.Dataset, owner string, clone time.Time, snap time.Time, resource string, app *core.Core) (*servermodel.ServerInternalClone, error) {
	cc, err := app.GetResourceClones(dss, resource)
	if err != nil {
		return nil, err
	}
	for _, t := range cc[snap] {
		if !t.CreatedAt.Equal(clone) {
			continue
		}
		if t.Owner != owner {
			continue
		}
		return &t, nil
	}
	return nil, fmt.Errorf("could not find clone %s@%s -> %s", resource, snap.Format(utils.TimestampFormat), clone.Format(utils.TimestampFormat))
}

func getClones(dss *zfs.Dataset, owner string, snap time.Time, resource string, app *core.Core) ([]servermodel.ServerInternalClone, error) {
	var clones []servermodel.ServerInternalClone
	cc, err := app.GetResourceClones(dss, resource)
	if err != nil {
		return nil, err
	}
	for _, c := range cc[snap] {
		if strings.ToLower(c.Owner) == strings.ToLower(owner) {
			clones = append(clones, c)
		}
	}
	sort.Slice(clones, func(i, j int) bool {
		return clones[i].CreatedAt.Before(clones[j].CreatedAt)
	})
	for i, c := range clones {
		c.Port, err = getPortClone(c.Name, app)
		if err != nil {
			return nil, err
		}
		clones[i] = c
	}
	return clones, nil
}

func getPortClone(clone string, app *core.Core) (int, error) {

	cons, err := app.GetCloneContainers(clone)
	if err != nil {
		return 0, err
	}

	for _, c := range cons {
		for _, name := range c.Names {
			if strings.HasSuffix(name, clone+"-proxy") {
				for _, port := range c.Ports {
					if port.PublicPort > 0 {
						return int(port.PublicPort), nil
					}
				}
			}
		}
	}
	return 0, fmt.Errorf("could not find proxy container for %s", clone)

}
