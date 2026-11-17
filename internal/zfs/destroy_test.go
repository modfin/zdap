package zfs

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	testBase    = "zdap-postgres-boris-base-2026-11-14T11.23.49"
	testSnap    = testBase + "@snap"
	testClone   = testBase + "-clone-2026-11-15T08.00.00.abc"
	testForeign = "scratch"
)

// scriptedZfs replaces the zfs command and answers `zfs program` runs from a
// queue of listings, while recording every other invocation. It is enough to
// drive DestroyAll, which lists, destroys, and lists again.
type scriptedZfs struct {
	listings []string // JSON return values, one per `zfs program` run
	lists    int
	calls    [][]string // every non-program invocation, in order
	failOn   string     // dataset path whose destroy should fail
}

func (s *scriptedZfs) install(t *testing.T) {
	t.Helper()
	prev := execZfs
	execZfs = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		if len(args) > 0 && args[0] == "program" {
			if s.lists >= len(s.listings) {
				t.Errorf("zfs program run %d times, only %d listings scripted", s.lists+1, len(s.listings))
				return []byte(`{"return": {}}`), nil, nil
			}
			out := s.listings[s.lists]
			s.lists++
			return []byte(out), nil, nil
		}
		s.calls = append(s.calls, args)
		if s.failOn != "" && args[len(args)-1] == s.failOn {
			return nil, []byte("cannot destroy: dataset is busy"), errors.New("exit status 1")
		}
		return nil, nil, nil
	}
	t.Cleanup(func() { execZfs = prev })
}

// destroyed returns the dataset path of each destroy call, in order.
func (s *scriptedZfs) destroyed() []string {
	var out []string
	for _, c := range s.calls {
		if len(c) > 0 && c[0] == "destroy" {
			out = append(out, c[len(c)-1])
		}
	}
	return out
}

func listing(names ...string) string {
	var b strings.Builder
	b.WriteString(`{"return": {`)
	for i, n := range names {
		if i > 0 {
			b.WriteString(",")
		}
		full := "zdap/1/databases/" + n
		b.WriteString(`"` + full + `": {"name": "` + full + `"}`)
	}
	b.WriteString("}}")
	return b.String()
}

// -R follows dependents out of the target hierarchy, so a name zdap did not
// generate must never reach zfs(8).
func TestDestroyRefusesNamesZdapDidNotCreate(t *testing.T) {
	for _, name := range []string{
		"",
		testForeign,
		"zdap/1/databases",         // the pool itself
		"-R",                       // would be read as a flag
		"../../tank/important",     // traversal
		"zdap-postgres-boris-base", // a prefix, without the timestamp
		testBase + "x",
	} {
		t.Run(name, func(t *testing.T) {
			args := stubZfs(t, "", "", nil)

			err := NewZFS("zdap/1/databases").Destroy(name)
			if err == nil {
				t.Fatalf("Destroy(%q) succeeded, want a refusal", name)
			}
			if !strings.Contains(err.Error(), "refusing to destroy") {
				t.Errorf("error = %v, want a refusal", err)
			}
			if len(*args) != 0 {
				t.Errorf("zfs was run with %q for a name that should have been refused", *args)
			}
		})
	}
}

func TestDestroyAcceptsEveryNameZdapGenerates(t *testing.T) {
	for _, name := range []string{testBase, testSnap, testClone} {
		t.Run(name, func(t *testing.T) {
			if err := checkDestroyTarget(name); err != nil {
				t.Errorf("checkDestroyTarget(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestDestroyIsRecursiveAndForced(t *testing.T) {
	args := stubZfs(t, "", "", nil)

	if err := NewZFS("zdap/1/databases").Destroy(testClone); err != nil {
		t.Fatal(err)
	}

	// -R is what replaces the hand-rolled Clones() recursion, -f the
	// UnmountAll. Losing either silently changes what a destroy does.
	want := []string{"destroy", "-R", "-f", "zdap/1/databases/" + testClone}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %q, want %q", *args, want)
	}
}

func TestDestroyReportsWhatZfsSaid(t *testing.T) {
	stubZfs(t, "", "cannot destroy 'zdap/1/databases/x': dataset is busy", errors.New("exit status 1"))

	err := NewZFS("zdap/1/databases").Destroy(testClone)
	if err == nil {
		t.Fatal("want an error when zfs destroy fails")
	}
	if !strings.Contains(err.Error(), "dataset is busy") {
		t.Errorf("error = %v, want it to carry what zfs printed", err)
	}
	if !strings.Contains(err.Error(), testClone) {
		t.Errorf("error = %v, want it to name the dataset", err)
	}
}

func TestDestroyAllTakesBasesThenLeftoverClones(t *testing.T) {
	s := &scriptedZfs{listings: []string{
		listing(testBase, testSnap, testClone, testForeign),
		// -R took the base, its snapshot and the clone made from it. An
		// unrelated clone and a dataset zdap did not create are left.
		listing("zdap-postgres-brex-base-2026-11-10T09.00.00-clone-2026-11-16T09.00.00.xyz", testForeign),
	}}
	s.install(t)

	if err := NewZFS("zdap/1/databases").DestroyAll(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"zdap/1/databases/" + testBase,
		"zdap/1/databases/zdap-postgres-brex-base-2026-11-10T09.00.00-clone-2026-11-16T09.00.00.xyz",
	}
	if got := s.destroyed(); !reflect.DeepEqual(got, want) {
		t.Errorf("destroyed %q, want %q", got, want)
	}
}

// DestroyAll used to destroy every child of the pool. A dataset someone else
// put there is not zdap's to remove.
func TestDestroyAllLeavesForeignDatasetsAlone(t *testing.T) {
	s := &scriptedZfs{listings: []string{
		listing(testForeign, "tank-backup"),
		listing(testForeign, "tank-backup"),
	}}
	s.install(t)

	if err := NewZFS("zdap/1/databases").DestroyAll(); err != nil {
		t.Fatal(err)
	}
	if got := s.destroyed(); len(got) != 0 {
		t.Errorf("destroyed %q, want nothing", got)
	}
}

// Cleanup that stops at the first failure leaves more behind than one that
// carries on, so the rest must still be attempted and every failure reported.
func TestDestroyAllKeepsGoingAfterAFailure(t *testing.T) {
	otherBase := "zdap-postgres-brex-base-2026-11-10T09.00.00"
	s := &scriptedZfs{
		listings: []string{
			listing(testBase, otherBase),
			listing(testBase),
		},
		failOn: "zdap/1/databases/" + testBase,
	}
	s.install(t)

	err := NewZFS("zdap/1/databases").DestroyAll()
	if err == nil {
		t.Fatal("want an error when a destroy fails")
	}
	if !strings.Contains(err.Error(), "dataset is busy") {
		t.Errorf("error = %v, want what zfs printed", err)
	}

	// The second pass only sweeps clones, so the failed base is not retried.
	// What matters is that the base after it in the first pass was still
	// attempted rather than skipped.
	var sawOther bool
	for _, p := range s.destroyed() {
		if strings.HasSuffix(p, otherBase) {
			sawOther = true
		}
	}
	if !sawOther {
		t.Errorf("destroyed %q, want the second base attempted after the first failed", s.destroyed())
	}
}
