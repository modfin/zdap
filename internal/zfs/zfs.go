package zfs

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	zfs "github.com/kraudcloud/go-libzfs/v2"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/servermodel"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs/zfsstat"
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
	pool     string
	poolLock sync.RWMutex
}

const PropCreated = "zdap:created_at"
const PropOwner = "zdap:owner"
const PropResource = "zdap:resource"
const PropSnappedAt = "zdap:snapped_at"
const PropClonePooled = "zdap:clone_pooled"
const PropPort = "zdap:port"
const PropExpires = "zdap:expires_at"
const PropHealthy = "zdap:healthy"

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

func zfsPropMap(zfsProps map[string]string) map[zfs.Prop]zfs.Property {
	if len(zfsProps) == 0 {
		return nil
	}
	pmap := map[string]zfs.Prop{
		"redundant_metadata": zfs.DatasetPropRedundantMetadata,
		"sync":               zfs.DatasetPropSync,
		"checksum":           zfs.DatasetPropChecksum,
		"dedup":              zfs.DatasetPropDedup,
		"compression":        zfs.DatasetPropCompression,
		"snapdir":            zfs.DatasetPropSnapdir,
		"snapdev":            zfs.DatasetPropSnapdev,
		"copies":             zfs.DatasetPropCopies,
		"primarycache":       zfs.DatasetPropPrimarycache,
		"secondarycache":     zfs.DatasetPropSecondarycache,
		"logbias":            zfs.DatasetPropLogbias,
		"xattr":              zfs.DatasetPropXattr,
		"dnodesize":          zfs.DatasetPropDnodeSize,
		"atime":              zfs.DatasetPropAtime,
		"relatime":           zfs.DatasetPropRelatime,
		"devices":            zfs.DatasetPropDevices,
		"exec":               zfs.DatasetPropExec,
		"setuid":             zfs.DatasetPropSetuid,
		"readonly":           zfs.DatasetPropReadonly,
		"vscan":              zfs.DatasetPropVscan,
		"nbmand":             zfs.DatasetPropNbmand,
		"overlay":            zfs.DatasetPropOverlay,
		"version":            zfs.DatasetPropVersion,
		"quota":              zfs.DatasetPropQuota,
		"reservation":        zfs.DatasetPropReservation,
		"refquota":           zfs.DatasetPropRefquota,
		"refreservation":     zfs.DatasetPropRefreservation,
		"filesystem_limit":   zfs.DatasetPropFilesystemLimit,
		"snapshot_limit":     zfs.DatasetPropSnapshotLimit,
		"recordsize":         zfs.DatasetPropRecordsize,
		//"special_small_blocks": zfs.DatasetPropSpecialSmallBlocks,
	}
	dsProps := make(map[zfs.Prop]zfs.Property, len(zfsProps))
	for key, val := range zfsProps {
		p, ok := pmap[key]
		if !ok {
			fmt.Printf("ignoring ZFS property: '%s' = '%s'\n", key, val)
			continue
		}
		dsProps[p] = zfs.Property{Value: val}
	}
	return dsProps
}

func (z *ZFS) CreateDataset(name string, resource string, creation time.Time, zfsProps map[string]string) (string, error) {
	z.writeLock()
	defer z.writeUnlock()

	zfsstat.RecordWrite(zfsstat.OpDatasetCreate)
	ds, err := zfs.DatasetCreate(fmt.Sprintf("%s/%s", z.pool, name), zfs.DatasetTypeFilesystem, zfsPropMap(zfsProps))
	if err != nil {
		return "", err
	}
	defer ds.Close()

	zfsstat.RecordWrite(zfsstat.OpSetUserProperty)
	err = ds.SetUserProperty(PropResource, resource)
	if err != nil {
		return "", err
	}
	zfsstat.RecordWrite(zfsstat.OpSetUserProperty)
	err = ds.SetUserProperty(PropCreated, creation.Format(TimestampFormat))
	if err != nil {
		return "", err
	}

	zfsstat.RecordWrite(zfsstat.OpMount)
	err = ds.Mount("", 0)
	if err != nil {
		return "", err
	}
	zfsstat.Inc(zfsstat.OpIsMounted)
	mounted, path := ds.IsMounted()
	if !mounted {
		return "", errors.New("could not mount fs")
	}
	return path, nil
}

