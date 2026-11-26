package bases

import (
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

type fakePull struct {
	io.ReadCloser
	waitErr error
}

func (p fakePull) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}
func (p fakePull) Wait(context.Context) error { return p.waitErr }

type fakeImages struct {
	inspectErr error
	pullErr    error
	waitErr    error

	pulled []string
}

func (f *fakeImages) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{}, f.inspectErr
}

func (f *fakeImages) ImagePull(_ context.Context, ref string, _ client.ImagePullOptions) (client.ImagePullResponse, error) {
	f.pulled = append(f.pulled, ref)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	return fakePull{ReadCloser: io.NopCloser(strings.NewReader("")), waitErr: f.waitErr}, nil
}

func TestEnsureImageLeavesAPresentImageAlone(t *testing.T) {
	f := &fakeImages{}
	if err := EnsureImage(context.Background(), f, "postgres:15"); err != nil {
		t.Fatal(err)
	}
	if len(f.pulled) != 0 {
		t.Errorf("pulled %q, want no pull for an image the host has", f.pulled)
	}
}

func TestEnsureImagePullsAMissingImage(t *testing.T) {
	f := &fakeImages{inspectErr: cerrdefs.ErrNotFound.WithMessage("No such image: postgres:15")}
	if err := EnsureImage(context.Background(), f, "postgres:15"); err != nil {
		t.Fatal(err)
	}
	if len(f.pulled) != 1 || f.pulled[0] != "postgres:15" {
		t.Errorf("pulled %q, want postgres:15 once", f.pulled)
	}
}

// A denied pull or a missing tag still answers 200 and reports the failure in
// the stream; that has to fail the job, not let it run into ContainerCreate.
func TestEnsureImageFailsOnAnErrorInThePullStream(t *testing.T) {
	f := &fakeImages{
		inspectErr: cerrdefs.ErrNotFound,
		waitErr:    errors.New("manifest for postgres:99 not found"),
	}
	err := EnsureImage(context.Background(), f, "postgres:99")
	if err == nil || !strings.Contains(err.Error(), "manifest for postgres:99 not found") {
		t.Errorf("err = %v, want the pull stream's error", err)
	}
}

// Only a missing image is a reason to pull. Anything else, such as the daemon
// being unreachable, is reported as it is.
func TestEnsureImageDoesNotPullOnOtherInspectErrors(t *testing.T) {
	f := &fakeImages{inspectErr: errors.New("Cannot connect to the Docker daemon")}
	err := EnsureImage(context.Background(), f, "postgres:15")
	if err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Errorf("err = %v, want the inspect error", err)
	}
	if len(f.pulled) != 0 {
		t.Errorf("pulled %q after a non not-found error", f.pulled)
	}
}

func TestEnsureImageRefusesAnEmptyImage(t *testing.T) {
	f := &fakeImages{}
	if err := EnsureImage(context.Background(), f, ""); err == nil {
		t.Error("want an error for an empty image name")
	}
}
