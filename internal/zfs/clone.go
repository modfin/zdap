package zfs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// Cloning used to open the base dataset recursively just to find one snapshot
// by name and read two properties off it -- around 252 ioctls for data that
// fits in two lines -- and then write the clone's six zdap:* properties one
// call at a time, after the clone already existed. `zfs clone -o` takes every
// property at creation instead, so the clone is never briefly visible without
// them, and `zfs get` fetches the two it needs from the origin without
// materialising a handle for every dataset on the pool.

// CloneDataset makes a writable clone of a snapshot and returns the clone's
// pool-relative name, the path it is mounted on, and the container the base
// was restored in as far as the base records it.
//
// snapName is pool relative, as it appears in a Listing: "<base>@snap".
func (z *ZFS) CloneDataset(owner, snapName string, port int, clonePooled bool, zfsProps map[string]string) (string, string, Runtime, error) {
	return z.cloneDataset(context.Background(), owner, snapName, port, clonePooled, zfsProps)
}

func (z *ZFS) cloneDataset(ctx context.Context, owner, snapName string, port int, clonePooled bool, zfsProps map[string]string) (string, string, Runtime, error) {
	// Also establishes that the part before the @ is a base name, so the
	// clone is named after something zdap created.
	if !IsSnapName(snapName) {
		return "", "", Runtime{}, fmt.Errorf("%q is not a zdap snapshot name", snapName)
	}
	baseName, _, _ := strings.Cut(snapName, "@")

	z.writeLock()
	defer z.writeUnlock()

	// The clone's zdap:resource and zdap:snapped_at come from the dataset the
	// snapshot was taken of, which is where CreateDataset wrote them, and so
	// does the container it has to run in. All four in one read.
	originPath := fmt.Sprintf("%s/%s", z.pool, baseName)
	originProps, err := z.getProps(ctx, originPath, PropResource, PropCreated, PropImage, PropVolume)
	if err != nil {
		return "", "", Runtime{}, err
	}

	created := FormatTimestamp(time.Now())
	cloneName := fmt.Sprintf("%s-clone-%s.%s", baseName, created, utils.RandStringRunes(3))

	origin := Runtime{Image: originProps[PropImage], Volume: originProps[PropVolume]}

	// The clone carries its base's runtime too, so `zfs get` on a clone says
	// what it runs without having to follow its origin.
	own := origin.props()
	own[PropOwner] = owner
	own[PropCreated] = created
	own[PropClonePooled] = strconv.FormatBool(clonePooled)
	own[PropPort] = strconv.Itoa(port)
	if v := originProps[PropResource]; v != "" {
		own[PropResource] = v
	}
	if v := originProps[PropCreated]; v != "" {
		own[PropSnappedAt] = v
	}

	clonePath := fmt.Sprintf("%s/%s", z.pool, cloneName)

	argv := []string{"clone"}
	argv = append(argv, zdapProps(own, zfsProps)...)
	argv = append(argv, fmt.Sprintf("%s/%s", z.pool, snapName), clonePath)

	zfsstat.Inc(zfsstat.OpZfsClone)
	stdout, stderr, err := execZfs(ctx, argv...)
	if err != nil {
		return "", "", Runtime{}, fmt.Errorf("could not clone %s: %w: %s", snapName, err, zcpMessage(stderr, stdout))
	}

	path, err := z.mountedPath(ctx, clonePath)
	if err != nil {
		return "", "", Runtime{}, err
	}
	return cloneName, path, origin, nil
}
