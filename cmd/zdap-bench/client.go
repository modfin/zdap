package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// debugPath is the zdapd route that reports the ZFS operation counters.
const debugPath = "/debug/zfs-ops"

type client struct {
	base string
	auth string
	http *http.Client
}

// response is the outcome of one HTTP call.
type response struct {
	Status int
	Bytes  int
	Body   []byte
	Err    error
}

// do makes a request and reads the whole body, so that the server has finished
// the work before the caller reads the counters.
func (c *client) do(method, path string) response {
	req, err := http.NewRequest(method, c.base+path, nil)
	if err != nil {
		return response{Err: err}
	}
	req.Header.Set("auth", c.auth)

	resp, err := c.http.Do(req)
	if err != nil {
		return response{Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	return response{Status: resp.StatusCode, Bytes: len(body), Body: body, Err: err}
}

// doJSON makes a request, fails on a non-2xx status and decodes the body into
// out when out is non-nil.
func (c *client) doJSON(method, path string, out any) error {
	r := c.do(method, path)
	if r.Err != nil {
		return r.Err
	}
	if r.Status < 200 || r.Status > 299 {
		return fmt.Errorf("%s %s: %s: %s", method, path, http.StatusText(r.Status), strings.TrimSpace(string(r.Body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return fmt.Errorf("%s %s: decoding response: %w", method, path, err)
	}
	return nil
}

func (c *client) getJSON(path string, out any) error { return c.doJSON("GET", path, out) }

// --- the zdapd counters ------------------------------------------------------

// opsReport mirrors the payload of GET /debug/zfs-ops.
type opsReport struct {
	UptimeSeconds float64           `json:"uptime_seconds"`
	Global        map[string]uint64 `json:"global"`
	Routes        []routeStats      `json:"routes"`
	Attributed    uint64            `json:"attributed_libzfs_calls"`
	Unattributed  uint64            `json:"unattributed_libzfs_calls"`
}

type routeStats struct {
	Method        string            `json:"method"`
	Path          string            `json:"path"`
	Requests      uint64            `json:"requests"`
	Overlapped    uint64            `json:"overlapped"`
	Ops           map[string]uint64 `json:"ops"`
	DurationNs    uint64            `json:"duration_ns"`
	MaxDurationNs uint64            `json:"max_duration_ns"`
}

// ops reads the current counters.
func (c *client) ops() (opsReport, error) {
	var r opsReport
	err := c.getJSON(debugPath, &r)
	return r, err
}

// resetRouteStats clears the per-route counters, so the next measurement
// starts from zero.
func (c *client) resetRouteStats() error {
	return c.doJSON("DELETE", debugPath, nil)
}
