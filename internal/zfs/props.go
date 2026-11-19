package zfs

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// getProps reads named properties off one dataset.
//
// ZFS prints "-" for a property that is not set, which is returned as the
// empty string: nothing zdap stores is legitimately "-", and writing that
// through to a clone is how a missing zdap:resource used to become a literal
// dash on the new dataset.
func (z *ZFS) getProps(ctx context.Context, dataset string, props ...string) (map[string]string, error) {
	zfsstat.Inc(zfsstat.OpZfsGet)

	stdout, stderr, err := execZfs(ctx, "get", "-H", "-o", "property,value", strings.Join(props, ","), dataset)
	if err != nil {
		return nil, fmt.Errorf("could not read properties of %s: %w: %s", dataset, err, zcpMessage(stderr, stdout))
	}

	out := make(map[string]string, len(props))
	for _, line := range strings.Split(string(stdout), "\n") {
		name, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		if value == "-" {
			value = ""
		}
		out[name] = value
	}
	return out, nil
}

// mountedPath returns where a dataset is mounted, and fails if it is not.
//
// zfs create and zfs clone mount the new filesystem themselves, where the
// libzfs path had to call Mount explicitly. Reading it back is still worth it:
// the mount is what the database container binds, so a dataset that is not
// mounted is of no use to anyone and should fail here rather than at container
// start.
func (z *ZFS) mountedPath(ctx context.Context, dataset string) (string, error) {
	state, err := z.getProps(ctx, dataset, "mounted", "mountpoint")
	if err != nil {
		return "", err
	}
	if state["mounted"] != "yes" || state["mountpoint"] == "" {
		return "", fmt.Errorf("%s was created but is not mounted", dataset)
	}
	return state["mountpoint"], nil
}

// zdapProps builds the -o arguments for a new dataset: the zdap:* properties
// zdap owns, then whatever the resource file asked for.
//
// The resource file's properties are applied last but can never overwrite a
// zdap:* one, so a resource cannot lie about its own dataset's owner, port or
// expiry.
func zdapProps(own map[string]string, fromResource map[string]string) []string {
	props := make(map[string]string, len(own)+len(fromResource))
	for k, v := range own {
		props[k] = v
	}
	for k, v := range fromResource {
		if strings.HasPrefix(k, "zdap:") {
			continue
		}
		props[k] = v
	}
	return propArgs(props)
}

// propAssignments renders a property map as "name=value", sorted so that a
// given set of properties always produces the same command.
func propAssignments(props map[string]string) []string {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	args := make([]string, 0, len(names))
	for _, name := range names {
		args = append(args, name+"="+props[name])
	}
	return args
}

// propArgs is propAssignments as -o arguments, for the commands that take
// properties at creation.
func propArgs(props map[string]string) []string {
	assignments := propAssignments(props)
	args := make([]string, 0, 2*len(assignments))
	for _, a := range assignments {
		args = append(args, "-o", a)
	}
	return args
}
