package main

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/modfin/zdap/internal/utils"
)

// timestampFormat is the layout the :createdAt and :time path parameters use.
// It is deliberately taken from the server's own constant: note that it is not
// the same layout zdap uses inside dataset names (zfs.TimestampFormat).
const timestampFormat = utils.TimestampFormat

// target is one endpoint to benchmark.
type target struct {
	// Name is the route as it is printed, e.g. "GET  /resources/:resource".
	Name   string
	Method string
	// Pattern is the echo route pattern zdapd reports the work under, e.g.
	// "/resources/:resource/snaps". Setup and teardown calls hit other
	// patterns, which is how their cost is kept out of the measurement.
	Pattern string
	// URL is the concrete path to call, relative to the base address. It is
	// ignored when Setup is set.
	URL string
	// Resource is the resource the target exercises.
	Resource string
	// Skip, when non-empty, says why this endpoint was not benchmarked.
	Skip string
	// Setup runs before each measured request and returns the URL to call. It
	// is for endpoints that consume something which has to exist first, such
	// as a clone to destroy. Work done here is not measured.
	Setup func() (string, error)
	// Teardown runs after each measured request, to put the server back in the
	// state it was found in. Work done here is not measured.
	Teardown func() error
	// Mutating marks endpoints that change state on the server.
	Mutating bool
}

// resourceInfo is the part of the /resources payload zdap-bench needs.
type resourceInfo struct {
	Name      string `json:"name"`
	ClonePool struct {
		MinClones int `json:"min_clones"`
	} `json:"pooled_clones"`
	Snaps []struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
		Clones    []struct {
			Name      string    `json:"name"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"clones"`
	} `json:"snaps"`
}

// discover picks a resource and a snapshot to exercise the parameterised
// routes with, then builds the endpoint list. The discovery requests
// themselves are made before any measurement starts.
func discover(c *client, want string, mutating bool) ([]target, error) {
	var resources []resourceInfo
	if err := c.getJSON("/resources", &resources); err != nil {
		// Still worth measuring the endpoints that need nothing discovered:
		// a server that cannot list its resources is exactly the case where
		// the ZFS counters are worth looking at.
		fmt.Printf("resource   could not be discovered: %v\n", err)
		return undiscoverableTargets(err), nil
	}
	if len(resources) == 0 {
		return nil, errors.New("zdapd reports no resources, nothing to benchmark")
	}

	r, err := pickResource(resources, want)
	if err != nil {
		return nil, err
	}

	var snapAt string
	if len(r.Snaps) > 0 {
		snapAt = r.Snaps[len(r.Snaps)-1].CreatedAt.Format(timestampFormat)
	}

	fmt.Printf("resource   %s (%d snapshots)\n", r.Name, len(r.Snaps))
	if snapAt != "" {
		fmt.Printf("snapshot   %s\n", snapAt)
	}

	base := "/resources/" + url.PathEscape(r.Name)
	noSnap := fmt.Sprintf("resource %s has no snapshot to work from", r.Name)
	noPool := fmt.Sprintf("resource %s has no clone pool configured", r.Name)

	ts := []target{
		{Name: "GET  /status", Pattern: "/status", Method: "GET", URL: "/status", Resource: r.Name},
		{Name: "GET  /resources", Pattern: "/resources", Method: "GET", URL: "/resources", Resource: r.Name},
		{Name: "GET  /resources/:resource", Pattern: "/resources/:resource", Method: "GET", URL: base, Resource: r.Name},
		{Name: "GET  /resources/:resource/snaps", Pattern: "/resources/:resource/snaps", Method: "GET", URL: base + "/snaps", Resource: r.Name},
		{Name: "GET  /resources/:resource/clones", Pattern: "/resources/:resource/clones", Method: "GET", URL: base + "/clones", Resource: r.Name},
		skipIf(snapAt == "", noSnap, target{
			Name: "GET  /resources/:resource/snaps/:createdAt", Pattern: "/resources/:resource/snaps/:createdAt", Method: "GET",
			URL: base + "/snaps/" + snapAt, Resource: r.Name,
		}),
	}

	mt := mutatingTargets(c, r, base, snapAt, noSnap, noPool)
	if !mutating {
		for i := range mt {
			mt[i].Skip = "mutating, rerun with -mutating " + mutatingConfirmation
		}
	}
	return append(ts, mt...), nil
}

func pickResource(resources []resourceInfo, want string) (resourceInfo, error) {
	if want != "" {
		for _, r := range resources {
			if r.Name == want {
				return r, nil
			}
		}
		return resourceInfo{}, fmt.Errorf("resource %q not found on this zdapd", want)
	}
	for _, r := range resources {
		if len(r.Snaps) > 0 {
			return r, nil
		}
	}
	return resources[0], nil
}

