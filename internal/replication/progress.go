package replication

import (
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Progress is a point-in-time view of one transfer.
//
// Total comes from the "size" line that `zfs send -P` prints before any data
// moves. Sent is counted in this process rather than parsed from zfs: contrary
// to what the flag names suggest, `zfs send -v` emits only the estimate and no
// periodic progress -- that is what -V does, and it writes to the process title
// rather than to stderr. Since the stream passes through our own pipe anyway,
// counting it here is both simpler and exact.
type Progress struct {
	Peer    string `json:"peer"`
	Dataset string `json:"dataset"`
	Sent    int64  `json:"sent"`
	// Total is what `zfs send -P` estimated before the stream started, not a
	// measured figure: the 117 GB trade base reported 127,167,190,064 and
	// actually sent 127,882,641,408, 0.56% more. Named for what it is so a
	// caller does not treat it as exact.
	Total   int64     `json:"total_estimated,omitempty"`
	Rate    int64     `json:"rate_bytes_per_sec"`
	Updated time.Time `json:"updated"`
}

// Percent is -1 when the total is unknown, and never exceeds 100.
//
// Because Total is an estimate, a finished transfer can report slightly more
// bytes than were predicted; a real run reached 100.5%. Clamping keeps the
// field meaningful for anything that drives a progress bar or a timeout off it.
func (p Progress) Percent() float64 {
	if p.Total <= 0 {
		return -1
	}
	return min(float64(p.Sent)/float64(p.Total)*100, 100)
}

// countingReader counts bytes passing through.
//
// It sits BELOW the bufio.Reader on purpose. Wrapping the other way round would
// hide bufio's io.WriterTo from io.Copy and silently drop the stream back to
// 32 KB writes, undoing the buffering it is measuring.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// sizeFromSendHeader scans `zfs send -P` output for the total byte count.
//
//	full<TAB>pool/ds@snap<TAB>127167190064
//	size<TAB>127167190064
type sizeFromSendHeader struct {
	mu    sync.Mutex
	tail  strings.Builder
	total atomic.Int64
	rest  []byte
}

func (s *sizeFromSendHeader) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Keep stderr for error reporting, bounded so a chatty failure cannot grow
	// without limit.
	if s.tail.Len() < 8192 {
		s.tail.Write(p)
	}

	s.rest = append(s.rest, p...)
	for {
		i := strings.IndexByte(string(s.rest), '\n')
		if i < 0 {
			break
		}
		line := string(s.rest[:i])
		s.rest = s.rest[i+1:]
		if after, ok := strings.CutPrefix(line, "size\t"); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(after), 10, 64); err == nil {
				s.total.Store(n)
			}
		}
	}
	return len(p), nil
}

func (s *sizeFromSendHeader) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tail.String()
}

// reportEvery ticks a progress callback until the returned stop is called.
func reportEvery(d time.Duration, fn func()) (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fn()
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
