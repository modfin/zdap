package zfs

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// PoolSpace is the capacity of the pool a zdap dataset lives on, in bytes.
type PoolSpace struct {
	// Total is the pool's size, Used what is allocated, Free the difference.
	Total uint64
	Used  uint64
	Free  uint64
}

// execZpool runs the zpool command and returns its stdout and stderr. It is a
// variable so that tests can substitute a stub for it.
var execZpool = func(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "zpool", args...)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// Space returns the pool's capacity in one call.
//
// This used to go through go-libzfs, as three separate Dataset.Pool() plus
// Pool.VDevTree() round trips for three numbers off the same struct. That path
// panics against ZFS 2.4.1: go-libzfs v2.22.1 sizes its pool property array for
// an older ZFS, so Pool.ReloadProperties writes past the end of it
// (index out of range [40] with length 37, zpool.go:529), which took down
// GET /status. zpool list reports exactly the same vdev statistics, in one
// call, without the version coupling.
func (z *ZFS) Space(ctx context.Context) (PoolSpace, error) {
	pool := poolName(z.pool)

	zfsstat.Inc(zfsstat.OpZpoolList)
	zfsstat.Add(zfsstat.VolKernelIoctls, zfsstat.IoctlsPerZpoolList)

	// -H drops the header and separates with tabs, -p prints exact byte counts
	// rather than human readable ones.
	stdout, stderr, err := execZpool(ctx, "list", "-Hp", "-o", "size,allocated,free", pool)
	if err != nil {
		return PoolSpace{}, fmt.Errorf("could not list pool %s: %w: %s", pool, err, zcpMessage(stderr, stdout))
	}

	line := strings.TrimSpace(string(stdout))
	if line == "" {
		return PoolSpace{}, fmt.Errorf("zpool list reported nothing for pool %s", pool)
	}
	// Guard against a pool name that matched more than one line.
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}

	fields := strings.Fields(line)
	if len(fields) != 3 {
		return PoolSpace{}, fmt.Errorf("zpool list returned %d fields for pool %s, want 3: %q", len(fields), pool, line)
	}

	var space PoolSpace
	for i, dst := range []*uint64{&space.Total, &space.Used, &space.Free} {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return PoolSpace{}, fmt.Errorf("could not parse %q from zpool list for pool %s: %w", fields[i], pool, err)
		}
		*dst = v
	}
	return space, nil
}
