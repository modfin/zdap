package zfs

import (
	"errors"
	"fmt"
	"github.com/bicomsystems/go-libzfs"
	"regexp"
	"sort"
	"strings"
	"time"
	"zdap/internal/utils"
)

func NewZFS(pool string) *ZFS {
	return &ZFS{
		pool: pool,
	}
}

type ZFS struct {
	pool string
}



var cloneReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}_[0-9]{2}_[0-9]{2}-clone-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}_[0-9]{2}_[0-9]{2}_[a-zA-Z]{3}$")
var snapReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}_[0-9]{2}_[0-9]{2}@snap$")
var baseReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}_[0-9]{2}_[0-9]{2}$")

func (z *ZFS) CreateDataset(name string) (string, error) {
	ds, err := zfs.DatasetCreate(fmt.Sprintf("%s/%s", z.pool, name), zfs.DatasetTypeFilesystem, nil)
	if err != nil {
		return "", err
	}

	err = ds.Mount("", 0)
	if err != nil {
		return "", err
	}
	mounted, path := ds.IsMounted()
	if !mounted {
		return "", errors.New("could not mount fs")
	}
	return path, nil
}

func (z *ZFS) destroyDatasetRec(path string) error {
	dataset, err := zfs.DatasetOpen(path)
	if err != nil {
		return nil
		//return fmt.Errorf("could not open ds: %w", err)
	}
	err = dataset.UnmountAll(0)
	if err != nil {
		return fmt.Errorf("could not unmout all: %w", err)
	}

	clones, err := dataset.Clones()
	if err != nil {
		return fmt.Errorf("could not get clones: %w", err)
	}
	for _, c := range clones {
		err = z.destroyDatasetRec(c)
		if err != nil {
			return fmt.Errorf("could not rec specific clone: %w", err)
		}
	}

	for _, c := range dataset.Children {
		if !c.IsSnapshot() {
			continue
		}
		p, err := c.Path()
		if err != nil {
			return fmt.Errorf("could not get path: %w", err)
		}
		err = z.destroyDatasetRec(p)
		if err != nil {
			return fmt.Errorf("could destroy snap: %w", err)
		}
	}
	for _, c := range dataset.Children {
		if c.IsSnapshot() {
			continue
		}
		p, err := c.Path()
		if err != nil {
			return fmt.Errorf("could not get path: %w", err)
		}
		err = z.destroyDatasetRec(p)
		if err != nil {
			return fmt.Errorf("could not recure: %w", err)
		}
	}

	dataset.Close()
	dataset, err = zfs.DatasetOpen(path)
	if err != nil {
		return fmt.Errorf("could not open ds2: %w", err)
	}
	fmt.Println(" - Destroying", path)
	err = dataset.Destroy(false)
	if err != nil {
		return fmt.Errorf("could not destroy ds: %w", err)
	}
	return nil

}

func (z *ZFS) Destroy(name string) error {
	return z.destroyDatasetRec(fmt.Sprintf("%s/%s", z.pool, name))
}

func (z *ZFS) DestroyAll() error {
	ds, err := zfs.DatasetOpen(z.pool)

	if err != nil {
		return err
	}

	ch := ds.Children
	sort.Slice(ch, func(i, j int) bool {
		path1, err := ch[i].Path()
		if err != nil {
			return false
		}
		path2, err := ch[j].Path()
		if err != nil {
			return false
		}
		return len(path1) < len(path2)
	})

	isClone := map[string]bool{}
	for _, c := range ds.Children {
		clones, err := c.Clones()
		if err != nil {
			return err
		}
		for _, c := range clones {
			isClone[c] = true
		}

		path, err := c.Path()
		if err != nil {
			return err
		}
		if isClone[path] {
			continue
		}

		fmt.Println("- Destroying", path)
		err = z.destroyDatasetRec(path)
		if err != nil {
			return err
		}
	}
	return nil
}

func (z *ZFS) List() ([]string, error) {
	dss, err := zfs.DatasetOpen(z.pool)
	if err != nil {
		return nil, err
	}

	var list []string
	for _, ds := range dss.Children {
		p, err := ds.Path()
		if err != nil {
			return nil, err
		}
		pre := fmt.Sprintf("%s/", z.pool)
		if strings.HasPrefix(p, pre) {
			list = append(list, strings.TrimPrefix(p, pre))
		}
		snaps, err := ds.Snapshots()
		if err != nil {
			return nil, err
		}
		for _, snap := range snaps {
			s, err := snap.Path()
			if err != nil {
				return nil, err
			}
			list = append(list, strings.TrimPrefix(s, pre))
		}

	}
	return list, nil
}

func (z *ZFS) ListClones() ([]string, error) {
	return z.listReg(cloneReg)
}
func (z *ZFS) ListBases() ([]string, error) {
	return z.listReg(baseReg)
}

func (z *ZFS) ListSnaps() ([]string, error) {
	return z.listReg(snapReg)
}

func (z *ZFS) listReg(reg *regexp.Regexp) ([]string, error) {
	ll, err := z.List()
	if err != nil {
		return nil, err
	}
	var list []string

	for _, item := range ll {
		if reg.MatchString(item) {
			list = append(list, item)
		}
	}
	return list, nil
}

func (z *ZFS) SnapDataset(name string) error {
	_, err := zfs.DatasetSnapshot(fmt.Sprintf("%s/%s@snap", z.pool, name), false, nil)
	return err
}

func (z *ZFS) CloneDataset(snapName string) (string, string, error) {

	parts := strings.Split(snapName, "@")
	if len(parts) != 2 {
		return "", "", errors.New("snap name is not propperly formated")
	}
	dsName, snapName := parts[0], parts[1]

	ds, err := zfs.DatasetOpen(fmt.Sprintf("%s/%s", z.pool, dsName))
	if err != nil {
		return "", "", err
	}

	ok, snap := ds.FindSnapshotName("@" + snapName)
	if !ok {
		return "", "", errors.New("could not find snapshot to clone")
	}

	cloneName := fmt.Sprintf("%s-clone-%s_%s", dsName, time.Now().Format("2006-01-02T15_04_05"), utils.RandStringRunes(3))
	clone, err := snap.Clone(fmt.Sprintf("%s/%s", z.pool, cloneName), nil)
	if err != nil {
		return "", "", err
	}

	err = clone.Mount("", 0)
	if err != nil {
		return "", "", err
	}
	mounted, path := clone.IsMounted()
	if !mounted {
		return "", "", errors.New("could not mount clone fs")
	}

	return cloneName, path, err
}
