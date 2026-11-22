package replication

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Pusher sends one base to one peer. The interface exists so that PushAll's
// concurrency can be tested without a network or a pool.
type Pusher interface {
	Push(ctx context.Context, peer, pool, dataset string, report func(Progress)) error
}

// PushAll sends a base to every target at once.
//
// Parallel rather than sequential for two reasons. The link is far faster than
// a single stream -- 10 GbE against a source read of roughly 250-475 MB/s --
// so one send at a time leaves most of the network idle. More importantly, the
// sends share the source's ARC: run together they read each block from disk
// about once and the second stream is served from cache, where run in sequence
// a base larger than the free ARC is read from disk again for every peer. On a
// 118 GB jamboree that is the difference between one pass over the platters and
// one per target, at a time when the pool is already busy restoring.
//
// Every target is attempted even if an earlier one fails: one unreachable peer
// should not stop the others. Errors come back joined, in target order rather
// than completion order so the message does not vary run to run.
func PushAll(ctx context.Context, p Pusher, pool, dataset string, targets []string, report func(Progress)) error {
	if len(targets) == 0 {
		return nil
	}

	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Go(func() {
			if err := p.Push(ctx, target, pool, dataset, report); err != nil {
				errs[i] = fmt.Errorf("replicating %s to %s: %w", dataset, target, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
