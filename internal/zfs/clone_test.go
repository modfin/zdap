package zfs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// cloneStub answers the three commands a clone runs: the property read on the
// origin, the clone itself, and the property read back off the new dataset.
type cloneStub struct {
	originProps string // stdout for the `zfs get` on the origin
	cloneState  string // stdout for the `zfs get` on the new clone
	cloneErr    error
	cloneStderr string

	cloneArgs []string // the argv the clone was run with
	gets      []string // the dataset each `zfs get` asked about
}

func (s *cloneStub) install(t *testing.T) {
	t.Helper()
	if s.originProps == "" {
		s.originProps = "zdap:resource\tpostgres-trade\nzdap:created_at\t2026-11-14T11.22.12\n"
	}
	if s.cloneState == "" {
		s.cloneState = "mounted\tyes\nmountpoint\t/zdap/1/databases/the-clone\n"
	}
	prev := execZfs
	execZfs = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		switch args[0] {
		case "get":
			s.gets = append(s.gets, args[len(args)-1])
			if strings.HasPrefix(args[4], "mounted") {
				return []byte(s.cloneState), nil, nil
			}
			return []byte(s.originProps), nil, nil
		case "clone":
			s.cloneArgs = args
			return nil, []byte(s.cloneStderr), s.cloneErr
		}
		t.Errorf("unexpected zfs call %q", args)
		return nil, nil, nil
	}
	t.Cleanup(func() { execZfs = prev })
}

// opt returns the value of a -o name=value argument, and whether it was there.
func opt(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			if v, ok := strings.CutPrefix(args[i+1], name+"="); ok {
				return v, true
			}
		}
	}
	return "", false
}

func TestCloneRefusesWhatIsNotAZdapSnapshot(t *testing.T) {
	for _, name := range []string{
		"",
		testBase,              // a base, not a snapshot of one
		"scratch@snap",        // not a zdap dataset
		testBase + "@nightly", // zdap only ever takes @snap
		testClone,
	} {
		t.Run(name, func(t *testing.T) {
			args := stubZfs(t, "", "", nil)

			_, _, err := NewZFS("zdap/1/databases").CloneDataset("alice", name, 5432, false, nil)
			if err == nil {
				t.Fatalf("CloneDataset(%q) succeeded, want a refusal", name)
			}
			if len(*args) != 0 {
				t.Errorf("zfs was run with %q for a snapshot name that should have been refused", *args)
			}
		})
	}
}

func TestCloneSetsEveryPropertyAtCreation(t *testing.T) {
	s := &cloneStub{}
	s.install(t)

	name, path, err := NewZFS("zdap/1/databases").CloneDataset(
		"alice", testSnap, 36043, true, map[string]string{"recordsize": "16k"})
	if err != nil {
		t.Fatal(err)
	}

	// Every property the old path wrote one call at a time after the clone
	// existed is now part of creating it, so a clone is never visible without
	// them.
	for _, tc := range []struct{ prop, want string }{
		{PropOwner, "alice"},
		{PropResource, "postgres-trade"},
		{PropSnappedAt, "2026-11-14T11.22.12"},
		{PropClonePooled, "true"},
		{PropPort, "36043"},
		{"recordsize", "16k"},
	} {
		got, ok := opt(s.cloneArgs, tc.prop)
		if !ok {
			t.Errorf("%s was not set on the clone: %q", tc.prop, s.cloneArgs)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.prop, got, tc.want)
		}
	}
	if _, ok := opt(s.cloneArgs, PropCreated); !ok {
		t.Errorf("%s was not set on the clone: %q", PropCreated, s.cloneArgs)
	}

	// Source then destination, both pool qualified.
	if n := len(s.cloneArgs); s.cloneArgs[n-2] != "zdap/1/databases/"+testSnap {
		t.Errorf("cloned from %q, want the snapshot", s.cloneArgs[n-2])
	}
	if n := len(s.cloneArgs); s.cloneArgs[n-1] != "zdap/1/databases/"+name {
		t.Errorf("cloned to %q, want %q", s.cloneArgs[n-1], "zdap/1/databases/"+name)
	}

	if !IsCloneName(name) {
		t.Errorf("clone name %q is not one a Listing would recognise", name)
	}
	if path != "/zdap/1/databases/the-clone" {
		t.Errorf("path = %q, want the mountpoint zfs reported", path)
	}

	// The origin is read once, for the two properties that come off it.
	if len(s.gets) != 2 || s.gets[0] != "zdap/1/databases/"+testBase {
		t.Errorf("gets = %q, want the origin then the clone", s.gets)
	}
}

// A resource file must not be able to set a zdap:* property and so lie about
// its own clone's owner, port or expiry.
func TestCloneIgnoresZdapPropertiesFromTheResourceFile(t *testing.T) {
	s := &cloneStub{}
	s.install(t)

	_, _, err := NewZFS("zdap/1/databases").CloneDataset("alice", testSnap, 5432, false,
		map[string]string{"zdap:owner": "mallory", "recordsize": "16k"})
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := opt(s.cloneArgs, PropOwner); got != "alice" {
		t.Errorf("%s = %q, want the owner the caller asked for", PropOwner, got)
	}
	if strings.Contains(strings.Join(s.cloneArgs, " "), "mallory") {
		t.Errorf("a resource file property reached the clone: %q", s.cloneArgs)
	}
}

// zfs prints "-" for a property that is not set. Writing that through made a
// missing zdap:resource into a literal dash on the clone.
func TestCloneDoesNotCopyAnUnsetOriginProperty(t *testing.T) {
	s := &cloneStub{originProps: "zdap:resource\t-\nzdap:created_at\t-\n"}
	s.install(t)

	if _, _, err := NewZFS("zdap/1/databases").CloneDataset("alice", testSnap, 5432, false, nil); err != nil {
		t.Fatal(err)
	}

	for _, prop := range []string{PropResource, PropSnappedAt} {
		if v, ok := opt(s.cloneArgs, prop); ok {
			t.Errorf("%s was set to %q from an unset origin property", prop, v)
		}
	}
}

// The mount is what the database container binds, so an unmounted clone has to
// fail here rather than at container start.
func TestCloneFailsWhenTheNewDatasetIsNotMounted(t *testing.T) {
	s := &cloneStub{cloneState: "mounted\tno\nmountpoint\t-\n"}
	s.install(t)

	_, _, err := NewZFS("zdap/1/databases").CloneDataset("alice", testSnap, 5432, false, nil)
	if err == nil {
		t.Fatal("want an error for a clone that is not mounted")
	}
	if !strings.Contains(err.Error(), "not mounted") {
		t.Errorf("error = %v, want it to say the clone is not mounted", err)
	}
}

func TestCloneReportsWhatZfsSaid(t *testing.T) {
	s := &cloneStub{cloneErr: errors.New("exit status 1"), cloneStderr: "cannot create 'x': out of space"}
	s.install(t)

	_, _, err := NewZFS("zdap/1/databases").CloneDataset("alice", testSnap, 5432, false, nil)
	if err == nil {
		t.Fatal("want an error when zfs clone fails")
	}
	if !strings.Contains(err.Error(), "out of space") {
		t.Errorf("error = %v, want it to carry what zfs printed", err)
	}
}

func TestPropArgsAreSortedSoTheCommandIsStable(t *testing.T) {
	got := propArgs(map[string]string{"b": "2", "a": "1", "c": "3"})
	want := []string{"-o", "a=1", "-o", "b=2", "-o", "c=3"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("propArgs = %q, want %q", got, want)
	}
}
