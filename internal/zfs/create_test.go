package zfs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// writeStub answers a create or snapshot run and the property read that
// follows a create.
type writeStub struct {
	state  string // stdout for the `zfs get` after a create
	err    error
	stderr string

	args []string // the argv of the create or snapshot run
}

func (s *writeStub) install(t *testing.T) {
	t.Helper()
	if s.state == "" {
		s.state = "mounted\tyes\nmountpoint\t/zdap/1/databases/the-base\n"
	}
	prev := execZfs
	execZfs = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		switch args[0] {
		case "get":
			return []byte(s.state), nil, nil
		case "create", "snapshot":
			s.args = args
			return nil, []byte(s.stderr), s.err
		}
		t.Errorf("unexpected zfs call %q", args)
		return nil, nil, nil
	}
	t.Cleanup(func() { execZfs = prev })
}

var testCreation = time.Date(2026, 11, 14, 11, 22, 12, 0, time.UTC)

func TestCreateSetsPropertiesAtCreation(t *testing.T) {
	s := &writeStub{}
	s.install(t)

	path, err := NewZFS("zdap/1/databases").CreateDataset(
		testBase, "postgres-trade", testCreation, Runtime{}, map[string]string{"recordsize": "64k"})
	if err != nil {
		t.Fatal(err)
	}

	// Written as part of creating the dataset, so a listing can never see a
	// base without the resource it belongs to.
	for _, tc := range []struct{ prop, want string }{
		{PropResource, "postgres-trade"},
		{PropCreated, "2026-11-14T11.22.12"},
		{"recordsize", "64k"},
	} {
		got, ok := opt(s.args, tc.prop)
		if !ok {
			t.Errorf("%s was not set: %q", tc.prop, s.args)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.prop, got, tc.want)
		}
	}

	if last := s.args[len(s.args)-1]; last != "zdap/1/databases/"+testBase {
		t.Errorf("created %q, want the pool qualified base", last)
	}
	if path != "/zdap/1/databases/the-base" {
		t.Errorf("path = %q, want the mountpoint zfs reported", path)
	}
}

func TestCreateAndSnapshotRefuseNamesThatAreNotBases(t *testing.T) {
	for _, name := range []string{"", "scratch", testSnap, testClone, "-o"} {
		t.Run(name, func(t *testing.T) {
			args := stubZfs(t, "", "", nil)
			z := NewZFS("zdap/1/databases")

			if _, err := z.CreateDataset(name, "r", testCreation, Runtime{}, nil); err == nil {
				t.Errorf("CreateDataset(%q) succeeded, want a refusal", name)
			}
			if err := z.SnapDataset(name, "r", testCreation); err == nil {
				t.Errorf("SnapDataset(%q) succeeded, want a refusal", name)
			}
			if len(*args) != 0 {
				t.Errorf("zfs was run with %q for a name that should have been refused", *args)
			}
		})
	}
}

// A base whose mount is missing would fail later, when the restore container
// binds it, with nothing pointing back here.
func TestCreateFailsWhenTheDatasetIsNotMounted(t *testing.T) {
	s := &writeStub{state: "mounted\tno\nmountpoint\t-\n"}
	s.install(t)

	_, err := NewZFS("zdap/1/databases").CreateDataset(testBase, "postgres-trade", testCreation, Runtime{}, nil)
	if err == nil {
		t.Fatal("want an error for a dataset that is not mounted")
	}
	if !strings.Contains(err.Error(), "not mounted") {
		t.Errorf("error = %v, want it to say the dataset is not mounted", err)
	}
}

func TestCreateReportsWhatZfsSaid(t *testing.T) {
	s := &writeStub{err: errors.New("exit status 1"), stderr: "cannot create: out of space"}
	s.install(t)

	_, err := NewZFS("zdap/1/databases").CreateDataset(testBase, "postgres-trade", testCreation, Runtime{}, nil)
	if err == nil {
		t.Fatal("want an error when zfs create fails")
	}
	if !strings.Contains(err.Error(), "out of space") {
		t.Errorf("error = %v, want it to carry what zfs printed", err)
	}
}

