// Package replication moves finished base datasets between zdap nodes over an
// mTLS connection, so that a resource can be restored once on whichever node
// holds its cron and then sent to the peers that also serve it.
package replication

import (
	"fmt"
	"regexp"
)

// baseName matches "zdap-<resource>-base-<timestamp>". The resource part is
// greedy because resource names contain hyphens (postgres-irma-v3); it stops at
// the last "-base-", which no resource name contains.
var baseName = regexp.MustCompile(`^zdap-(.+)-base-(\d{4}-\d{2}-\d{2}T\d{2}\.\d{2}\.\d{2})$`)

// ParseBaseName returns the resource encoded in a base dataset name.
//
// The receiving side uses this to decide what it is willing to accept: a peer
// certificate proves who is pushing, not what, so a stream is only taken when
// the name parses and names a resource this node is configured for.
func ParseBaseName(dataset string) (resource string, ok bool) {
	m := baseName.FindStringSubmatch(dataset)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// SnapshotOf is the snapshot zdap takes of a finished base.
func SnapshotOf(dataset string) string { return dataset + "@snap" }

// Path qualifies a dataset name with the pool it lives in.
func Path(pool, dataset string) string { return fmt.Sprintf("%s/%s", pool, dataset) }
