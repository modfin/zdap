package zfs

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// A channel program result covering what a real pool holds: a base, its
// snapshot, a pooled clone that has been claimed, and a clone that carries
// none of the properties written after creation.
const listingJSON = `{"return": {
  "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49": {
    "name": "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49",
    "props": {"zdap:resource": "postgres-boris", "zdap:created_at": "2026-11-14T11.23.49"}
  },
  "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49@snap": {
    "name": "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49@snap",
    "props": {"zdap:resource": "postgres-boris", "zdap:created_at": "2026-11-14T11.23.49"}
  },
  "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.01.02.abc": {
    "name": "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.01.02.abc",
    "props": {
      "zdap:resource": "postgres-boris",
      "zdap:owner": "lars-erik",
      "zdap:created_at": "2026-11-14T12.01.02",
      "zdap:snapped_at": "2026-11-14T11.23.49",
      "zdap:clone_pooled": "true",
      "zdap:healthy": "true",
      "zdap:port": "15432",
      "zdap:expires_at": "2026-11-14T13.01.02"
    }
  },
  "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.05.00.xyz": {
    "name": "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.05.00.xyz",
    "props": {
      "zdap:resource": "postgres-boris",
      "zdap:owner": "zdapd",
      "zdap:created_at": "2026-11-14T12.05.00",
      "zdap:snapped_at": "2026-11-14T11.23.49",
      "zdap:clone_pooled": "false"
    }
  }
}}`

func loadTestListing(t *testing.T) *Listing {
	t.Helper()
	stubZfs(t, listingJSON, "", nil)

	lst, err := NewZFS("zdap/1/databases").Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return lst
}

func TestListingNamesAreRelativeToThePool(t *testing.T) {
	want := []string{
		"zdap-postgres-boris-base-2026-11-14T11.23.49",
		"zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.01.02.abc",
		"zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.05.00.xyz",
		"zdap-postgres-boris-base-2026-11-14T11.23.49@snap",
	}
	if got := loadTestListing(t).Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("names = %q, want %q", got, want)
	}
}

func TestListingBases(t *testing.T) {
	want := []string{"zdap-postgres-boris-base-2026-11-14T11.23.49"}
	if got := loadTestListing(t).Bases(); !reflect.DeepEqual(got, want) {
		t.Errorf("bases = %q, want %q", got, want)
	}
}

func TestListingSnaps(t *testing.T) {
	snaps := loadTestListing(t).Snaps()
	if len(snaps) != 1 {
		t.Fatalf("snaps = %d, want 1", len(snaps))
	}

	s := snaps[0]
	if s.Name != "zdap-postgres-boris-base-2026-11-14T11.23.49@snap" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Resource != "postgres-boris" {
		t.Errorf("resource = %q, want postgres-boris", s.Resource)
	}
	if want := time.Date(2026, 11, 14, 11, 23, 49, 0, time.UTC); !s.CreatedAt.Equal(want) {
		t.Errorf("created at = %v, want %v", s.CreatedAt, want)
	}
}

func TestListingClonesParsesEveryProperty(t *testing.T) {
	clones := loadTestListing(t).Clones()
	if len(clones) != 2 {
		t.Fatalf("clones = %d, want 2", len(clones))
	}

	c := clones[0]
	if c.Resource != "postgres-boris" || c.Owner != "lars-erik" {
		t.Errorf("resource = %q, owner = %q", c.Resource, c.Owner)
	}
	if want := time.Date(2026, 11, 14, 12, 1, 2, 0, time.UTC); !c.CreatedAt.Equal(want) {
		t.Errorf("created at = %v, want %v", c.CreatedAt, want)
	}
	if want := time.Date(2026, 11, 14, 11, 23, 49, 0, time.UTC); !c.SnappedAt.Equal(want) {
		t.Errorf("snapped at = %v, want %v", c.SnappedAt, want)
	}
	if !c.ClonePooled || !c.Healthy {
		t.Errorf("pooled = %v, healthy = %v, want both true", c.ClonePooled, c.Healthy)
	}
	if c.Port != 15432 {
		t.Errorf("port = %d, want 15432", c.Port)
	}
	if c.ExpiresAt == nil {
		t.Fatal("expires at = nil, want a time")
	}
	if want := time.Date(2026, 11, 14, 13, 1, 2, 0, time.UTC); !c.ExpiresAt.Equal(want) {
		t.Errorf("expires at = %v, want %v", c.ExpiresAt, want)
	}
}

