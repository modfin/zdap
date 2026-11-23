package zfs

import (
	"context"
	"fmt"
	"time"

	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// CreateDataset makes the dataset a base restore is written into, and returns
// the path it is mounted on.
//
// The zdap:* properties are set as part of creating it rather than afterwards,
// so the dataset is never briefly visible to a listing without the resource it
// belongs to. rt is the container the restore runs in; it is recorded here
// because the image is already decided, and so that a base never exists
// without it -- clones of it read it back instead of trusting the resource
// file, which may since have moved on to another image.
func (z *ZFS) CreateDataset(name string, resource string, creation time.Time, rt Runtime, zfsProps map[string]string) (string, error) {
	return z.createDataset(context.Background(), name, resource, creation, rt, zfsProps)
}

func (z *ZFS) createDataset(ctx context.Context, name string, resource string, creation time.Time, rt Runtime, zfsProps map[string]string) (string, error) {
	// A base that does not carry a name zdap recognises can never be listed,
	// snapshotted or cloned, so there is no point creating one.
	if !IsBaseName(name) {
		return "", fmt.Errorf("%q is not a zdap base name", name)
	}

	z.writeLock()
	defer z.writeUnlock()

	path := fmt.Sprintf("%s/%s", z.pool, name)

	own := rt.props()
	own[PropResource] = resource
	own[PropCreated] = FormatTimestamp(creation)

	argv := []string{"create"}
	argv = append(argv, zdapProps(own, zfsProps)...)
	argv = append(argv, path)

	zfsstat.Inc(zfsstat.OpZfsCreate)
	stdout, stderr, err := execZfs(ctx, argv...)
	if err != nil {
		return "", fmt.Errorf("could not create %s: %w: %s", path, err, zcpMessage(stderr, stdout))
	}

	return z.mountedPath(ctx, path)
}

// SnapDataset takes the @snap snapshot of a finished base.
//
// The snapshot carries the same zdap:resource and zdap:created_at as the
// dataset it was taken of, which is what a Listing reports for it and what
// CloneDataset copies onto a clone as zdap:snapped_at.
func (z *ZFS) SnapDataset(name string, resource string, created time.Time) error {
	return z.snapDataset(context.Background(), name, resource, created)
}

func (z *ZFS) snapDataset(ctx context.Context, name string, resource string, created time.Time) error {
	if !IsBaseName(name) {
		return fmt.Errorf("%q is not a zdap base name", name)
	}

	z.writeLock()
	defer z.writeUnlock()

	snap := fmt.Sprintf("%s/%s@snap", z.pool, name)

	argv := []string{"snapshot"}
	argv = append(argv, propArgs(map[string]string{
		PropResource: resource,
		PropCreated:  FormatTimestamp(created),
	})...)
	argv = append(argv, snap)

	zfsstat.Inc(zfsstat.OpZfsSnapshot)
	stdout, stderr, err := execZfs(ctx, argv...)
	if err != nil {
		return fmt.Errorf("could not snapshot %s: %w: %s", snap, err, zcpMessage(stderr, stdout))
	}
	return nil
}
