// Package zfsstat counts the ZFS work zdapd performs.
//
// It exists to make the cost of the current "fetch everything, filter in Go"
// access pattern measurable, so that the migration to zfs channel programs can
// be driven by numbers instead of intuition.
//
// Two kinds of counters are kept, distinguished by key prefix:
//
//	op.*   one libzfs entry point call made by zdapd (see the Op* constants)
//	vol.*  the volume of data that call pulled out of ZFS (handles, property
//	       values, estimated kernel ioctls)
//
// All counters are process global and monotonically increasing. A measurement
// is a Snapshot before and after the work, then Sub. That is exact as long as
// the work is the only thing touching ZFS at the time, which is why the HTTP
// recorder below also tracks whether requests overlapped.
package zfsstat

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// libzfs entry points that zdapd calls. One count == one call from zdap code.
const (
	// OpDatasetOpenRecursive is zfs.DatasetOpen: opens the named dataset and
	// every descendant filesystem and snapshot, loading the full property
	// table for each one. This is what ZFS.Open does on the pool root, and it
	// is the single most expensive thing zdapd does.
	OpDatasetOpenRecursive = "op.dataset_open_recursive"
	// OpDatasetOpenSingle is zfs.DatasetOpenSingle: one dataset, no children.
	OpDatasetOpenSingle = "op.dataset_open_single"
	OpDatasetCreate     = "op.dataset_create"
	OpDatasetSnapshot   = "op.dataset_snapshot"
	OpDatasetClone      = "op.dataset_clone"
	OpDatasetDestroy    = "op.dataset_destroy"
	// OpDatasetClones is Dataset.Clones. Note that go-libzfs implements it by
	// re-opening the whole pool recursively, so each call costs another full
	// OpDatasetOpenRecursive underneath.
	OpDatasetClones = "op.dataset_clones"
	// OpDatasetPath is Dataset.Path. Served from the already open handle, so
	// no kernel round trip, but zdapd calls it once per dataset per listing.
	OpDatasetPath = "op.dataset_path"
	// OpDatasetSnapshots is Dataset.Snapshots, an in-memory filter over the
	// children that were already materialised by the recursive open.
	OpDatasetSnapshots = "op.dataset_snapshots"
	// OpGetUserProperty is Dataset.GetUserProperty. Served from the open
	// handle's nvlist, but it takes the go-libzfs global lock every time.
	OpGetUserProperty  = "op.get_user_property"
	OpSetUserProperty  = "op.set_user_property"
	OpSetProperty      = "op.set_property"
	OpFindSnapshotName = "op.find_snapshot_name"
	OpMount            = "op.mount"
	OpUnmountAll       = "op.unmount_all"
	OpIsMounted        = "op.is_mounted"
	// OpChannelProgram is one `zfs program` run: a Lua program executed inside
	// the kernel in a single transaction, returning only what was asked for.
	// This is what the libzfs read paths are being migrated to.
	OpChannelProgram = "op.channel_program"
	// OpZpoolList is one `zpool list` run.
	OpZpoolList = "op.zpool_list"
)

// Volume counters: how much was actually fetched.
const (
	// VolDatasetHandles is the number of dataset handles materialised by
	// recursive opens: one per filesystem and one per snapshot, each with its
	// full property table loaded.
	VolDatasetHandles = "vol.dataset_handles"
	// VolPropertyTableLoads is the number of full property-table loads
	// (zfs_refresh_properties + a read of every native property). One per
	// materialised handle.
	VolPropertyTableLoads = "vol.property_table_loads"
	// VolPropertyValues is the number of individual native property values
	// read out of libzfs, i.e. VolPropertyTableLoads * NativePropsPerDataset.
	VolPropertyValues = "vol.property_values"
	// VolKernelIoctls is the estimated number of /dev/zfs ioctls. See
	// IoctlsPerHandle for how the estimate is calibrated and checked.
	VolKernelIoctls = "vol.kernel_ioctls_est"
)

// NativePropsPerDataset is how many native properties go-libzfs reads for every
// dataset handle it opens (the DatasetPropType..DatasetNumProps loop in
// Dataset.ReloadProperties). zdap reads at most 8 of them.
const NativePropsPerDataset = 91

// Calibration of the ioctl estimate, measured on ZFS 2.4.1 with
// go-libzfs v2.22.1 by tracing ioctls on /dev/zfs (bench/zfs-ioctl-bench.sh
// re-measures it):
//
//	DatasetOpenSingle(pool)            8 ioctls
//	DatasetOpen(pool), 34 handles    252 ioctls
//	Path / GetUserProperty             0 ioctls (served from the open handle)
//
// 252 ioctls over 34 handles is 7.4 per handle: 4x OBJSET_ZPLPROPS,
// 1x OBJSET_STATS, ~1.3x DATASET_LIST_NEXT and 1x SNAPSHOT_LIST_NEXT.
const (
	// IoctlsPerHandleX10 is the measured ioctl cost of materialising one dataset
	// handle during a recursive open, scaled by 10 to stay in integers.
	IoctlsPerHandleX10 = 74
	// IoctlsPerSingleOpen is the measured ioctl cost of DatasetOpenSingle.
	IoctlsPerSingleOpen = 8
	// IoctlsPerWrite is the assumed cost of a property set, create, clone,
	// snapshot or destroy. These are not on the read path the migration
	// targets, so they are counted but not calibrated.
	IoctlsPerWrite = 1
	// IoctlsPerChannelProgram is the cost of a `zfs program` run: one
	// ZFS_IOC_CHANNEL_PROGRAM, whatever the program walks inside the kernel.
	// That invariance is the point of the migration.
	IoctlsPerChannelProgram = 1
	// IoctlsPerZpoolList is the measured ZFS ioctl cost of `zpool list -Hp`:
	// ZFS_IOC_POOL_STATS, ZFS_IOC_POOL_GET_PROPS and ZFS_IOC_LOG_HISTORY.
	IoctlsPerZpoolList = 3
)

