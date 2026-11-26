package zfs

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal/servermodel"
)

// Listing is what zdap keeps under the pool dataset, as of one moment: every
// base, snapshot and clone, with its zdap:* properties.
//
// It is the replacement for passing an open *Dataset around. A request loads
// one Listing and filters it as many times as it likes, because filtering is
// now a map lookup rather than a walk over libzfs handles. Nothing in it needs
// closing, and it does not go stale on its own: reload it to see newer state.
type Listing struct {
	// datasets maps a pool-relative name to that dataset's zdap:* properties.
	datasets map[string]map[string]string
}

// Load fetches the listing with a single channel program.
func (z *ZFS) Load(ctx context.Context) (*Listing, error) {
	var datasets map[string]zcpDataset
	err := channelProgram(ctx, poolName(z.pool), zcpListDatasets, []string{z.pool}, &datasets)
	if err != nil {
		return nil, err
	}

	prefix := z.pool + "/"
	l := &Listing{datasets: make(map[string]map[string]string, len(datasets))}
	for path, ds := range datasets {
		name, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		l.datasets[name] = ds.Props
	}
	return l, nil
}

// prop returns a dataset's zdap:* property, or the empty string if the dataset
// does not carry it. Clones have no zdap:healthy or zdap:expires_at until
// something sets them, and older clones may have no zdap:port.
func (l *Listing) prop(name, prop string) string {
	return l.datasets[name][prop]
}

// Names returns every dataset and snapshot in the listing, sorted.
func (l *Listing) Names() []string {
	names := make([]string, 0, len(l.datasets))
	for name := range l.datasets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Bases returns the names of the base datasets, sorted.
func (l *Listing) Bases() []string {
	return l.namesMatching(IsBaseName)
}

func (l *Listing) namesMatching(match func(string) bool) []string {
	var names []string
	for _, name := range l.Names() {
		if match(name) {
			names = append(names, name)
		}
	}
	return names
}

// Snaps returns the zdap snapshots in the listing, sorted by name.
func (l *Listing) Snaps() []servermodel.ServerInternalSnapshot {
	var snaps []servermodel.ServerInternalSnapshot
	for _, name := range l.namesMatching(IsSnapName) {
		createdAt, _ := ParseTimestamp(l.prop(name, PropCreated))
		snaps = append(snaps, servermodel.ServerInternalSnapshot{
			PublicSnap: zdap.PublicSnap{
				Name:      name,
				Resource:  l.prop(name, PropResource),
				CreatedAt: createdAt,
			},
		})
	}
	return snaps
}

// Clones returns the clones in the listing, sorted by name.
func (l *Listing) Clones() []servermodel.ServerInternalClone {
	var clones []servermodel.ServerInternalClone
	for _, name := range l.namesMatching(IsCloneName) {
		createdAt, _ := ParseTimestamp(l.prop(name, PropCreated))
		snappedAt, _ := ParseTimestamp(l.prop(name, PropSnappedAt))

		// A clone that has never been claimed has no expiry. The clone pool
		// prunes a clone once this is before time.Now(), which is a comparison
		// of instants and so is correct in any zone, now that the value is
		// written and read as UTC.
		var expiresAt *time.Time
		if at, err := ParseTimestamp(l.prop(name, PropExpires)); err == nil {
			expiresAt = &at
		}

		// zdap:port is missing on clones made before it was recorded.
		port, _ := strconv.Atoi(l.prop(name, PropPort))

		clones = append(clones, servermodel.ServerInternalClone{
			PublicClone: zdap.PublicClone{
				Name:        name,
				Resource:    l.prop(name, PropResource),
				Owner:       l.prop(name, PropOwner),
				CreatedAt:   createdAt,
				SnappedAt:   snappedAt,
				ClonePooled: l.prop(name, PropClonePooled) == "true",
				Healthy:     l.prop(name, PropHealthy) == "true",
				ExpiresAt:   expiresAt,
				Port:        port,
			},
		})
	}
	return clones
}
