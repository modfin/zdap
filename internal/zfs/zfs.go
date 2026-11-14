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
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

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

// TimestampFormat is how zdap writes a time into a dataset name and into the
// zdap:created_at, zdap:snapped_at and zdap:expires_at properties.
//
// The layout carries no zone, so a zdap timestamp is UTC by convention. Use
// FormatTimestamp and ParseTimestamp rather than time.Format and time.Parse so
// that the convention cannot drift: stamping local wall clock and reading it
// back as UTC shifts the instant by the host's offset, which used to let a
// claimed clone outlive its TTL by an hour on a host that was not on UTC.
const TimestampFormat = "2006-01-02T15.04.05"

// FormatTimestamp renders a time as a zdap timestamp, in UTC.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(TimestampFormat)
}

// ParseTimestamp reads a zdap timestamp back as the instant it denotes.
func ParseTimestamp(s string) (time.Time, error) {
	return time.Parse(TimestampFormat, s)
}

var TimeReg = regexp.MustCompile("[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}")

var cloneReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}-clone-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}.[a-zA-Z]{3}$")
var snapReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}@snap$")
var baseReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}$")

// Pool returns the dataset path zdap keeps its datasets under.
func (z *ZFS) Pool() string { return z.pool }

func (z *ZFS) GetDatasetBaseNameAt(name string, at time.Time) string {
	return fmt.Sprintf("zdap-%s-base-%s", name, FormatTimestamp(at))
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
	err = ds.SetUserProperty(PropCreated, FormatTimestamp(creation))
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
	err = ds.SetUserProperty(PropCreated, FormatTimestamp(created))
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

	created := FormatTimestamp(time.Now())

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

// SetUserProperty sets a zdap:* property on one dataset, named relative to the
// pool.
//
// It takes a name rather than an open dataset so that callers do not have to
// hold a handle from a recursive open just to write one property. Opening the
// single dataset costs 8 ioctls, against 229 for opening the pool.
func (z *ZFS) SetUserProperty(name string, prop string, value string) error {
	z.writeLock()
	defer z.writeUnlock()

	ds, err := zfs.DatasetOpenSingle(fmt.Sprintf("%s/%s", z.pool, name))
	if err != nil {
		return fmt.Errorf("could not open dataset %s: %w", name, err)
	}
	zfsstat.RecordSingleOpen()
	defer ds.Close()

	zfsstat.RecordWrite(zfsstat.OpSetUserProperty)
	return ds.SetUserProperty(prop, value)
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
