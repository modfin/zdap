package replication

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A peer certificate proves who is pushing, not what. These cases must be
// refused by authorize() before any zfs command runs -- the assertion is that
// the error is *Rejected rather than a zfs failure, which is what proves the
// gate came first rather than the pool simply not existing.
func Test_Recv_refusesUnauthorizedDatasets(t *testing.T) {
	r := NewReceiver("zdap/databases", func(resource string) bool {
		return resource == "postgres-carrel"
	})

	for name, dataset := range map[string]string{
		"not a base name":        "tank/anything",
		"path traversal":         "../../../etc/passwd",
		"a clone, not a base":    "zdap-postgres-carrel-base-2026-11-22T10.54.02-clone-2026-11-22T11.00.00.abc",
		"snapshot suffix":        "zdap-postgres-carrel-base-2026-11-22T10.54.02@snap",
		"resource not on node":   "zdap-postgres-elsewhere-base-2026-11-22T10.54.02",
		"empty":                  "",
		"plausible but bogus ts": "zdap-postgres-carrel-base-tomorrow",
	} {
		err := r.Recv(context.Background(), dataset, strings.NewReader(""))
		assert.Error(t, err, name)

		var rejected *Rejected
		assert.True(t, errors.As(err, &rejected),
			"%s: expected a *Rejected before zfs ran, got %v", name, err)
	}
}

func Test_ResumeToken_refusesUnauthorizedDatasets(t *testing.T) {
	r := NewReceiver("zdap/databases", func(string) bool { return false })

	_, err := r.ResumeToken(context.Background(), "zdap-postgres-carrel-base-2026-11-22T10.54.02")
	var rejected *Rejected
	assert.True(t, errors.As(err, &rejected), "got %v", err)
	assert.Contains(t, err.Error(), "not configured on this node")
}

// A name this node does serve must get past the gate and on to zfs, which is
// where it then fails in a unit test because there is no pool. The assertion is
// only that the failure is NOT a *Rejected -- naming the exact zfs command
// would tie the test to whether zfs is installed on the machine running it.
func Test_Recv_authorizedNameReachesZfs(t *testing.T) {
	r := NewReceiver("zdap/databases", func(string) bool { return true })

	err := r.Recv(context.Background(), "zdap-postgres-carrel-base-2026-11-22T10.54.02", strings.NewReader(""))
	assert.Error(t, err)

	var rejected *Rejected
	assert.False(t, errors.As(err, &rejected), "should have passed the gate, got %v", err)
}

// NOTE: the idempotent path -- receiving a base this node already holds and
// reporting success -- cannot be covered here, because it needs a real dataset
// to exist. It is the behaviour that stops a transient failure after a
// successful receive from wedging that base forever, so it belongs in the
// integration pass against a real pool.
func Test_alreadyComplete_unknownDatasetIsNotComplete(t *testing.T) {
	r := NewReceiver("zdap/databases", func(string) bool { return true })

	complete, err := r.alreadyComplete(context.Background(), "zdap-postgres-nope-base-2026-11-22T10.54.02")
	if err != nil {
		t.Skipf("needs a working zfs binary: %v", err)
	}
	assert.False(t, complete)
}
