package zfs

import (
	"errors"
	"fmt"
	"github.com/bicomsystems/go-libzfs"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/utils"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Dataset currently just wrap the dataset structure from go-libzfs, but will probably be extended in the future.
type Dataset struct {
	*zfs.Dataset
}

func NewZFS(pool string) *ZFS {
	return &ZFS{
		pool: pool,
	}
}

type ZFS struct {
	pool string
}

const PropCreated = "zdap:created_at"
const PropOwner = "zdap:owner"
const PropResource = "zdap:resource"
const PropSnappedAt = "zdap:snapped_at"

const TimestampFormat = "2006-01-02T15.04.05"

var TimeReg = regexp.MustCompile("[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}")

var cloneReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}-clone-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}.[a-zA-Z]{3}$")
var snapReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}@snap$")
var baseReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}$")

func (z *ZFS) GetDatasetBaseNameAt(name string, at time.Time) string {
	return fmt.Sprintf("zdap-%s-base-%s", name, at.Format(TimestampFormat))
}

func (z *ZFS) NewDatasetBaseName(name string, t time.Time) string {
	return z.GetDatasetBaseNameAt(name, t)
}
func (z *ZFS) GetDatasetSnapNameAt(name string, at time.Time) string {
	return fmt.Sprintf("%s@snap", z.GetDatasetBaseNameAt(name, at))
}

