package zfs

import (
	"context"
	"fmt"

	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// SetUserProperty sets one zdap:* property on a dataset, named relative to the
// pool.
func (z *ZFS) SetUserProperty(name string, prop string, value string) error {
	return z.SetUserProperties(name, map[string]string{prop: value})
}

// SetUserProperties sets several properties on one dataset in a single
// command.
//
// ZFS applies them together, so a reader never sees half of them. That matters
// where a pair of properties means something only as a pair: claiming a pooled
// clone writes an owner and an expiry, and a clone carrying one without the
// other is neither free nor properly claimed.
//
// This used to open the dataset and then write each property separately, which
// cost an open plus one call per property, and left exactly that gap between
// them.
func (z *ZFS) SetUserProperties(name string, props map[string]string) error {
	return z.setProperties(context.Background(), name, props)
}

func (z *ZFS) setProperties(ctx context.Context, name string, props map[string]string) error {
	if len(props) == 0 {
		return nil
	}
	if !isZdapName(name) {
		return fmt.Errorf("refusing to set properties on %q: not a zdap base, snapshot or clone name", name)
	}

	dataset := fmt.Sprintf("%s/%s", z.pool, name)

	z.writeLock()
	defer z.writeUnlock()

	argv := append([]string{"set"}, propAssignments(props)...)
	argv = append(argv, dataset)

	zfsstat.Inc(zfsstat.OpZfsSet)
	stdout, stderr, err := execZfs(ctx, argv...)
	if err != nil {
		return fmt.Errorf("could not set properties on %s: %w: %s", dataset, err, zcpMessage(stderr, stdout))
	}
	return nil
}