func (z *ZFS) destroyDatasetRec(path string) error {
	z.readLock()
	dataset, err := zfs.DatasetOpen(path)
	if err == nil {
		zfsstat.RecordRecursiveOpen(countHandles(&dataset))
	}
	z.readUnlock()
	if err != nil {
		return err
		//return fmt.Errorf("could not open ds: %w", err)
	}
	defer dataset.Close()
	z.writeLock()
	zfsstat.RecordWrite(zfsstat.OpUnmountAll)
	err = dataset.UnmountAll(0)
	z.writeUnlock()

	if err != nil {
		return fmt.Errorf("could not unmout all: %w", err)
	}

	z.readLock()
	// Dataset.Clones re-opens the entire pool recursively inside go-libzfs,
	// so this costs another full recursive open that is not visible here.
	zfsstat.Inc(zfsstat.OpDatasetClones)
	clones, err := dataset.Clones()
	z.readUnlock()
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
	z.readLock()
	dataset, err = zfs.DatasetOpen(path)
	if err == nil {
		zfsstat.RecordRecursiveOpen(countHandles(&dataset))
	}
	z.readUnlock()
	if err != nil {
		return fmt.Errorf("could not open ds2: %w", err)
	}
	fmt.Println(" - Destroying", path)
	z.writeLock()
	zfsstat.RecordWrite(zfsstat.OpDatasetDestroy)
	err = dataset.Destroy(false)
	z.writeUnlock()
	if err != nil {
		return fmt.Errorf("could not destroy ds: %w", err)
	}
	return nil

}

func (z *ZFS) Destroy(name string) error {
	return z.destroyDatasetRec(fmt.Sprintf("%s/%s", z.pool, name))
}

func (z *ZFS) DestroyAll() error {
	z.readLock()
	ds, err := zfs.DatasetOpen(z.pool)
	if err == nil {
		zfsstat.RecordRecursiveOpen(countHandles(&ds))
	}
	z.readUnlock()

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
		z.readLock()
		zfsstat.Inc(zfsstat.OpDatasetClones)
		clones, err := c.Clones()
		z.readUnlock()
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
		zfsstat.Inc(zfsstat.OpDatasetPath)
		p, err := ds.Path()
		if err != nil {
			return nil, err
		}
		pre := fmt.Sprintf("%s/", z.pool)
		if strings.HasPrefix(p, pre) {
			list = append(list, strings.TrimPrefix(p, pre))
		}
		zfsstat.Inc(zfsstat.OpDatasetSnapshots)
		snaps, err := ds.Snapshots()
		if err != nil {
			return nil, err
		}
		for _, snap := range snaps {
			zfsstat.Inc(zfsstat.OpDatasetPath)
			s, err := snap.Path()
			if err != nil {
				return nil, err
			}
			list = append(list, strings.TrimPrefix(s, pre))
		}

	}
	return list, nil
}

