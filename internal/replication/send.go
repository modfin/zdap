package replication

import (
	"bytes"
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

	"github.com/modfin/zdap/internal/config"
)

// Client pushes finished bases to peers.
type Client struct {
	http        *http.Client
	defaultPort int
}

func NewClient(m *config.TLSMaterial, defaultPort int) *Client {
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{TLSClientConfig: m.ClientTLS()},
			// No Timeout on purpose: a trade base is 117 GB and takes hours.
			// Cancellation comes from the caller's context instead.
		},
		defaultPort: defaultPort,
	}
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
func (c *Client) Push(ctx context.Context, peer, pool, dataset string) error {
	if _, ok := ParseBaseName(dataset); !ok {
		return fmt.Errorf("refusing to send %q: not a base dataset name", dataset)
	}

	token, err := c.resumeToken(ctx, peer, dataset)
	if err != nil {
		return fmt.Errorf("could not ask %s for a resume token: %w", peer, err)
	}

	var args []string
	if token != "" {
		args = []string{"send", "-t", token}
	} else {
		// -L  large blocks, without which a recordsize above 128k cannot be sent
		// -c  send records in their on-disk compressed form, roughly halving the bytes
		// -p  carry dataset properties, including the zdap:* ones that tell the
		//     receiving node which resource a base belongs to
		args = []string{"send", "-L", "-c", "-p", Path(pool, SnapshotOf(dataset))}
	}
	return c.stream(ctx, peer, dataset, args)
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
func (c *Client) stream(ctx context.Context, peer, dataset string, args []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start zfs send: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(peer, basePath(dataset)), stdout)
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
