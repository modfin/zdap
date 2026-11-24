package bases

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// fakeStopper answers a stop with the exit code docker would report for it.
type fakeStopper struct {
	exitCode int64
	stopErr  error
	waitErr  error

	timeout *int // the grace period the stop asked for
}

func (f *fakeStopper) ContainerStop(_ context.Context, _ string, opts client.ContainerStopOptions) (client.ContainerStopResult, error) {
	f.timeout = opts.Timeout
	return client.ContainerStopResult{}, f.stopErr
}

func (f *fakeStopper) ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult {
	res := make(chan container.WaitResponse, 1)
	errs := make(chan error, 1)
	if f.waitErr != nil {
		errs <- f.waitErr
	} else {
		res <- container.WaitResponse{StatusCode: f.exitCode}
	}
	return client.ContainerWaitResult{Result: res, Error: errs}
}

func TestStopBaseContainerAcceptsACleanShutdown(t *testing.T) {
	f := &fakeStopper{exitCode: 0}
	if err := stopBaseContainer(f, "id", "zdap-postgres-irma-v3-base-x"); err != nil {
		t.Fatal(err)
	}
	if f.timeout == nil || *f.timeout != 900 {
		t.Errorf("stop grace period = %v s, want 900: a shutdown checkpoint after a restore needs minutes, not docker's default", f.timeout)
	}
}

// Docker reports 137 when the grace period ran out and it sent SIGKILL. That
// is what happened to the 2026-11-24 irma-v3 base, and it must not be snapped.
func TestStopBaseContainerRefusesAKilledDatabase(t *testing.T) {
	for _, code := range []int64{137, 1} {
		f := &fakeStopper{exitCode: code}
		err := stopBaseContainer(f, "id", "zdap-postgres-irma-v3-base-x")
		if err == nil {
			t.Fatalf("exit code %d was accepted, want the base refused", code)
		}
		if !strings.Contains(err.Error(), "zdap-postgres-irma-v3-base-x") || !strings.Contains(err.Error(), "not snapshotted") {
			t.Errorf("error = %v, want it to name the base and say it was not snapshotted", err)
		}
	}
}

func TestStopBaseContainerReportsDockerErrors(t *testing.T) {
	for _, f := range []*fakeStopper{
		{stopErr: errors.New("no such container")},
		{waitErr: errors.New("connection reset")},
	} {
		if err := stopBaseContainer(f, "id", "b"); err == nil {
			t.Errorf("%+v: want the docker error, got nil", f)
		}
	}
}

// Destroying a clone kills it on purpose, so the exit code is returned for the
// caller to judge rather than turned into an error.
func TestStopContainerReturnsTheExitCodeWithoutJudgingIt(t *testing.T) {
	f := &fakeStopper{exitCode: 137}
	code, err := stopContainer(f, "id", 0)
	if err != nil {
		t.Fatal(err)
	}
	if code != 137 {
		t.Errorf("code = %d, want 137", code)
	}
}
