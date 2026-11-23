package cloning

import (
	"testing"

	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/zfs"
)

func TestRuntimeFor(t *testing.T) {
	// The resource file has since moved jamboree to another image and path.
	r := internal.Resource{Docker: internal.Docker{
		Image:  "timescale/timescaledb-ha:pg15.4-ts2.12.0",
		Volume: "/home/postgres/pgdata/data",
	}}

	for _, tc := range []struct {
		name   string
		origin zfs.Runtime
		want   zfs.Runtime
	}{
		{
			name:   "a base that records its runtime keeps it",
			origin: zfs.Runtime{Image: "timescale/timescaledb:2.12.0-pg15", Volume: "/var/lib/postgresql/data"},
			want:   zfs.Runtime{Image: "timescale/timescaledb:2.12.0-pg15", Volume: "/var/lib/postgresql/data"},
		},
		{
			name:   "a base from before zdap recorded one follows the resource file",
			origin: zfs.Runtime{},
			want:   zfs.Runtime{Image: "timescale/timescaledb-ha:pg15.4-ts2.12.0", Volume: "/home/postgres/pgdata/data"},
		},
		{
			// Should not happen, since both are written together, but each
			// field stands on its own rather than the pair falling back as one.
			name:   "only the image recorded",
			origin: zfs.Runtime{Image: "postgres:15"},
			want:   zfs.Runtime{Image: "postgres:15", Volume: "/home/postgres/pgdata/data"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeFor(tc.origin, r); got != tc.want {
				t.Errorf("runtimeFor = %+v, want %+v", got, tc.want)
			}
		})
	}
}