func (z *ZFS) ListClones(dss *Dataset) ([]servermodel.ServerInternalClone, error) {

	var clones []servermodel.ServerInternalClone

	cc, err := z.listReg(dss, cloneReg)
	if err != nil {
		return nil, err
	}

	cds := map[string]*zfs.Dataset{}
	for i, cd := range dss.Children {
		zfsstat.Inc(zfsstat.OpDatasetPath)
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

		zfsstat.Add(zfsstat.OpGetUserProperty, 8) // the 8 zdap:* properties read below

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
		clonePooled, err := d.GetUserProperty(PropClonePooled)
		if err != nil {
			return nil, err
		}
		healthy, err := d.GetUserProperty(PropHealthy)
		if err != nil {
			return nil, err
		}

		// TODO for backwards compatibility, should be removed
		port := 0
		portString, err := d.GetUserProperty(PropPort)
		if err == nil {
			port, _ = strconv.Atoi(portString.Value)
		}

		expires, err := d.GetUserProperty(PropExpires)
		if err != nil {
			return nil, err
		}

		createdAt, _ := time.Parse(TimestampFormat, created.Value)
		snappedAt, _ := time.Parse(TimestampFormat, snapped.Value)
		expAt, err := time.Parse(TimestampFormat, expires.Value)
		var expiresAt *time.Time
		if err == nil {
			expiresAt = &expAt
		}

		clones = append(clones, servermodel.ServerInternalClone{
			PublicClone: zdap.PublicClone{
				Name:        c,
				Resource:    resource.Value,
				Owner:       owner.Value,
				CreatedAt:   createdAt,
				SnappedAt:   snappedAt,
				ClonePooled: clonePooled.Value == "true",
				Healthy:     healthy.Value == "true",
				ExpiresAt:   expiresAt,
				Port:        port},
			Dataset: d,
		})
	}

	return clones, nil
}
func (z *ZFS) ListBases(dss *Dataset) ([]string, error) {
	return z.listReg(dss, baseReg)
}

