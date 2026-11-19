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
// pool-relative name and the path it is mounted on.
//
// snapName is pool relative, as it appears in a Listing: "<base>@snap".
func (z *ZFS) CloneDataset(owner, snapName string, port int, clonePooled bool, zfsProps map[string]string) (string, string, error) {
	return z.cloneDataset(context.Background(), owner, snapName, port, clonePooled, zfsProps)
}

func (z *ZFS) cloneDataset(ctx context.Context, owner, snapName string, port int, clonePooled bool, zfsProps map[string]string) (string, string, error) {
	// Also establishes that the part before the @ is a base name, so the
	// clone is named after something zdap created.
	if !IsSnapName(snapName) {
		return "", "", fmt.Errorf("%q is not a zdap snapshot name", snapName)
	}
	baseName, _, _ := strings.Cut(snapName, "@")

	z.writeLock()
	defer z.writeUnlock()

	// The clone's zdap:resource and zdap:snapped_at come from the dataset the
	// snapshot was taken of, which is where CreateDataset wrote them.
	origin := fmt.Sprintf("%s/%s", z.pool, baseName)
	originProps, err := z.getProps(ctx, origin, PropResource, PropCreated)
	if err != nil {
		return "", "", err
	}

	created := FormatTimestamp(time.Now())
	cloneName := fmt.Sprintf("%s-clone-%s.%s", baseName, created, utils.RandStringRunes(3))

	own := map[string]string{
		PropOwner:       owner,
		PropCreated:     created,
		PropClonePooled: strconv.FormatBool(clonePooled),
		PropPort:        strconv.Itoa(port),
	}
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
		return "", "", fmt.Errorf("could not clone %s: %w: %s", snapName, err, zcpMessage(stderr, stdout))
	}

	path, err := z.mountedPath(ctx, clonePath)
	if err != nil {
		return "", "", err
	}
	return cloneName, path, nil
}
