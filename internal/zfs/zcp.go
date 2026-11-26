package zfs

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// This is what replaced go-libzfs on the read path: a zfs channel program runs
// inside the kernel in one transaction and returns only what was asked for, so
// a listing costs one ioctl instead of a dataset handle and a full property
// table load per dataset. See bench/README.md for the numbers.
//
// Channel programs require root. zdapd already runs as root because it mounts
// datasets, so that is not a new requirement, but it does mean these paths
// cannot be exercised by an unprivileged process.

const (
	// zcpInstructionLimit caps how much work a program may do. The ZFS default
	// is 10 million and the maximum is 100 million. Listing costs a handful of
	// instructions per dataset plus a few per property, so this leaves room for
	// a pool with hundreds of thousands of datasets.
	zcpInstructionLimit = 50 * 1000 * 1000
	// zcpMemoryLimit caps the memory a program may allocate. The ZFS default is
	// 10 MiB and the maximum is 100 MiB. The result set is the bulk of it, and
	// it grows with the number of clones, each carrying eight zdap:* values, so
	// this is set well above the default. A program that exceeds the limit
	// fails outright rather than returning partial results.
	zcpMemoryLimit = 64 * 1024 * 1024
)

//go:embed listDatasets.lua
var zcpListDatasets string

// zcpDataset is one entry of a listing program's result.
type zcpDataset struct {
	Name string `json:"name"`
	// Props holds the dataset's zdap:* user properties. A dataset that carries
	// none of them has an empty map, not an error.
	Props map[string]string `json:"props"`
}

// execZfs runs the zfs command and returns its stdout and stderr. It is a
// variable so that tests can substitute a stub for it.
var execZfs = func(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "zfs", args...)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// poolName returns the pool a dataset path belongs to. A channel program is
// run against a pool, not against the dataset it happens to look at.
func poolName(path string) string {
	if i := strings.Index(path, "/"); i >= 0 {
		return path[:i]
	}
	return path
}

// channelProgram runs a read-only zfs channel program against the pool and
// decodes its return value into dest.
func channelProgram[T any](ctx context.Context, pool, program string, args []string, dest *T) error {
	f, err := os.CreateTemp("", "zdap-zcp-*.lua")
	if err != nil {
		return fmt.Errorf("could not create channel program file: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)

	_, err = f.WriteString(program)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("could not write channel program file: %w", err)
	}

	// -j prints JSON, -n runs the program read only.
	argv := []string{
		"program", "-jn",
		"-t", strconv.Itoa(zcpInstructionLimit),
		"-m", strconv.Itoa(zcpMemoryLimit),
		pool, name, "--",
	}
	argv = append(argv, args...)

	zfsstat.Inc(zfsstat.OpChannelProgram)
	zfsstat.Add(zfsstat.VolKernelIoctls, zfsstat.IoctlsPerChannelProgram)

	stdout, stderr, runErr := execZfs(ctx, argv...)

	// zfs program reports a failing program as JSON on stdout, but refuses a
	// malformed invocation, or a non-root caller, in plain text on stderr. Try
	// the structured form first and fall back to the raw output.
	var result struct {
		Return T       `json:"return"`
		Error  *string `json:"error"`
	}
	if err := json.Unmarshal(stdout, &result); err != nil {
		if runErr != nil {
			return fmt.Errorf("zfs program failed: %w: %s", runErr, zcpMessage(stderr, stdout))
		}
		return fmt.Errorf("could not decode zfs program output: %w: %s", err, zcpMessage(stdout, stderr))
	}
	if result.Error != nil {
		return fmt.Errorf("zfs program error: %s", *result.Error)
	}
	if runErr != nil {
		return fmt.Errorf("zfs program failed: %w: %s", runErr, zcpMessage(stderr, stdout))
	}

	if dest != nil {
		*dest = result.Return
	}
	return nil
}

// zcpMessage picks the most useful of the two output streams for an error, and
// keeps it to one line so it stays readable in a log.
func zcpMessage(preferred, fallback []byte) string {
	msg := strings.TrimSpace(string(preferred))
	if msg == "" {
		msg = strings.TrimSpace(string(fallback))
	}
	msg = strings.ReplaceAll(msg, "\n", "; ")
	if len(msg) > 500 {
		msg = msg[:500] + "..."
	}
	if msg == "" {
		return "no output"
	}
	return msg
}

// IsCloneName reports whether a pool-relative dataset name is a zdap clone.
func IsCloneName(name string) bool { return cloneReg.MatchString(name) }

// IsSnapName reports whether a pool-relative dataset name is a zdap snapshot.
func IsSnapName(name string) bool { return snapReg.MatchString(name) }

// IsBaseName reports whether a pool-relative dataset name is a zdap base.
func IsBaseName(name string) bool { return baseReg.MatchString(name) }
