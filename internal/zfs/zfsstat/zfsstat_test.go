package zfsstat

import (
	"testing"
	"time"
)

func TestSubDropsUnchangedCounters(t *testing.T) {
	before := Stats{"op.x": 3, "op.y": 1}
	after := Stats{"op.x": 3, "op.y": 5, "op.z": 2}

	d := after.Sub(before)
	if _, ok := d["op.x"]; ok {
		t.Error("unchanged counter should be dropped from the delta")
	}
	if got := d["op.y"]; got != 4 {
		t.Errorf("op.y delta = %d, want 4", got)
	}
	if got := d["op.z"]; got != 2 {
		t.Errorf("new counter delta = %d, want 2", got)
	}
}

func TestSubDoesNotUnderflow(t *testing.T) {
	// Counters are monotonic, so this only happens if the snapshots are passed
	// the wrong way round. It must not produce a huge bogus delta.
	d := Stats{"op.y": 1}.Sub(Stats{"op.y": 5})
	if len(d) != 0 {
		t.Errorf("delta = %v, want empty", d)
	}
}

func TestCallsCountsOnlyOpCounters(t *testing.T) {
	s := Stats{
		OpChannelProgram: 1,
		OpZfsGet:         30,
		VolKernelIoctls:  3094,
	}
	if got := s.Calls(); got != 31 {
		t.Errorf("calls = %d, want 31", got)
	}
}

func TestRecordRequestAccumulatesPerRoute(t *testing.T) {
	ResetRoutes()
	t.Cleanup(ResetRoutes)

	RecordRequest("GET", "/resources", 10*time.Millisecond, Stats{"op.dataset_path": 99}, false)
	RecordRequest("GET", "/resources", 30*time.Millisecond, Stats{"op.dataset_path": 101}, true)
	RecordRequest("GET", "/status", time.Millisecond, Stats{"op.pool_open": 3}, false)

	routes := Routes()
	if len(routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(routes))
	}

	byPath := map[string]RouteStats{}
	for _, r := range routes {
		byPath[r.Path] = r
	}

	res := byPath["/resources"]
	if res.Requests != 2 {
		t.Errorf("requests = %d, want 2", res.Requests)
	}
	if res.Overlapped != 1 {
		t.Errorf("overlapped = %d, want 1", res.Overlapped)
	}
	if res.Ops["op.dataset_path"] != 200 {
		t.Errorf("paths = %d, want 200", res.Ops["op.dataset_path"])
	}
	if got := res.PerRequest()["op.dataset_path"]; got != 100 {
		t.Errorf("paths per request = %v, want 100", got)
	}
	if res.MaxDurationNs != uint64(30*time.Millisecond) {
		t.Errorf("max duration = %d, want %d", res.MaxDurationNs, 30*time.Millisecond)
	}
}

func TestRoutesReturnsACopy(t *testing.T) {
	ResetRoutes()
	t.Cleanup(ResetRoutes)

	RecordRequest("GET", "/status", time.Millisecond, Stats{"op.pool_open": 1}, false)

	routes := Routes()
	routes[0].Ops["op.pool_open"] = 999

	if got := Routes()[0].Ops["op.pool_open"]; got != 1 {
		t.Errorf("mutating the returned stats changed the registry: got %d, want 1", got)
	}
}

func TestEnterReportsOverlap(t *testing.T) {
	if Enter() {
		t.Error("first Enter should not report an overlap")
	}
	if !Enter() {
		t.Error("second concurrent Enter should report an overlap")
	}
	Leave()
	Leave()

	if Enter() {
		t.Error("Enter after both left should not report an overlap")
	}
	Leave()
}
