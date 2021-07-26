package api

import (
	"fmt"
	"sort"
	"strings"
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
			Name: r.Name,
			Alias: r.Alias,
		}
		res.Snaps, err = getSnaps(r.Name, app)
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
		if r.Name != resource {
			continue
		}
		res := zdap.PublicResource{
			Name: r.Name,
			Alias: r.Alias,
		}
		res.Snaps, err = getSnaps(r.Name, app)
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
		if !t.CreatedAt.Equal(createdAt) {
			continue
		}
		t.Clones, err = getClones(t.CreatedAt, resource, app)
		return &t, nil
	}
	return nil, fmt.Errorf("could not find snap %s@%s", resource, createdAt.Format(utils.TimestampFormat))
}
func getSnaps(resource string, app *core.Core) ([]zdap.PublicSnap, error) {
	ss, err := app.GetResourceSnaps(resource)
	if err != nil {
		return nil, err
	}
	var snaps []zdap.PublicSnap
	for _, t := range ss {
		t.Clones, err = getClones(t.CreatedAt, resource, app)
		if err != nil{
			return nil, err
		}
		snaps = append(snaps, t)
	}
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
	})
	return snaps, nil
}

func getClone(clone time.Time, snap time.Time, resource string, app *core.Core) (*zdap.PublicClone, error) {
	cc, err := app.GetResourceClones(resource)
	fmt.Println("CLONES", cc)
	if err != nil {
		return nil, err
	}
	for _, t := range cc[snap] {
		if !t.CreatedAt.Equal(clone) {
			continue
		}
		return &t, nil
	}
	return nil, fmt.Errorf("could not find clone %s@%s -> %s", resource, snap.Format(utils.TimestampFormat), clone.Format(utils.TimestampFormat))
}

func getClones(snap time.Time, resource string, app *core.Core) ([]zdap.PublicClone, error) {
	var clones []zdap.PublicClone
	cc, err := app.GetResourceClones(resource)
	if err != nil {
		return nil, err
	}
	fmt.Println("CLONES", cc)
	clones =  cc[snap]
	sort.Slice(clones, func(i, j int) bool {
		return clones[i].CreatedAt.Before(clones[j].CreatedAt)
	})
	for i, c := range clones{
		c.Port, err = getPortClone(c.Name, app)
		if err != nil{
			return nil, err
		}
		clones[i] = c
	}
	return clones, nil
}

func getPortClone(clone string, app *core.Core) (int, error){

	cons, err := app.GetCloneContainers(clone)
	if err != nil{
		return 0, err
	}

	for _, c := range cons{
		for _, name := range c.Names{
			if strings.HasSuffix(name, "-proxy"){
				for _, port := range c.Ports{
					if port.PublicPort > 0{
						return int(port.PublicPort), nil
					}
				}
			}
		}
	}
	return 0, fmt.Errorf("could not find proxy container for %s", clone)

}