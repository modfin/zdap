package replication

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modfin/zdap/internal/config"
)

// streamBuffer sizes the buffer on both ends of a transfer.
//
// Without it the stream moves in 32 KB steps: io.Copy inside net/http uses a
// 32 KB buffer, so every chunked-encoding frame and the TLS records under it
// are sized by that. bufio.Reader implements io.WriterTo and io.NopCloser
// preserves it, so wrapping the source makes io.Copy hand off to WriteTo and
// write buffer-sized blocks instead. The first measured 117 GB transfer ran at
// 155 MB/s against pools that read at 325 and write at 278, so the loss was in
// the middle rather than at either end.
const streamBuffer = 4 << 20

// Client pushes finished bases to peers.
type Client struct {
	http        *http.Client
	defaultPort int

	// inFlight serialises pushes per (peer, dataset). Two sends of the same
	// base to the same peer at once fight over one `zfs recv`, and the loser
	// fails -- observed when a retry was issued while the previous attempt was
	// still unwinding. A scheduler retrying a slow transfer would hit exactly
	// this, so the second caller is refused rather than allowed to collide.
	mu       sync.Mutex
	inFlight map[string]struct{}
}

func NewClient(m *config.TLSMaterial, defaultPort int) *Client {
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{TLSClientConfig: m.ClientTLS()},
			// No Timeout on purpose: a trade base is 117 GB and takes hours.
			// Cancellation comes from the caller's context instead.
		},
		defaultPort: defaultPort,
		inFlight:    map[string]struct{}{},
	}
}

// begin claims a (peer, dataset) pair, reporting false if it is already taken.
func (c *Client) begin(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.inFlight[key]; busy {
		return false
	}
	c.inFlight[key] = struct{}{}
	return true
}

func (c *Client) end(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inFlight, key)
}

func (c *Client) endpoint(peer, path string) string {
	host := peer
	if _, _, err := net.SplitHostPort(peer); err != nil {
		host = net.JoinHostPort(peer, strconv.Itoa(c.defaultPort))
	}
	return "https://" + host + path
}

func basePath(dataset string) string {
	return "/replication/bases/" + url.PathEscape(dataset)
}

// Push sends a base and its snapshot to a peer.
//
// If the peer still holds a resume token for this dataset the transfer
// continues from there. `zfs send -t` takes no stream flags of its own — the
// token already encodes how the original stream was produced — which is why the
// two argument lists differ so much.
func (c *Client) Push(ctx context.Context, peer, pool, dataset string, report func(Progress)) error {
	if _, ok := ParseBaseName(dataset); !ok {
		return fmt.Errorf("refusing to send %q: not a base dataset name", dataset)
	}

	key := peer + "|" + dataset
	if !c.begin(key) {
		return fmt.Errorf("already replicating %s to %s", dataset, peer)
	}
	defer c.end(key)

	token, err := c.resumeToken(ctx, peer, dataset)
	if err != nil {
		return fmt.Errorf("could not ask %s for a resume token: %w", peer, err)
	}

	// -V  progress in the process title, for anyone watching with `ps`
	// -P  machine-readable header, which is where the total byte count comes
	//     from; neither -v nor -P emits periodic progress, so the bytes sent
	//     are counted in this process instead
	var args []string
	if token != "" {
		args = []string{"send", "-V", "-P", "-t", token}
	} else {
		// -L  large blocks, without which a recordsize above 128k cannot be sent
		// -c  send records in their on-disk compressed form, roughly halving the bytes
		// -p  carry dataset properties, including the zdap:* ones that tell the
		//     receiving node which resource a base belongs to
		args = []string{"send", "-L", "-c", "-p", "-V", "-P", Path(pool, SnapshotOf(dataset))}
	}
	return c.stream(ctx, peer, dataset, args, report)
}

func (c *Client) resumeToken(ctx context.Context, peer, dataset string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.endpoint(peer, basePath(dataset)+"/resume"), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("peer returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// stream runs zfs send and posts its stdout to the peer as the request body.
func (c *Client) stream(ctx context.Context, peer, dataset string, args []string, report func(Progress)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, "zfs", args...)
	stderr := &sizeFromSendHeader{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start zfs send: %w", err)
	}

	// Counter below the buffer, buffer above it: see countingReader and
	// streamBuffer for why the order matters.
	counted := &countingReader{r: stdout}
	buffered := bufio.NewReaderSize(counted, streamBuffer)

	if report != nil {
		started := time.Now()
		emit := func() {
			sent := counted.n.Load()
			elapsed := time.Since(started).Seconds()
			var rate int64
			if elapsed > 0 {
				rate = int64(float64(sent) / elapsed)
			}
			report(Progress{
				Peer: peer, Dataset: dataset,
				Sent: sent, Total: stderr.total.Load(),
				Rate: rate, Updated: time.Now(),
			})
		}
		defer reportEvery(time.Second, emit)()
		defer emit() // one final reading after the stream ends
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(peer, basePath(dataset)), buffered)
	if err != nil {
		cancel()
		_ = cmd.Wait()
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, doErr := c.http.Do(req)

	// Do has read stdout to EOF (or failed), so the pipe is finished with and
	// Wait is safe. Wait must not be called earlier: it closes the pipe.
	waitErr := cmd.Wait()
	if waitErr != nil {
		waitErr = fmt.Errorf("zfs send: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}

	if doErr != nil {
		// The send failing is usually a consequence of the post failing, so the
		// transport error is the one worth leading with.
		return errors.Join(fmt.Errorf("sending %s to %s: %w", dataset, peer, doErr), waitErr)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return errors.Join(fmt.Errorf("peer %s refused %s: %s: %s",
			peer, dataset, resp.Status, strings.TrimSpace(string(body))), waitErr)
	}
	return waitErr
}