// A clone carries zdap:healthy, zdap:expires_at and zdap:port only once
// something has written them, so the listing has to treat them as absent
// rather than failing. This is the case that would break if the channel
// program used zfs.get_prop, which throws on a property that is not set.
func TestListingClonesToleratesMissingProperties(t *testing.T) {
	c := loadTestListing(t).Clones()[1]

	if c.Healthy {
		t.Error("healthy = true, want false for a clone with no zdap:healthy")
	}
	if c.ExpiresAt != nil {
		t.Errorf("expires at = %v, want nil for a clone with no zdap:expires_at", c.ExpiresAt)
	}
	if c.Port != 0 {
		t.Errorf("port = %d, want 0 for a clone with no zdap:port", c.Port)
	}
	if c.ClonePooled {
		t.Error("pooled = true, want false")
	}
	// What it does carry still has to come through.
	if c.Owner != "zdapd" {
		t.Errorf("owner = %q, want zdapd", c.Owner)
	}
}

func TestListingToleratesDatasetsWithNoProperties(t *testing.T) {
	stubZfs(t, `{"return": {
		"zdap/1/databases/zdap-postgres-x-base-2026-11-14T11.23.49": {"name": "zdap/1/databases/zdap-postgres-x-base-2026-11-14T11.23.49", "props": {}},
		"zdap/1/databases/stray-dataset": {"name": "zdap/1/databases/stray-dataset"}
	}}`, "", nil)

	lst, err := NewZFS("zdap/1/databases").Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(lst.Names()); n != 2 {
		t.Errorf("names = %d, want 2", n)
	}
	if n := len(lst.Bases()); n != 1 {
		t.Errorf("bases = %d, want 1", n)
	}
	if n := len(lst.Snaps()); n != 0 {
		t.Errorf("snaps = %d, want 0", n)
	}
	if n := len(lst.Clones()); n != 0 {
		t.Errorf("clones = %d, want 0", n)
	}
}

// zdap:expires_at is the one timestamp compared against wall clock time: the
// clone pool prunes a clone once it is before time.Now(). Written and read as
// UTC, that comparison is correct whatever zone the host is in. Stamping local
// wall clock and reading it back as UTC used to shift the instant by the
// host's offset, so a claimed clone outlived its TTL by an hour in CET.
func TestListingExpiryIsRelativeToWallClock(t *testing.T) {
	for _, tc := range []struct {
		name        string
		offset      time.Duration
		wantExpired bool
	}{
		{"expired ten minutes ago", -10 * time.Minute, true},
		{"expired two hours ago", -2 * time.Hour, true},
		{"expires in ten minutes", 10 * time.Minute, false},
		{"expires in two hours", 2 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Written the way clonepool writes it.
			written := FormatTimestamp(time.Now().Add(tc.offset))
			stubZfs(t, `{"return": {
				"zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.01.02.abc": {
					"name": "zdap/1/databases/zdap-postgres-boris-base-2026-11-14T11.23.49-clone-2026-11-14T12.01.02.abc",
					"props": {"zdap:resource": "postgres-boris", "zdap:expires_at": "`+written+`"}
				}
			}}`, "", nil)

			lst, err := NewZFS("zdap/1/databases").Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			clones := lst.Clones()
			if len(clones) != 1 {
				t.Fatalf("clones = %d, want 1", len(clones))
			}
			if clones[0].ExpiresAt == nil {
				t.Fatal("expires at = nil, want a time")
			}

			// The test the clone pool makes in pruneExpired.
			if got := clones[0].ExpiresAt.Before(time.Now()); got != tc.wantExpired {
				t.Errorf("ExpiresAt(%s).Before(now) = %v, want %v (wrote %q, read %v)",
					tc.offset, got, tc.wantExpired, written, clones[0].ExpiresAt)
			}
		})
	}
}

// Every zdap timestamp is UTC, which is what keeps dataset names, the zdap:*
// properties and the API wire format agreeing with each other on a host that
// is not itself on UTC.
func TestListingTimestampsAreUTC(t *testing.T) {
	c := loadTestListing(t).Clones()[0]

	if zone, _ := c.CreatedAt.Zone(); zone != "UTC" {
		t.Errorf("created at zone = %q, want UTC", zone)
	}
	if zone, _ := c.SnappedAt.Zone(); zone != "UTC" {
		t.Errorf("snapped at zone = %q, want UTC", zone)
	}
	if got, want := c.CreatedAt.Format(time.RFC3339), "2026-11-14T12:01:02Z"; got != want {
		t.Errorf("created at = %s, want %s", got, want)
	}
}
