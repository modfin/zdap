package zfs_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/modfin/zdap/internal/zfs"
	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// These benchmarks measure the ZFS layer directly, without the daemon:
//
//	sudo env ZDAP_BENCH_ZPOOL=zdap/1/databases go test ./internal/zfs/ -run '^$' -bench . -benchtime 10x
//
// sudo is needed because Load runs a zfs channel program, and channel programs
// are root only. Besides ns/op they report the ZFS work per operation:
//
//	zfscalls/op  calls out to ZFS: channel programs and zfs/zpool runs
//	ioctls/op    estimated /dev/zfs ioctls

func benchPool(b *testing.B) *zfs.ZFS {
	b.Helper()
	pool := os.Getenv("ZDAP_BENCH_ZPOOL")
	if pool == "" {
		b.Skip("set ZDAP_BENCH_ZPOOL to a zdap pool, e.g. zdap/1/databases, to run this benchmark")
	}
	z := zfs.NewZFS(pool)
	if _, err := z.Load(context.Background()); err != nil {
		b.Skipf("cannot list pool %s: %v", pool, err)
	}
	return z
}

func benchListing(b *testing.B) *zfs.Listing {
	b.Helper()
	lst, err := benchPool(b).Load(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	return lst
}

// reportZFSWork turns the counter delta over the whole benchmark into
// per-operation metrics.
func reportZFSWork(b *testing.B, before zfsstat.Stats) {
	b.Helper()
	d := zfsstat.Snapshot().Sub(before)
	n := float64(b.N)
	b.ReportMetric(float64(d.Calls())/n, "zfscalls/op")
	b.ReportMetric(float64(d[zfsstat.VolKernelIoctls])/n, "ioctls/op")
}

// BenchmarkLoad measures fetching the whole listing. Every read endpoint does
// exactly one of these, where it used to do a recursive open of the pool.
func BenchmarkLoad(b *testing.B) {
	z := benchPool(b)

	before := zfsstat.Snapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := z.Load(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportZFSWork(b, before)
}

// BenchmarkSnaps measures picking the snapshots out of a loaded listing.
func BenchmarkSnaps(b *testing.B) {
	lst := benchListing(b)

	before := zfsstat.Snapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = lst.Snaps()
	}
	b.StopTimer()
	reportZFSWork(b, before)
}

// BenchmarkClones measures picking the clones out of a loaded listing,
// including parsing their zdap:* properties.
func BenchmarkClones(b *testing.B) {
	lst := benchListing(b)

	before := zfsstat.Snapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = lst.Clones()
	}
	b.StopTimer()
	reportZFSWork(b, before)
}

// BenchmarkLoadAndListAll is the shape of GET /resources: load the listing
// once, then pick out snapshots and clones once per configured resource. It is
// the comparison against the old recursive open plus a walk per resource.
func BenchmarkLoadAndListAll(b *testing.B) {
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
		lst, err := z.Load(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		for r := 0; r < resources; r++ {
			_ = lst.Snaps()
			_ = lst.Clones()
		}
	}
	b.StopTimer()
	reportZFSWork(b, before)
}
