package replication

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type fakePusher struct {
	mu       sync.Mutex
	seen     []string
	inFlight int
	peak     int
	hold     time.Duration
	fail     map[string]error
}

func (f *fakePusher) Push(ctx context.Context, peer, pool, dataset string) error {
	f.mu.Lock()
	f.seen = append(f.seen, peer)
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	f.mu.Unlock()

	time.Sleep(f.hold)

	f.mu.Lock()
	f.inFlight--
	err := f.fail[peer]
	f.mu.Unlock()
	return err
}

// The point of the change: the sends must overlap. Run in sequence, a base
// bigger than the free ARC is read off the platters once per peer.
func Test_PushAll_runsTargetsConcurrently(t *testing.T) {
	f := &fakePusher{hold: 50 * time.Millisecond}
	targets := []string{"a", "b", "c"}

	start := time.Now()
	assert.NoError(t, PushAll(context.Background(), f, "pool", "zdap-x-base-2026-01-01T00.00.00", targets))
	elapsed := time.Since(start)

	assert.Equal(t, 3, f.peak, "all three sends should have been in flight at once")
	assert.Less(t, elapsed, 120*time.Millisecond, "sequential would take at least 150ms")
	assert.ElementsMatch(t, targets, f.seen)
}

// One unreachable peer must not stop the others, and every failure must be
// reported rather than only the first.
func Test_PushAll_oneFailureDoesNotStopTheRest(t *testing.T) {
	boom := errors.New("connection refused")
	f := &fakePusher{fail: map[string]error{"b": boom}}

	err := PushAll(context.Background(), f, "pool", "zdap-x-base-2026-01-01T00.00.00",
		[]string{"a", "b", "c"})

	assert.Error(t, err)
	assert.True(t, errors.Is(err, boom))
	assert.Contains(t, err.Error(), "to b")
	assert.ElementsMatch(t, []string{"a", "b", "c"}, f.seen, "a and c still attempted")
}

func Test_PushAll_reportsEveryFailure(t *testing.T) {
	one, two := errors.New("one"), errors.New("two")
	f := &fakePusher{fail: map[string]error{"a": one, "c": two}}

	err := PushAll(context.Background(), f, "pool", "zdap-x-base-2026-01-01T00.00.00",
		[]string{"a", "b", "c"})

	assert.True(t, errors.Is(err, one))
	assert.True(t, errors.Is(err, two))
}

func Test_PushAll_noTargets(t *testing.T) {
	f := &fakePusher{}
	assert.NoError(t, PushAll(context.Background(), f, "pool", "zdap-x", nil))
	assert.Empty(t, f.seen)
}
