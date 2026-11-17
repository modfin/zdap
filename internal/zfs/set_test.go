package zfs

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSetUserPropertyIsOneCommand(t *testing.T) {
	args := stubZfs(t, "", "", nil)

	if err := NewZFS("zdap/1/databases").SetUserProperty(testClone, PropHealthy, "true"); err != nil {
		t.Fatal(err)
	}

	want := []string{"set", "zdap:healthy=true", "zdap/1/databases/" + testClone}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %q, want %q", *args, want)
	}
}

// Claiming a clone writes an owner and an expiry. ZFS applies them together,
// so a reader never sees one without the other.
func TestSetUserPropertiesGoOnTogether(t *testing.T) {
	args := stubZfs(t, "", "", nil)

	err := NewZFS("zdap/1/databases").SetUserProperties(testClone, map[string]string{
		PropOwner:   "alice",
		PropExpires: "2026-11-18T09.00.00",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"set",
		"zdap:expires_at=2026-11-18T09.00.00",
		"zdap:owner=alice",
		"zdap/1/databases/" + testClone,
	}
	if !reflect.DeepEqual(*args, want) {
		t.Errorf("args = %q, want one command carrying both, sorted", *args)
	}
}

func TestSetRefusesNamesZdapDidNotCreate(t *testing.T) {
	for _, name := range []string{"", "scratch", "-o", "zdap/1/databases"} {
		t.Run(name, func(t *testing.T) {
			args := stubZfs(t, "", "", nil)

			err := NewZFS("zdap/1/databases").SetUserProperty(name, PropHealthy, "true")
			if err == nil {
				t.Fatalf("SetUserProperty(%q) succeeded, want a refusal", name)
			}
			if len(*args) != 0 {
				t.Errorf("zfs was run with %q for a name that should have been refused", *args)
			}
		})
	}
}

func TestSetWithNoPropertiesRunsNothing(t *testing.T) {
	args := stubZfs(t, "", "", nil)

	if err := NewZFS("zdap/1/databases").SetUserProperties(testClone, nil); err != nil {
		t.Fatal(err)
	}
	if len(*args) != 0 {
		t.Errorf("zfs was run with %q for an empty property set", *args)
	}
}

func TestSetReportsWhatZfsSaid(t *testing.T) {
	stubZfs(t, "", "cannot set property for 'x': permission denied", errors.New("exit status 1"))

	err := NewZFS("zdap/1/databases").SetUserProperty(testClone, PropHealthy, "true")
	if err == nil {
		t.Fatal("want an error when zfs set fails")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %v, want it to carry what zfs printed", err)
	}
}
