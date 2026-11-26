package clonepool

import (
	"testing"
	"time"

	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/cloning"
	"github.com/modfin/zdap/internal/zfs"
)

// newTestPool builds a pool whose actions fail immediately: the dataset does
// not exist and channel programs need root, so action() returns at its first
// call and the loop goes back to waiting. That is enough to exercise the
// lifecycle without touching a real pool.
func newTestPool() *ClonePool {
	r := internal.Resource{Name: "test"}
	return NewClonePool(r, &cloning.CloneContext{
		Resource: r,
		Z:        zfs.NewZFS("zdap/does-not-exist"),
	})
}

func TestStopAndWaitEndsTheLoop(t *testing.T) {
	p := newTestPool()
	p.Start()

	done := make(chan struct{})
	go func() {
		p.StopAndWait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pool loop did not exit after StopAndWait")
	}
}

// A pool that is replaced on reload is stopped while it may be in the middle
// of a trigger, so Stop has to be safe whatever the loop is doing, and safe to
// call more than once.
func TestStopIsIdempotentAndDoesNotBlock(t *testing.T) {
	p := newTestPool()
	p.Start()

	p.TriggerGC()
	p.Stop()
	p.Stop()
	p.Stop()

	done := make(chan struct{})
	go func() {
		p.loop.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pool loop did not exit")
	}
}

// TriggerGC must not block or panic once the pool has been stopped: cron jobs
// registered before a reload call it through a lookup that may race with the
// pool being replaced.
func TestTriggerGCAfterStopIsHarmless(t *testing.T) {
	p := newTestPool()
	p.Start()
	p.StopAndWait()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			p.TriggerGC()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("TriggerGC blocked on a stopped pool")
	}
}

func TestStopBeforeStartDoesNotPanic(t *testing.T) {
	p := newTestPool()
	p.Stop()
	p.StopAndWait()
}

func TestResourceReportsWhatThePoolWasBuiltWith(t *testing.T) {
	r := internal.Resource{Name: "test"}
	r.ClonePool.MinClones = 3
	p := NewClonePool(r, &cloning.CloneContext{Resource: r, Z: zfs.NewZFS("zdap/x")})

	if got := p.Resource(); got.Name != "test" || got.ClonePool.MinClones != 3 {
		t.Errorf("Resource() = %+v", got)
	}
}
