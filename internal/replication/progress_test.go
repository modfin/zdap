package replication

import (
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The total comes from `zfs send -P`, which prints a tab-separated header
// before any data moves. Neither -v nor -P emits periodic progress, so this is
// the only thing worth parsing out of stderr.
func Test_sizeFromSendHeader_readsTotal(t *testing.T) {
	s := &sizeFromSendHeader{}
	_, _ = io.WriteString(s, "full\tzdap/databases/zdap-x-base-2026-01-01T00.00.00@snap\t127167190064\n")
	_, _ = io.WriteString(s, "size\t127167190064\n")

	assert.Equal(t, int64(127167190064), s.total.Load())
	assert.Contains(t, s.String(), "size", "stderr is still kept for error reporting")
}

// Writes do not arrive aligned to lines.
func Test_sizeFromSendHeader_handlesSplitWrites(t *testing.T) {
	s := &sizeFromSendHeader{}
	for _, chunk := range []string{"full\tds@snap\t12", "3\nsi", "ze\t4", "56\n"} {
		_, _ = io.WriteString(s, chunk)
	}
	assert.Equal(t, int64(456), s.total.Load())
}

func Test_sizeFromSendHeader_ignoresHumanFormat(t *testing.T) {
	s := &sizeFromSendHeader{}
	// Without -P, zfs prints "estimated size is 118G" -- unparseable, and must
	// leave the total at zero rather than guessing.
	_, _ = io.WriteString(s, "full send of ds@snap estimated size is 118G\ntotal estimated size is 118G\n")
	assert.Equal(t, int64(0), s.total.Load())
}

func Test_sizeFromSendHeader_tailIsBounded(t *testing.T) {
	s := &sizeFromSendHeader{}
	for range 1000 {
		_, _ = io.WriteString(s, strings.Repeat("noise ", 100)+"\n")
	}
	assert.Less(t, len(s.String()), 16384, "a chatty failure must not grow without limit")
}

func Test_countingReader_counts(t *testing.T) {
	c := &countingReader{r: strings.NewReader(strings.Repeat("x", 5000))}
	n, err := io.Copy(io.Discard, c)
	assert.NoError(t, err)
	assert.Equal(t, int64(5000), n)
	assert.Equal(t, int64(5000), c.n.Load())
}

// The real pattern: one goroutine streams while the progress ticker samples the
// counter. Concurrent Reads are not a case that happens, and testing them would
// only prove that strings.Reader is not thread-safe.
func Test_countingReader_safeToSampleWhileReading(t *testing.T) {
	c := &countingReader{r: strings.NewReader(strings.Repeat("x", 100000))}

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Go(func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, c)
	})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					_ = c.n.Load()
				}
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int64(100000), c.n.Load())
}

func Test_Progress_Percent(t *testing.T) {
	assert.InDelta(t, 50.0, Progress{Sent: 50, Total: 100}.Percent(), 0.001)
	assert.Equal(t, -1.0, Progress{Sent: 50}.Percent(), "unknown total reports -1, not a divide by zero")

	// Total is an estimate from `zfs send -P`; a real 117 GB run overshot it by
	// 0.56% and reported 100.5%.
	assert.Equal(t, 100.0, Progress{Sent: 127882641408, Total: 127167190064}.Percent(),
		"an overshooting estimate must not report more than 100%")
}
