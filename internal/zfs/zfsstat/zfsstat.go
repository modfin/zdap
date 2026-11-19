// Package zfsstat counts the ZFS work zdapd performs.
//
// It exists to make the cost of zdapd's ZFS access measurable, so that the
// migration off go-libzfs could be driven by numbers instead of intuition.
// That migration is done: every path is a channel program or a zfs/zpool
// command, and the counters now simply say how many of each a request costs.
//
// Two kinds of counters are kept, distinguished by key prefix:
//
//	op.*   one call out to ZFS made by zdapd (see the Op* constants)
//	vol.*  the volume that call pulled out of ZFS (estimated kernel ioctls)
//
// bench/baseline.json holds the pre-migration numbers, recorded while the
// libzfs counters that are gone from here still existed.
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

// Calls out to ZFS. One count == one call from zdap code.
const (
	// OpChannelProgram is one `zfs program` run: a Lua program executed inside
	// the kernel in a single transaction, returning only what was asked for.
	// This is what the read paths run on.
	OpChannelProgram = "op.channel_program"
	// OpZpoolList is one `zpool list` run.
	OpZpoolList = "op.zpool_list"
	// OpZfsDestroy is one `zfs destroy -R -f` run. It replaces a recursive
	// open, an unmount, a Dataset.Clones (itself a full recursive open of the
	// pool) and a libzfs destroy, per level of the hierarchy being removed.
	// ZFS does that walk internally now, so the ioctls it costs are neither
	// counted nor estimated here; what the counter shows is that one command
	// replaced work that used to grow with the size of the pool.
	OpZfsDestroy = "op.zfs_destroy"
	// OpZfsClone is one `zfs clone -o ...` run. It replaces a recursive open
	// of the base dataset, a snapshot lookup, a libzfs clone, six separate
	// property writes and an explicit mount.
	OpZfsClone = "op.zfs_clone"
	// OpZfsGet is one `zfs get` run for a named set of properties on one
	// dataset. This is what reading a couple of properties costs now that it
	// no longer means opening the pool to get at a handle.
	OpZfsGet = "op.zfs_get"
	// OpZfsCreate is one `zfs create -o ...` run: the dataset, its zdap:*
	// properties and its mount, where the libzfs path was a create, two
	// property writes and an explicit mount.
	OpZfsCreate = "op.zfs_create"
	// OpZfsSnapshot is one `zfs snapshot -o ...` run.
	OpZfsSnapshot = "op.zfs_snapshot"
	// OpZfsSet is one `zfs set` run, however many properties it carries. The
	// libzfs path it replaces opened the dataset and then wrote one property
	// per call.
	OpZfsSet = "op.zfs_set"
)

// Volume counters: how much the call actually cost.
const (
	// VolKernelIoctls is the estimated number of /dev/zfs ioctls.
	//
	// Only the two read paths add to it. A zfs create, clone, snapshot, set or
	// destroy does its walk inside the kernel, so what it costs in ioctls is
	// not something zdap can count from the outside -- and no longer something
	// that grows with the size of the pool, which is what this was built to
	// show.
	VolKernelIoctls = "vol.kernel_ioctls_est"
)

const (
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

// Calls is the number of op.* calls in s: every channel program,
// zfs and zpool run zdapd made.
func (s Stats) Calls() uint64 {
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