func (z *ZFS) CreateDataset(name string, resource string, creation time.Time) (string, error) {
	ds, err := zfs.DatasetCreate(fmt.Sprintf("%s/%s", z.pool, name), zfs.DatasetTypeFilesystem, nil)
	if err != nil {
		return "", err
	}
	defer ds.Close()

	err = ds.SetUserProperty(PropResource, resource)
	if err != nil {
		return "", err
	}
	err = ds.SetUserProperty(PropCreated, creation.Format(TimestampFormat))
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
	defer dataset.Close()
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
	defer ds.Close()

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

func (z *ZFS) List(dss *Dataset) ([]string, error) {
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

func (z *ZFS) ListClones(dss *Dataset) ([]zdap.PublicClone, error) {

	var clones []zdap.PublicClone

	cc, err := z.listReg(dss, cloneReg)
	if err != nil {
		return nil, err
	}

	cds := map[string]*zfs.Dataset{}
	for i, cd := range dss.Children {
		p, err := cd.Path()
		if err != nil {
			continue
		}
		cds[p] = &dss.Children[i]
	}

	for _, c := range cc {
		d, ok := cds[fmt.Sprintf("%s/%s", z.pool, c)]
		if !ok {
			return nil, fmt.Errorf("child %s/%s not found", z.pool, c)
		}

		owner, err := d.GetUserProperty(PropOwner)
		if err != nil {
			return nil, err
		}
		created, err := d.GetUserProperty(PropCreated)
		if err != nil {
			return nil, err
		}

		resource, err := d.GetUserProperty(PropResource)
		if err != nil {
			return nil, err
		}
		snapped, err := d.GetUserProperty(PropSnappedAt)
		if err != nil {
			return nil, err
		}

		createdAt, _ := time.Parse(TimestampFormat, created.Value)
		snappedAt, _ := time.Parse(TimestampFormat, snapped.Value)

		clones = append(clones, zdap.PublicClone{
			Name:      c,
			Resource:  resource.Value,
			Owner:     owner.Value,
			CreatedAt: createdAt,
			SnappedAt: snappedAt,
		})
	}

	return clones, nil
}
func (z *ZFS) ListBases(dss *Dataset) ([]string, error) {
	return z.listReg(dss, baseReg)
}

func (z *ZFS) ListSnaps(dss *Dataset) ([]zdap.PublicSnap, error) {

	sn, err := z.listReg(dss, snapReg)
	if err != nil {
		return nil, err
	}
	var snaps []zdap.PublicSnap

	cds := map[string]*zfs.Dataset{}
	for _, cd := range dss.Children {
		for i, ccd := range cd.Children {
			if ccd.IsSnapshot() {
				p, err := ccd.Path()
				if err != nil {
					continue
				}
				cds[p] = &cd.Children[i]
			}
		}
	}
	for _, s := range sn {
		d, ok := cds[fmt.Sprintf("%s/%s", z.pool, s)]
		if !ok {
			return nil, fmt.Errorf("child %s/%s not found", z.pool, s)
		}

		created, err := d.GetUserProperty(PropCreated)
		if err != nil {
			return nil, err
		}
		createdAt, _ := time.Parse(TimestampFormat, created.Value)

		resource, err := d.GetUserProperty(PropResource)
		if err != nil {
			return nil, err
		}

		snaps = append(snaps, zdap.PublicSnap{
			Name:      s,
			Resource:  resource.Value,
			CreatedAt: createdAt,
		})
	}

	return snaps, nil
}

func (z *ZFS) listReg(dss *Dataset, reg *regexp.Regexp) ([]string, error) {
	ll, err := z.List(dss)
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

func (z *ZFS) SnapDataset(name string, resource string, created time.Time) error {
	ds, err := zfs.DatasetSnapshot(fmt.Sprintf("%s/%s@snap", z.pool, name), false, nil, nil)
	if err != nil {
		return err
	}
	defer ds.Close()

	err = ds.SetUserProperty(PropResource, resource)
	if err != nil {
		return err
	}
	err = ds.SetUserProperty(PropCreated, created.Format(TimestampFormat))
	if err != nil {
		return err
	}
	return err
}

func (z *ZFS) CloneDataset(owner, snapName string) (string, string, error) {

	parts := strings.Split(snapName, "@")
	if len(parts) != 2 {
		return "", "", errors.New("snap name is not propperly formated")
	}
	dsName, snapName := parts[0], parts[1]

	ds, err := zfs.DatasetOpen(fmt.Sprintf("%s/%s", z.pool, dsName))
	if err != nil {
		return "", "", err
	}
	defer ds.Close()

	ok, snap := ds.FindSnapshotName("@" + snapName)
	if !ok {
		return "", "", errors.New("could not find snapshot to clone")
	}

	created := time.Now().Format(TimestampFormat)

	cloneName := fmt.Sprintf("%s-clone-%s.%s", dsName, created, utils.RandStringRunes(3))
	clone, err := snap.Clone(fmt.Sprintf("%s/%s", z.pool, cloneName), nil)
	if err != nil {
		return "", "", err
	}
	defer clone.Close()

	err = clone.SetUserProperty(PropOwner, owner)
	if err != nil {
		return "", "", err
	}
	err = clone.SetUserProperty(PropCreated, created)
	if err != nil {
		return "", "", err
	}

	resource, err := ds.GetUserProperty(PropResource)
	if err != nil {
		return "", "", err
	}
	err = clone.SetUserProperty(PropResource, resource.Value)
	if err != nil {
		return "", "", err
	}
	snappedAt, err := ds.GetUserProperty(PropCreated)
	if err != nil {
		return "", "", err
	}
	err = clone.SetUserProperty(PropSnappedAt, snappedAt.Value)
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

func (z *ZFS) UsedSpace(dss *Dataset) (uint64, error) {
	//p, err := zfs.PoolOpen(z.pool)
	p, err := dss.Pool()
	if err != nil {
		return 0, err
	}
	defer p.Close()
	s, err := p.VDevTree()
	if err != nil {
		return 0, err
	}
	return s.Stat.Alloc, nil
}

func (z *ZFS) FreeSpace(dss *Dataset) (uint64, error) {
	//p, err := zfs.PoolOpen(z.pool)
	p, err := dss.Pool()
	if err != nil {
		return 0, err
	}
	defer p.Close()
	s, err := p.VDevTree()
	if err != nil {
		return 0, err
	}
	return s.Stat.Space - s.Stat.Alloc, nil

}

func (z *ZFS) TotalSpace(dss *Dataset) (uint64, error) {
	//p, err := zfs.PoolOpen(z.pool)
	p, err := dss.Pool()
	if err != nil {
		return 0, err
	}
	defer p.Close()
	s, err := p.VDevTree()
	if err != nil {
		return 0, err
	}
	return s.Stat.Space, nil
}

func (z *ZFS) Open() (*Dataset, error) {
	dss, err := zfs.DatasetOpen(z.pool)
	if err != nil {
		return nil, err
	}
	return &Dataset{Dataset: &dss}, nil
}
