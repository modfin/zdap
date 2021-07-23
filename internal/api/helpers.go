package api

import (
	"fmt"
	"sort"
	"time"
	"zdap"
	"zdap/internal/core"
	"zdap/internal/utils"
)

func getResources(app *core.Core) ([]zdap.PublicResource, error) {
	var err error
	var resources []zdap.PublicResource
	for _, r := range app.GetResources() {
		res := zdap.PublicResource{
			Name: r,
		}
		res.Snaps, err = getSnaps(r, app)
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
func getResource(resource string, app *core.Core) (*zdap.PublicResource, error) {
	var err error
	for _, r := range app.GetResources() {
		if r != resource {
			continue
		}
		res := zdap.PublicResource{
			Name: r,
		}
		res.Snaps, err = getSnaps(r, app)
		if err != nil {
			return nil, err
		}
		return &res, nil
	}
	return nil, fmt.Errorf("could not find resource")
}
func getSnap(createdAt time.Time, resource string, app *core.Core) (*zdap.PublicSnap, error) {
	ss, err := app.GetResourceSnaps(resource)
	if err != nil {
		return nil, err
	}
	for _, t := range ss {
		if !t.Equal(createdAt) {
			continue
		}
		res := zdap.PublicSnap{
			CreatedAt: t,
		}
		res.Clones, err = getClones(res.CreatedAt, resource, app)
		return &res, nil
	}
	return nil, fmt.Errorf("could not find snap %s@%s", resource, createdAt.Format(utils.TimestampFormat))
}
func getSnaps(resource string, app *core.Core) ([]zdap.PublicSnap, error) {
	var snaps []zdap.PublicSnap
	ss, err := app.GetResourceSnaps(resource)
	if err != nil {
		return nil, err
	}
	for _, t := range ss {
		res := zdap.PublicSnap{
			CreatedAt: t,
		}
		res.Clones, err = getClones(res.CreatedAt, resource, app)
		snaps = append(snaps, res)
	}
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
	})
	return snaps, nil
}

func getClone(clone time.Time, snap time.Time, resource string, app *core.Core) (*zdap.PublicClone, error) {
	cc, err := app.GetResourceClones(resource)
	if err != nil {
		return nil, err
	}
	for _, t := range cc[snap] {
		if !t.Equal(clone) {
			continue
		}
		res := zdap.PublicClone{
			CreatedAt: t,
		}
		return &res, nil
	}
	return nil, fmt.Errorf("could not find clone %s@%s -> %s", resource, snap.Format(utils.TimestampFormat), clone.Format(utils.TimestampFormat))
}

func getClones(snap time.Time, resource string, app *core.Core) ([]zdap.PublicClone, error) {
	var clones []zdap.PublicClone
	cc, err := app.GetResourceClones(resource)
	if err != nil {
		return nil, err
	}
	for _, t := range cc[snap] {
		res := zdap.PublicClone{
			CreatedAt: t,
		}
		clones = append(clones, res)
	}
	sort.Slice(clones, func(i, j int) bool {
		return clones[i].CreatedAt.Before(clones[j].CreatedAt)
	})
	return clones, nil
}
