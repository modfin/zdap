package zfs

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// stubZfs replaces the zfs command for the duration of a test and records the
// arguments it was called with.
func stubZfs(t *testing.T, stdout, stderr string, err error) *[]string {
	t.Helper()

	var got []string
	prev := execZfs
	execZfs = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		got = args
		return []byte(stdout), []byte(stderr), err
	}
	t.Cleanup(func() { execZfs = prev })
	return &got
}

func TestPoolName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"zdap/1/databases", "zdap"},
		{"zdap", "zdap"},
		{"tank/x", "tank"},
	} {
		if got := poolName(tc.in); got != tc.want {
			t.Errorf("poolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestListNamesStripsThePoolPrefix(t *testing.T) {
	args := stubZfs(t, `{"return": {
		"zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49": {"name": "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49"},
		"zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49@snap": {"name": "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49@snap"},
		"zdap/1/databases": {"name": "zdap/1/databases"},
		"zdap/somewhere-else": {"name": "zdap/somewhere-else"}
	}}`, "", nil)

	z := NewZFS("zdap/1/databases")
	lst, err := z.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	names := lst.Names()
	want := []string{
		"zdap-postgres-boris-base-2026-11-14T11.23.49",
		"zdap-postgres-boris-base-2026-11-14T11.23.49@snap",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}

	// The program runs against the pool, read only, and is handed the dataset
	// path to list below.
	a := *args
	if a[0] != "program" || a[1] != "-jn" {
		t.Errorf("args = %q, want a read-only json program run", a)
	}
	if a[len(a)-1] != "zdap/1/databases" || a[len(a)-2] != "--" {
		t.Errorf("args = %q, want the dataset path passed after --", a)
	}
	if a[len(a)-4] != "zdap" {
		t.Errorf("args = %q, want the program run against pool zdap", a)
	}
}

func TestListNamesRemovesTheProgramFile(t *testing.T) {
	var programFile string
	prev := execZfs
	execZfs = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		// The file is the argument just before the -- separator.
		for i, a := range args {
			if a == "--" {
				programFile = args[i-1]
			}
		}
		b, err := os.ReadFile(programFile)
		if err != nil {
			t.Errorf("program file is not readable while zfs runs: %v", err)
		}
		if !strings.Contains(string(b), "zfs.list.children") {
			t.Errorf("program file does not hold the channel program: %q", b)
		}
		return []byte(`{"return": {}}`), nil, nil
	}
	t.Cleanup(func() { execZfs = prev })

	if _, err := NewZFS("zdap/1/databases").Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(programFile); !os.IsNotExist(err) {
		t.Errorf("program file %s was left behind", programFile)
	}
}

func TestChannelProgramReportsProgramErrors(t *testing.T) {
	stubZfs(t, `{"error": "dataset does not exist"}`, "", errors.New("exit status 1"))

	_, err := NewZFS("zdap/1/databases").Load(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "dataset does not exist") {
		t.Errorf("error = %v, want it to carry the program's message", err)
	}
}

func TestChannelProgramReportsPlainTextFailures(t *testing.T) {
	// How zfs reports a non-root caller: nothing on stdout, plain text on
	// stderr. This is the failure a developer is most likely to hit.
	stubZfs(t, "", "Channel program execution failed:\nPermission denied. Channel programs must be run as root.\n", errors.New("exit status 1"))

	_, err := NewZFS("zdap/1/databases").Load(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "must be run as root") {
		t.Errorf("error = %v, want it to carry the stderr message", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error = %q, want it on one line", err)
	}
}

func TestChannelProgramReportsUndecodableOutput(t *testing.T) {
	stubZfs(t, "not json at all", "", nil)

	_, err := NewZFS("zdap/1/databases").Load(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "not json at all") {
		t.Errorf("error = %v, want it to show what could not be decoded", err)
	}
}

func TestNameMatchers(t *testing.T) {
	const (
		base  = "zdap-postgres-boris-base-2026-11-14T11.23.49"
		snap  = "zdap-postgres-boris-base-2026-11-14T11.23.49@snap"
		clone = "zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.01.02.abc"
	)

	for _, tc := range []struct {
		name                    string
		isBase, isSnap, isClone bool
	}{
		{base, true, false, false},
		{snap, false, true, false},
		{clone, false, false, true},
		{"zdap/1/databases", false, false, false},
		{"something-else", false, false, false},
	} {
		if got := IsBaseName(tc.name); got != tc.isBase {
			t.Errorf("IsBaseName(%q) = %v, want %v", tc.name, got, tc.isBase)
		}
		if got := IsSnapName(tc.name); got != tc.isSnap {
			t.Errorf("IsSnapName(%q) = %v, want %v", tc.name, got, tc.isSnap)
		}
		if got := IsCloneName(tc.name); got != tc.isClone {
			t.Errorf("IsCloneName(%q) = %v, want %v", tc.name, got, tc.isClone)
		}
	}
}
