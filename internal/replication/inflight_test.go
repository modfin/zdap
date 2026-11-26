package replication

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Two pushes of the same base to the same peer fight over one `zfs recv` and
// the loser fails. This was observed for real: a retry issued while the
// previous attempt was still unwinding failed in under a second, and the same
// thing would happen to a scheduler retrying a slow transfer.
func Test_begin_refusesTheSamePairTwice(t *testing.T) {
	c := &Client{inFlight: map[string]struct{}{}}

	assert.True(t, c.begin("10.100.0.71|zdap-x-base-2026-01-01T00.00.00"))
	assert.False(t, c.begin("10.100.0.71|zdap-x-base-2026-01-01T00.00.00"),
		"second push of the same base to the same peer must be refused")

	// Different peer, and different base on the same peer, are independent.
	assert.True(t, c.begin("10.100.0.72|zdap-x-base-2026-01-01T00.00.00"))
	assert.True(t, c.begin("10.100.0.71|zdap-y-base-2026-01-01T00.00.00"))

	// Releasing lets the next attempt through, so a failed transfer can retry.
	c.end("10.100.0.71|zdap-x-base-2026-01-01T00.00.00")
	assert.True(t, c.begin("10.100.0.71|zdap-x-base-2026-01-01T00.00.00"))
}

func Test_begin_isRaceFree(t *testing.T) {
	c := &Client{inFlight: map[string]struct{}{}}

	var mu sync.Mutex
	won := 0
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if c.begin("peer|dataset") {
				mu.Lock()
				won++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, won, "exactly one caller may hold a pair")
}
