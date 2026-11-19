package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/core"
	"github.com/modfin/zdap/internal/servermodel"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
)

var errProxyNotFound = errors.New("could not find proxy container")

func getStatus(ctx context.Context, app *core.Core) (zdap.ServerStatus, error) {
	return app.ServerStatus(ctx)
}

func getResources(lst *zfs.Listing, owner string, app *core.Core) ([]servermodel.ServerInternalResource, error) {
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
		res.Snaps, err = getSnaps(lst, owner, r.Name, app)
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
func getResource(lst *zfs.Listing, owner string, resource string, app *core.Core) (*servermodel.ServerInternalResource, error) {
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
		res.Snaps, err = getSnaps(lst, owner, r.Name, app)
		if err != nil {
			return nil, err
		}
		return &res, nil
	}
	return nil, fmt.Errorf("could not find resource")
}
func getSnap(lst *zfs.Listing, owner string, createdAt time.Time, resource string, app *core.Core) (*servermodel.ServerInternalSnapshot, error) {
	ss, err := app.GetResourceSnaps(lst, resource)
	if err != nil {
		return nil, err
	}
	for _, t := range ss {
		if !t.CreatedAt.Equal(createdAt) {
			continue
		}
		t.Clones, err = getClones(lst, owner, t.CreatedAt, resource, app)
		return &t, err
	}
	return nil, fmt.Errorf("could not find snap %s@%s", resource, createdAt.Format(utils.TimestampFormat))
}
func getSnaps(lst *zfs.Listing, owner string, resource string, app *core.Core) ([]servermodel.ServerInternalSnapshot, error) {
	ss, err := app.GetResourceSnaps(lst, resource)
	if err != nil {
		return nil, err
	}

	// Grouped once for the whole resource. This used to be a call per snapshot,
	// each of which rebuilt the map from scratch off a full walk of the pool.
	cc, err := app.GetResourceClones(lst, resource)
	if err != nil {
		return nil, err
	}

	var snaps []servermodel.ServerInternalSnapshot
	for _, t := range ss {
		t.Clones, err = clonesOf(cc[t.CreatedAt], owner, app)
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

func getClone(lst *zfs.Listing, owner string, clone time.Time, snap time.Time, resource string, app *core.Core) (*servermodel.ServerInternalClone, error) {
	cc, err := app.GetResourceClones(lst, resource)
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

func getClones(lst *zfs.Listing, owner string, snap time.Time, resource string, app *core.Core) ([]servermodel.ServerInternalClone, error) {
	cc, err := app.GetResourceClones(lst, resource)
	if err != nil {
		return nil, err
	}
	return clonesOf(cc[snap], owner, app)
}

// clonesOf narrows one snapshot's clones to those the caller owns, sorts them
// and fills in the port each one's proxy container is listening on.
func clonesOf(snapClones []servermodel.ServerInternalClone, owner string, app *core.Core) ([]servermodel.ServerInternalClone, error) {
	var clones []servermodel.ServerInternalClone
	var err error
	for _, c := range snapClones {
		if strings.EqualFold(c.Owner, owner) {
			clones = append(clones, c)
		}
	}
	sort.Slice(clones, func(i, j int) bool {
		return clones[i].CreatedAt.Before(clones[j].CreatedAt)
	})
	for i, c := range clones {
		c.Port, err = getPortClone(c.Name, app)
		if errors.Is(err, errProxyNotFound) {
			// The clone's containers have disappeared. Keep listing it (port 0) so
			// that it stays visible and can be destroyed, instead of failing the request.
			log.Printf("warning: %v", err)
			c.Broken = true
		} else if err != nil {
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
	return 0, fmt.Errorf("%w for %s", errProxyNotFound, clone)

}
