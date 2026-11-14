package zfs_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/modfin/zdap/internal/zfs"
	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// These benchmarks measure the ZFS layer directly, without the daemon, so that
// a channel program implementation of a listing can be compared against the
// current one in isolation:
//
//	ZDAP_BENCH_ZPOOL=zdap/1/databases go test ./internal/zfs/ -run '^$' -bench . -benchtime 10x
//
// Besides ns/op they report the ZFS work per operation, which is the number
// that has to come down:
//
//	handles/op   dataset handles materialised, each with its full property table
//	propvals/op  native property values read out of libzfs
//	libzfs/op    libzfs entry point calls
//	ioctls/op    estimated /dev/zfs ioctls
//
// They need read access to a real pool, so they skip unless ZDAP_BENCH_ZPOOL
// names one. Nothing here writes to ZFS.

func benchPool(b *testing.B) *zfs.ZFS {
	b.Helper()
	pool := os.Getenv("ZDAP_BENCH_ZPOOL")
	if pool == "" {
		b.Skip("set ZDAP_BENCH_ZPOOL to a zdap pool, e.g. zdap/1/databases, to run this benchmark")
	}
	z := zfs.NewZFS(pool)
	dss, err := z.Open()
	if err != nil {
		b.Skipf("cannot open pool %s: %v", pool, err)
	}
	dss.Close()
	return z
}

// reportZFSWork turns the counter delta over the whole benchmark into
// per-operation metrics.
func reportZFSWork(b *testing.B, before zfsstat.Stats) {
	b.Helper()
	d := zfsstat.Snapshot().Sub(before)
	n := float64(b.N)
	b.ReportMetric(float64(d[zfsstat.VolDatasetHandles])/n, "handles/op")
	b.ReportMetric(float64(d[zfsstat.VolPropertyValues])/n, "propvals/op")
	b.ReportMetric(float64(d.LibzfsCalls())/n, "libzfs/op")
	b.ReportMetric(float64(d[zfsstat.VolKernelIoctls])/n, "ioctls/op")
}

// BenchmarkOpen measures the recursive open of the pool on its own. Every REST
// endpoint starts with one of these.
func BenchmarkOpen(b *testing.B) {
	z := benchPool(b)

	before := zfsstat.Snapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dss, err := z.Open()
		if err != nil {
			b.Fatal(err)
		}
		dss.Close()
	}
	b.StopTimer()
	reportZFSWork(b, before)
}

// BenchmarkListSnaps measures listing snapshots off an already open pool, the
// way the handlers do it.
func BenchmarkListSnaps(b *testing.B) {
	z := benchPool(b)
	dss, err := z.Open()
	if err != nil {
		b.Fatal(err)
	}
	defer dss.Close()

	before := zfsstat.Snapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := z.ListSnaps(dss); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportZFSWork(b, before)
}

// BenchmarkListClones measures listing clones off an already open pool.
func BenchmarkListClones(b *testing.B) {
	z := benchPool(b)
	dss, err := z.Open()
	if err != nil {
		b.Fatal(err)
	}
	defer dss.Close()

	before := zfsstat.Snapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := z.ListClones(dss); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportZFSWork(b, before)
}

// BenchmarkListBases measures listing base datasets off an already open pool.
func BenchmarkListBases(b *testing.B) {
	z := benchPool(b)
	dss, err := z.Open()
	if err != nil {
		b.Fatal(err)
	}
	defer dss.Close()

	before := zfsstat.Snapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := z.ListBases(dss); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportZFSWork(b, before)
}

// BenchmarkOpenAndListAll is the shape of GET /resources: open the pool once,
// then list snapshots and clones once per configured resource. It is the
// clearest view of the repeated full walks the current code does, and the one
// a channel program implementation should flatten.
func BenchmarkOpenAndListAll(b *testing.B) {
	z := benchPool(b)

	resources := 15 // roughly what a zdap host carries
	if v := os.Getenv("ZDAP_BENCH_RESOURCES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			resources = n
		}
	}
	b.Logf("simulating %d resources", resources)

	before := zfsstat.Snapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dss, err := z.Open()
		if err != nil {
			b.Fatal(err)
		}
		for r := 0; r < resources; r++ {
			if _, err := z.ListSnaps(dss); err != nil {
				b.Fatal(err)
			}
			if _, err := z.ListClones(dss); err != nil {
				b.Fatal(err)
			}
		}
		dss.Close()
	}
	b.StopTimer()
	reportZFSWork(b, before)
}
