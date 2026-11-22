package replication

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Rejected means a request was refused before any zfs command ran, because the
// dataset name did not parse or named a resource this node does not serve.
// Callers map it to a 4xx; anything else is a 5xx.
type Rejected struct{ Reason string }

func (e *Rejected) Error() string { return e.Reason }

// Receiver accepts base datasets pushed by peers.
type Receiver struct {
	pool string
	// serves reports whether this node is configured for a resource. A peer
	// certificate proves who is pushing, not what: without this check any peer
	// could create arbitrary datasets in the pool.
	serves func(resource string) bool
}

func NewReceiver(pool string, serves func(resource string) bool) *Receiver {
	return &Receiver{pool: pool, serves: serves}
}

// authorize is the only gate between a peer and `zfs recv`.
func (r *Receiver) authorize(dataset string) error {
	resource, ok := ParseBaseName(dataset)
	if !ok {
		return &Rejected{Reason: fmt.Sprintf("%q is not a base dataset name", dataset)}
	}
	if !r.serves(resource) {
		return &Rejected{Reason: fmt.Sprintf("resource %q is not configured on this node", resource)}
	}
	return nil
}

// Recv streams a zfs send into the pool. -s keeps a resume token on failure, so
// an interrupted transfer of a large base continues instead of restarting.
//
// Receiving a base this node already holds is a success, not an error. A
// transient failure after the data landed -- a recovered panic, a dropped
// response -- leaves the sender believing the push failed, and because a
// complete dataset has no resume token the next attempt is a full send. Without
// this check that send would hit "destination already exists" and keep failing
// for as long as the base lives.
func (r *Receiver) Recv(ctx context.Context, dataset string, body io.Reader) error {
	if err := r.authorize(dataset); err != nil {
		return err
	}
	target := Path(r.pool, dataset)

	complete, err := r.alreadyComplete(ctx, dataset)
	if err != nil {
		return err
	}
	if complete {
		// Drain so the sender sees the stream consumed rather than a reset.
		_, _ = io.Copy(io.Discard, body)
		return nil
	}

	cmd := exec.CommandContext(ctx, "zfs", "recv", "-s", target)
	// exec copies a non-*os.File stdin with its own 32 KB io.Copy; buffering
	// here gives that the WriteTo path instead. See streamBuffer.
	cmd.Stdin = bufio.NewReaderSize(body, streamBuffer)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("zfs recv %s: %w: %s", target, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ResumeToken returns the token for a partially received dataset, or "" when
// there is nothing to resume.
func (r *Receiver) ResumeToken(ctx context.Context, dataset string) (string, error) {
	if err := r.authorize(dataset); err != nil {
		return "", err
	}
	target := Path(r.pool, dataset)

	cmd := exec.CommandContext(ctx, "zfs", "get", "-H", "-o", "value", "receive_resume_token", target)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// A dataset that does not exist yet is the normal first-send case, not
		// an error: there is simply nothing to resume.
		if strings.Contains(stderr.String(), "does not exist") {
			return "", nil
		}
		return "", fmt.Errorf("zfs get receive_resume_token %s: %w: %s", target, err, strings.TrimSpace(stderr.String()))
	}
	token := strings.TrimSpace(stdout.String())
	if token == "-" {
		return "", nil
	}
	return token, nil
}

// alreadyComplete reports whether the dataset is present and not mid-transfer.
// A dataset holding a resume token is a partial receive and must be resumed,
// not skipped.
func (r *Receiver) alreadyComplete(ctx context.Context, dataset string) (bool, error) {
	target := Path(r.pool, dataset)

	cmd := exec.CommandContext(ctx, "zfs", "list", "-H", "-o", "name", target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "does not exist") {
			return false, nil
		}
		return false, fmt.Errorf("zfs list %s: %w: %s", target, err, strings.TrimSpace(stderr.String()))
	}

	token, err := r.ResumeToken(ctx, dataset)
	if err != nil {
		return false, err
	}
	return token == "", nil
}