func (z *ZFS) ListSnaps(dss *Dataset) ([]servermodel.ServerInternalSnapshot, error) {

	sn, err := z.listReg(dss, snapReg)
	if err != nil {
		return nil, err
	}
	var snaps []servermodel.ServerInternalSnapshot

	cds := map[string]*zfs.Dataset{}
	for _, cd := range dss.Children {
		for i, ccd := range cd.Children {
			if ccd.IsSnapshot() {
				zfsstat.Inc(zfsstat.OpDatasetPath)
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

		zfsstat.Add(zfsstat.OpGetUserProperty, 2) // zdap:created_at and zdap:resource

		created, err := d.GetUserProperty(PropCreated)
		if err != nil {
			return nil, err
		}
		createdAt, _ := time.Parse(TimestampFormat, created.Value)

		resource, err := d.GetUserProperty(PropResource)
		if err != nil {
			return nil, err
		}

		snaps = append(snaps, servermodel.ServerInternalSnapshot{
			PublicSnap: zdap.PublicSnap{
				Name:      s,
				Resource:  resource.Value,
				CreatedAt: createdAt},
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
	z.writeLock()
	defer z.writeUnlock()

	zfsstat.RecordWrite(zfsstat.OpDatasetSnapshot)
	ds, err := zfs.DatasetSnapshot(fmt.Sprintf("%s/%s@snap", z.pool, name), false, nil)
	if err != nil {
		return err
	}
	defer ds.Close()

	zfsstat.RecordWrite(zfsstat.OpSetUserProperty)
	err = ds.SetUserProperty(PropResource, resource)
	if err != nil {
		return err
	}
	zfsstat.RecordWrite(zfsstat.OpSetUserProperty)
	err = ds.SetUserProperty(PropCreated, created.Format(TimestampFormat))
	if err != nil {
		return err
	}
	return err
}

func (z *ZFS) CloneDataset(owner, snapName string, port int, clonePooled bool, zfsProps map[string]string) (string, string, error) {
	z.writeLock()
	defer z.writeUnlock()

	parts := strings.Split(snapName, "@")
	if len(parts) != 2 {
		return "", "", errors.New("snap name is not properly formated")
	}
	dsName, snapName := parts[0], parts[1]

	ds, err := zfs.DatasetOpen(fmt.Sprintf("%s/%s", z.pool, dsName))
	if err != nil {
		return "", "", err
	}
	zfsstat.RecordRecursiveOpen(countHandles(&ds))
	defer ds.Close()

	zfsstat.Inc(zfsstat.OpFindSnapshotName)
	ok, snap := ds.FindSnapshotName("@" + snapName)
	if !ok {
		return "", "", errors.New("could not find snapshot to clone")
	}

	created := time.Now().Format(TimestampFormat)

	cloneName := fmt.Sprintf("%s-clone-%s.%s", dsName, created, utils.RandStringRunes(3))

	zfsstat.RecordWrite(zfsstat.OpDatasetClone)
	clone, err := snap.Clone(fmt.Sprintf("%s/%s", z.pool, cloneName), zfsPropMap(zfsProps))
	if err != nil {
		return "", "", err
	}
	defer clone.Close()

	// 6 zdap:* properties are written on the new clone and 2 are read off the
	// origin dataset below.
	zfsstat.Add(zfsstat.OpSetUserProperty, 6)
	zfsstat.Add(zfsstat.OpGetUserProperty, 2)
	zfsstat.Add(zfsstat.VolKernelIoctls, 6*zfsstat.IoctlsPerWrite)

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
	err = clone.SetUserProperty(PropClonePooled, strconv.FormatBool(clonePooled))
	if err != nil {
		return "", "", err
	}
	err = clone.SetUserProperty(PropPort, strconv.Itoa(port))
	if err != nil {
		return "", "", err
	}

	zfsstat.RecordWrite(zfsstat.OpMount)
	err = clone.Mount("", 0)
	if err != nil {
		return "", "", err
	}
	zfsstat.Inc(zfsstat.OpIsMounted)
	mounted, path := clone.IsMounted()
	if !mounted {
		return "", "", errors.New("could not mount clone fs")
	}

	return cloneName, path, err
}

func (z *ZFS) SetProperties(name string, props map[string]string, forgiving bool) error {
	ds, err := zfs.DatasetOpenSingle(fmt.Sprintf("%s/%s", z.pool, name))
	if err != nil {
		return err
	}
	zfsstat.RecordSingleOpen()
	return z.SetDatasetProperties(ds, props, forgiving)
}

func (z *ZFS) SetDatasetProperties(dataset zfs.Dataset, props map[string]string, forgiving bool) error {
	if len(props) == 0 {
		return nil
	}

	z.writeLock()
	defer z.writeUnlock()

	var errs []error
	for prop, val := range zfsPropMap(props) {
		zfsstat.RecordWrite(zfsstat.OpSetProperty)
		err := dataset.SetProperty(prop, val.Value)
		if err != nil {
			if forgiving {
				fmt.Printf("failed to set property '%d' to '%s', error: %v\n", prop, val.Value, err)
				continue
			}
			errs = append(errs, fmt.Errorf("failed to set property '%d' to '%s': %w", prop, val.Value, err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

func (z *ZFS) SetUserProperty(dataset zfs.Dataset, prop string, value string) error {
	z.writeLock()
	defer z.writeUnlock()

	zfsstat.RecordWrite(zfsstat.OpSetUserProperty)
	return dataset.SetUserProperty(prop, value)
}

func (z *ZFS) Open() (*Dataset, error) {
	z.readLock()
	defer z.readUnlock()
	dss, err := zfs.DatasetOpen(z.pool)
	if err != nil {
		return nil, err
	}
	// DatasetOpen walks the whole pool: every filesystem and every snapshot
	// below z.pool gets a handle with its full property table loaded, whether
	// or not the caller needs it. Count what it materialised.
	zfsstat.RecordRecursiveOpen(countHandles(&dss))
	return &Dataset{Dataset: &dss}, nil
}

// countHandles returns the number of dataset handles in an opened tree. The
// walk is in memory only; the handles were already materialised by DatasetOpen.
func countHandles(d *zfs.Dataset) int {
	n := 1
	for i := range d.Children {
		n += countHandles(&d.Children[i])
	}
	return n
}

func (z *ZFS) readLock() {
	z.poolLock.RLock()
}

func (z *ZFS) readUnlock() {
	z.poolLock.RUnlock()
}

func (z *ZFS) writeLock() {
	z.poolLock.Lock()
}

func (z *ZFS) writeUnlock() {
	z.poolLock.Unlock()
}
