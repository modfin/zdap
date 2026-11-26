package zfs

import (
	"fmt"
	"regexp"
	"sync"
	"time"
)

func NewZFS(pool string) *ZFS {
	return &ZFS{
		pool: pool,
	}
}

type ZFS struct {
	pool     string
	poolLock sync.RWMutex
}

const PropCreated = "zdap:created_at"
const PropOwner = "zdap:owner"
const PropResource = "zdap:resource"
const PropSnappedAt = "zdap:snapped_at"
const PropClonePooled = "zdap:clone_pooled"
const PropPort = "zdap:port"
const PropExpires = "zdap:expires_at"
const PropHealthy = "zdap:healthy"

// PropImage and PropVolume record the container a base was restored in: the
// image, and the path its dataset was mounted at inside it. A clone has to run
// the same image against the same path, and the resource file cannot be
// trusted to still say what it said when an older base was built -- it is
// edited whenever the source database moves to a newer version.
const PropImage = "zdap:image"
const PropVolume = "zdap:volume"

// Runtime is the container a base was restored in, as recorded on the base by
// PropImage and PropVolume. A field is empty when the base does not record it,
// which is true of every base built before these properties existed.
type Runtime struct {
	Image  string
	Volume string
}

// props renders the fields that are set as zdap:* properties.
func (rt Runtime) props() map[string]string {
	p := map[string]string{}
	if rt.Image != "" {
		p[PropImage] = rt.Image
	}
	if rt.Volume != "" {
		p[PropVolume] = rt.Volume
	}
	return p
}

// TimestampFormat is how zdap writes a time into a dataset name and into the
// zdap:created_at, zdap:snapped_at and zdap:expires_at properties.
//
// The layout carries no zone, so a zdap timestamp is UTC by convention. Use
// FormatTimestamp and ParseTimestamp rather than time.Format and time.Parse so
// that the convention cannot drift: stamping local wall clock and reading it
// back as UTC shifts the instant by the host's offset, which used to let a
// claimed clone outlive its TTL by an hour on a host that was not on UTC.
const TimestampFormat = "2006-01-02T15.04.05"

// FormatTimestamp renders a time as a zdap timestamp, in UTC.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(TimestampFormat)
}

// ParseTimestamp reads a zdap timestamp back as the instant it denotes.
func ParseTimestamp(s string) (time.Time, error) {
	return time.Parse(TimestampFormat, s)
}

var TimeReg = regexp.MustCompile("[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}")

var cloneReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}-clone-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}.[a-zA-Z]{3}$")
var snapReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}@snap$")
var baseReg = regexp.MustCompile("^zdap.*base-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}.[0-9]{2}.[0-9]{2}$")

// Pool returns the dataset path zdap keeps its datasets under.
func (z *ZFS) Pool() string { return z.pool }

func (z *ZFS) GetDatasetBaseNameAt(name string, at time.Time) string {
	return fmt.Sprintf("zdap-%s-base-%s", name, FormatTimestamp(at))
}

func (z *ZFS) NewDatasetBaseName(name string, t time.Time) string {
	return z.GetDatasetBaseNameAt(name, t)
}
func (z *ZFS) GetDatasetSnapNameAt(name string, at time.Time) string {
	return fmt.Sprintf("%s@snap", z.GetDatasetBaseNameAt(name, at))
}

func (z *ZFS) writeLock() {
	z.poolLock.Lock()
}

func (z *ZFS) writeUnlock() {
	z.poolLock.Unlock()
}