// Both of the above run in a child process, so unlike the libzfs counters they
// also cost a fork and exec, around 4 ms for zpool list on the machine the
// calibration was done on. The benchmark's wall clock column covers that.

var counters sync.Map // string -> *atomic.Uint64

func counter(key string) *atomic.Uint64 {
	if c, ok := counters.Load(key); ok {
		return c.(*atomic.Uint64)
	}
	c, _ := counters.LoadOrStore(key, new(atomic.Uint64))
	return c.(*atomic.Uint64)
}

// Add adds n to a counter.
func Add(key string, n uint64) {
	if n == 0 {
		return
	}
	counter(key).Add(n)
}

// Inc adds one to a counter.
func Inc(key string) { counter(key).Add(1) }

// RecordRecursiveOpen records one zfs.DatasetOpen that materialised handles
// dataset handles (the dataset itself plus every descendant and snapshot).
func RecordRecursiveOpen(handles int) {
	if handles < 0 {
		handles = 0
	}
	h := uint64(handles)
	Inc(OpDatasetOpenRecursive)
	Add(VolDatasetHandles, h)
	Add(VolPropertyTableLoads, h)
	Add(VolPropertyValues, h*NativePropsPerDataset)
	Add(VolKernelIoctls, (h*IoctlsPerHandleX10)/10)
}

// RecordSingleOpen records one zfs.DatasetOpenSingle.
func RecordSingleOpen() {
	Inc(OpDatasetOpenSingle)
	Add(VolDatasetHandles, 1)
	Add(VolPropertyTableLoads, 1)
	Add(VolPropertyValues, NativePropsPerDataset)
	Add(VolKernelIoctls, IoctlsPerSingleOpen)
}

// RecordWrite records a libzfs call that changes state.
func RecordWrite(op string) {
	Inc(op)
	Add(VolKernelIoctls, IoctlsPerWrite)
}

// Stats is a point-in-time copy of the counters.
type Stats map[string]uint64

// Snapshot copies the current counter values.
func Snapshot() Stats {
	s := Stats{}
	counters.Range(func(k, v any) bool {
		s[k.(string)] = v.(*atomic.Uint64).Load()
		return true
	})
	return s
}

// Sub returns s minus other, dropping keys that did not change. A counter that
// went backwards yields nothing rather than an underflowed value; the counters
// are monotonic, so that only happens if the two snapshots are swapped.
func (s Stats) Sub(other Stats) Stats {
	d := Stats{}
	for k, v := range s {
		if o := other[k]; v > o {
			d[k] = v - o
		}
	}
	return d
}

// LibzfsCalls is the number of op.* calls in s.
func (s Stats) LibzfsCalls() uint64 {
	var n uint64
	for k, v := range s {
		if len(k) > 3 && k[:3] == "op." {
			n += v
		}
	}
	return n
}

// Keys returns the counter names in s, sorted.
func (s Stats) Keys() []string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- per-request attribution -------------------------------------------------

var inFlight atomic.Int64

// Enter marks the start of a measured request and reports whether another
// measured request was already running, in which case the counter delta cannot
// be attributed to either one.
func Enter() (overlapped bool) { return inFlight.Add(1) > 1 }

// Leave marks the end of a measured request.
func Leave() { inFlight.Add(-1) }

// RouteStats is the accumulated ZFS cost of one route.
type RouteStats struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// Requests is how many requests were measured.
	Requests uint64 `json:"requests"`
	// Overlapped is how many of those ran concurrently with another request.
	// Their counter deltas are not trustworthy; benchmark serially.
	Overlapped uint64 `json:"overlapped"`
	// Ops is the sum of the counter deltas over all measured requests.
	Ops Stats `json:"ops"`
	// DurationNs is the summed handler wall time.
	DurationNs    uint64 `json:"duration_ns"`
	MaxDurationNs uint64 `json:"max_duration_ns"`
}

// PerRequest returns ops divided by the number of requests, as a float map.
func (r RouteStats) PerRequest() map[string]float64 {
	m := map[string]float64{}
	if r.Requests == 0 {
		return m
	}
	for k, v := range r.Ops {
		m[k] = float64(v) / float64(r.Requests)
	}
	return m
}

var (
	routeMu    sync.Mutex
	routeStats = map[string]*RouteStats{}
)

// RecordRequest attributes a counter delta to a route.
func RecordRequest(method, path string, dur time.Duration, delta Stats, overlapped bool) {
	key := method + " " + path
	routeMu.Lock()
	defer routeMu.Unlock()

	r, ok := routeStats[key]
	if !ok {
		r = &RouteStats{Method: method, Path: path, Ops: Stats{}}
		routeStats[key] = r
	}
	r.Requests++
	if overlapped {
		r.Overlapped++
	}
	for k, v := range delta {
		r.Ops[k] += v
	}
	ns := uint64(dur.Nanoseconds())
	r.DurationNs += ns
	if ns > r.MaxDurationNs {
		r.MaxDurationNs = ns
	}
}

// Routes returns a copy of the per-route stats, sorted by route key.
func Routes() []RouteStats {
	routeMu.Lock()
	defer routeMu.Unlock()

	out := make([]RouteStats, 0, len(routeStats))
	for _, r := range routeStats {
		c := *r
		c.Ops = Stats{}
		for k, v := range r.Ops {
			c.Ops[k] = v
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// ResetRoutes clears the per-route stats, so a benchmark run can start clean.
func ResetRoutes() {
	routeMu.Lock()
	defer routeMu.Unlock()
	routeStats = map[string]*RouteStats{}
}