func TestResourcePropertiesCannotOverrideZdapPropsOnCreate(t *testing.T) {
	s := &writeStub{}
	s.install(t)

	_, err := NewZFS("zdap/1/databases").CreateDataset(testBase, "postgres-trade", testCreation, Runtime{},
		map[string]string{"zdap:resource": "something-else"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := opt(s.args, PropResource); got != "postgres-trade" {
		t.Errorf("%s = %q, want the resource the caller named", PropResource, got)
	}
}

func TestSnapshotCarriesTheBaseProperties(t *testing.T) {
	s := &writeStub{}
	s.install(t)

	if err := NewZFS("zdap/1/databases").SnapDataset(testBase, "postgres-trade", testCreation); err != nil {
		t.Fatal(err)
	}

	if s.args[0] != "snapshot" {
		t.Fatalf("ran %q, want a snapshot", s.args)
	}
	// CloneDataset copies these onto a clone, and a Listing reports them for
	// the snapshot itself.
	if got, _ := opt(s.args, PropResource); got != "postgres-trade" {
		t.Errorf("%s = %q", PropResource, got)
	}
	if got, _ := opt(s.args, PropCreated); got != "2026-11-14T11.22.12" {
		t.Errorf("%s = %q", PropCreated, got)
	}
	if last := s.args[len(s.args)-1]; last != "zdap/1/databases/"+testBase+"@snap" {
		t.Errorf("snapshotted %q, want the @snap of the base", last)
	}
	if !IsSnapName(testBase + "@snap") {
		t.Error("the name this writes is not one a Listing would recognise")
	}
}

func TestSnapshotReportsWhatZfsSaid(t *testing.T) {
	s := &writeStub{err: errors.New("exit status 1"), stderr: "dataset already exists"}
	s.install(t)

	err := NewZFS("zdap/1/databases").SnapDataset(testBase, "postgres-trade", testCreation)
	if err == nil {
		t.Fatal("want an error when zfs snapshot fails")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to carry what zfs printed", err)
	}
}

// The image and volume a base is restored with are written with it, so a clone
// of it can still find them after the resource file has moved on.
func TestCreateRecordsTheRuntime(t *testing.T) {
	s := &writeStub{}
	s.install(t)

	rt := Runtime{Image: "timescale/timescaledb:2.12.0-pg15", Volume: "/var/lib/postgresql/data"}
	if _, err := NewZFS("zdap/1/databases").CreateDataset(testBase, "postgres-jamboree", testCreation, rt, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := opt(s.args, PropImage); got != rt.Image {
		t.Errorf("%s = %q, want %q", PropImage, got, rt.Image)
	}
	if got, _ := opt(s.args, PropVolume); got != rt.Volume {
		t.Errorf("%s = %q, want %q", PropVolume, got, rt.Volume)
	}
}

// An unknown runtime is left unset rather than written as an empty string, so
// that a clone falls back to the resource file instead of to "".
func TestCreateLeavesAnUnknownRuntimeUnset(t *testing.T) {
	s := &writeStub{}
	s.install(t)

	if _, err := NewZFS("zdap/1/databases").CreateDataset(testBase, "r", testCreation, Runtime{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, prop := range []string{PropImage, PropVolume} {
		if v, ok := opt(s.args, prop); ok {
			t.Errorf("%s was set to %q for an unknown runtime", prop, v)
		}
	}
}

// The resource file's zfs properties must not be able to claim another image.
func TestResourcePropertiesCannotOverrideTheRuntime(t *testing.T) {
	s := &writeStub{}
	s.install(t)

	_, err := NewZFS("zdap/1/databases").CreateDataset(testBase, "r", testCreation,
		Runtime{Image: "postgres:15", Volume: "/data"},
		map[string]string{PropImage: "evil:latest", PropVolume: "/elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := opt(s.args, PropImage); got != "postgres:15" {
		t.Errorf("%s = %q, want the image the restore ran", PropImage, got)
	}
	if got, _ := opt(s.args, PropVolume); got != "/data" {
		t.Errorf("%s = %q, want the volume the restore used", PropVolume, got)
	}
}