// mutatingTargets builds the endpoints that create or destroy clones. Each one
// leaves the server as it found it: whatever a measured request creates is
// destroyed by its teardown, and whatever it destroys is created by its setup.
func mutatingTargets(c *client, r resourceInfo, base, snapAt, noSnap, noPool string) []target {
	return []target{
		skipIf(snapAt == "", noSnap, target{
			Name: "POST /resources/:resource/snaps", Pattern: "/resources/:resource/snaps", Method: "POST",
			URL: base + "/snaps", Resource: r.Name, Mutating: true,
			Teardown: func() error { return destroyNewestClone(c, base) },
		}),
		skipIf(snapAt == "", noSnap, target{
			Name: "POST /resources/:resource/snaps/:createdAt", Pattern: "/resources/:resource/snaps/:createdAt", Method: "POST",
			URL: base + "/snaps/" + snapAt, Resource: r.Name, Mutating: true,
			Teardown: func() error { return destroyNewestClone(c, base) },
		}),
		skipIf(r.ClonePool.MinClones == 0, noPool, target{
			Name: "POST /resources/:resource/claim", Pattern: "/resources/:resource/claim", Method: "POST",
			URL: base + "/claim?ttl=60", Resource: r.Name, Mutating: true,
		}),
		skipIf(r.ClonePool.MinClones == 0, noPool, target{
			Name: "DEL  /resources/:resource/claims/:claimId", Pattern: "/resources/:resource/claims/:claimId", Method: "DELETE",
			Resource: r.Name, Mutating: true,
			Setup: func() (string, error) {
				var clone struct {
					Name string `json:"name"`
				}
				if err := c.doJSON("POST", base+"/claim?ttl=60", &clone); err != nil {
					return "", err
				}
				if clone.Name == "" {
					return "", errors.New("claim returned no clone name")
				}
				return base + "/claims/" + url.PathEscape(clone.Name), nil
			},
		}),
		skipIf(snapAt == "", noSnap, target{
			Name: "DEL  /resources/:resource/clones/:time", Pattern: "/resources/:resource/clones/:time", Method: "DELETE",
			Resource: r.Name, Mutating: true,
			Setup: func() (string, error) {
				at, err := createClone(c, base, snapAt)
				if err != nil {
					return "", err
				}
				return base + "/clones/" + at, nil
			},
		}),
		skipIf(snapAt == "", noSnap, target{
			Name: "DEL  /resources/:resource/clones", Pattern: "/resources/:resource/clones", Method: "DELETE",
			Resource: r.Name, Mutating: true,
			Setup: func() (string, error) {
				_, err := createClone(c, base, snapAt)
				return base + "/clones", err
			},
		}),
	}
}

func skipIf(cond bool, reason string, t target) target {
	if cond {
		t.Skip = reason
	}
	return t
}

// createClone clones the given snapshot and returns the new clone's created_at
// in the format the destroy routes expect.
func createClone(c *client, base, snapAt string) (string, error) {
	var clone struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := c.doJSON("POST", base+"/snaps/"+snapAt, &clone); err != nil {
		return "", err
	}
	if clone.CreatedAt.IsZero() {
		return "", errors.New("clone was created without a created_at")
	}
	return clone.CreatedAt.Format(timestampFormat), nil
}

// destroyNewestClone removes the most recently created clone of a resource, so
// that a benchmark of the clone-creating endpoints does not leave a pile of
// clones and containers behind.
func destroyNewestClone(c *client, base string) error {
	var clones []struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := c.getJSON(base+"/clones", &clones); err != nil {
		return err
	}
	if len(clones) == 0 {
		return nil
	}
	newest := clones[0].CreatedAt
	for _, cl := range clones {
		if cl.CreatedAt.After(newest) {
			newest = cl.CreatedAt
		}
	}
	return c.doJSON("DELETE", base+"/clones/"+newest.Format(timestampFormat), nil)
}

// undiscoverableTargets is the fallback when GET /resources does not answer:
// the two endpoints that take no path parameters are still benchmarked, and
// the rest are reported as skipped with the reason.
func undiscoverableTargets(cause error) []target {
	ts := []target{
		{Name: "GET  /status", Pattern: "/status", Method: "GET", URL: "/status"},
		{Name: "GET  /resources", Pattern: "/resources", Method: "GET", URL: "/resources"},
	}
	reason := fmt.Sprintf("needs a resource, and /resources failed: %v", cause)
	for _, name := range []string{
		"GET  /resources/:resource",
		"GET  /resources/:resource/snaps",
		"GET  /resources/:resource/clones",
		"GET  /resources/:resource/snaps/:createdAt",
	} {
		ts = append(ts, target{Name: name, Skip: reason})
	}
	return ts
}
