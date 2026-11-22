package replication

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_ParseBaseName(t *testing.T) {
	for _, tc := range []struct {
		in       string
		resource string
		ok       bool
	}{
		{"zdap-postgres-carrel-base-2026-11-22T10.54.02", "postgres-carrel", true},
		// resource names contain hyphens
		{"zdap-postgres-irma-v3-base-2026-11-22T04.07.40", "postgres-irma-v3", true},

		// everything below must be refused: these are what a hostile or
		// confused peer would send to get a dataset created somewhere else.
		{"zdap-postgres-carrel-base-2026-11-22T10.54.02-clone-2026-11-22T11.00.00.abc", "", false},
		{"zdap-postgres-carrel-base-2026-11-22T10.54.02@snap", "", false},
		{"../../etc/passwd", "", false},
		{"tank/secrets", "", false},
		{"zdap-postgres-carrel-base-nonsense", "", false},
		{"zdap--base-2026-11-22T10.54.02", "", false},
		{"", "", false},
	} {
		resource, ok := ParseBaseName(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		assert.Equal(t, tc.resource, resource, tc.in)
	}
}

func Test_SnapshotOf_and_Path(t *testing.T) {
	assert.Equal(t, "zdap-x-base-2026-01-01T00.00.00@snap", SnapshotOf("zdap-x-base-2026-01-01T00.00.00"))
	assert.Equal(t, "zdap/databases/zdap-x", Path("zdap/databases", "zdap-x"))
}
