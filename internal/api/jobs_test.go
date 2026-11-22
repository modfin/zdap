package api

import (
	"context"
	"testing"
	"time"

	"github.com/modfin/zdap/internal/replication"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// finish runs a job to completion so the registry sees a terminal state.
func finish(t *testing.T, r *jobRegistry, err error) string {
	t.Helper()
	done := make(chan struct{})
	id := r.start(func(context.Context, func(replication.Progress)) error {
		close(done)
		return err
	})
	<-done
	require.Eventually(t, func() bool {
		j, _ := r.get(id)
		return j.State != "running"
	}, time.Second, time.Millisecond)
	return id
}

func Test_list_newestFirst(t *testing.T) {
	r := newJobs()
	for range 3 {
		finish(t, r, nil)
		time.Sleep(2 * time.Millisecond) // distinct Started timestamps
	}
	got := r.list("")
	require.Len(t, got, 3)
	for i := 1; i < len(got); i++ {
		assert.False(t, got[i].Started.After(got[i-1].Started), "must be newest first")
	}
}

// The query a scheduler actually makes: what is in flight right now?
func Test_list_filtersByState(t *testing.T) {
	r := newJobs()
	finish(t, r, nil)
	block := make(chan struct{})
	defer close(block)
	r.start(func(context.Context, func(replication.Progress)) error {
		<-block
		return nil
	})

	require.Eventually(t, func() bool { return len(r.list("running")) == 1 }, time.Second, time.Millisecond)
	assert.Len(t, r.list("done"), 1)
	assert.Len(t, r.list(""), 2)
	assert.Empty(t, r.list("failed"))
}

// Nothing removed finished jobs before, so a daemon replicating nightly
// accumulated them for its whole lifetime.
func Test_prune_boundsFinishedJobs(t *testing.T) {
	r := newJobs()
	for range keepFinished + 25 {
		finish(t, r, nil)
	}
	assert.LessOrEqual(t, len(r.list("")), keepFinished+1,
		"finished jobs must be bounded")
}

// A running job must never be pruned, however many finish around it.
func Test_prune_keepsRunningJobs(t *testing.T) {
	r := newJobs()
	block := make(chan struct{})
	defer close(block)
	running := r.start(func(context.Context, func(replication.Progress)) error {
		<-block
		return nil
	})
	for range keepFinished + 50 {
		finish(t, r, nil)
	}
	_, ok := r.get(running)
	assert.True(t, ok, "a running job was pruned")
}

func Test_list_copiesProgress(t *testing.T) {
	r := newJobs()
	block := make(chan struct{})
	defer close(block)
	r.start(func(_ context.Context, report func(replication.Progress)) error {
		report(replication.Progress{Peer: "10.0.0.1", Sent: 42})
		<-block
		return nil
	})
	require.Eventually(t, func() bool {
		l := r.list("running")
		return len(l) == 1 && len(l[0].Progress) == 1
	}, time.Second, time.Millisecond)

	// Mutating what we got back must not reach the registry.
	got := r.list("running")[0]
	got.Progress["10.0.0.1"] = replication.Progress{Sent: 999}
	assert.Equal(t, int64(42), r.list("running")[0].Progress["10.0.0.1"].Sent)
}
