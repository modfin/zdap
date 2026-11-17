package main

import (
	"fmt"
	"time"
)

// Result is what one benchmarked endpoint produced.
type Result struct {
	Name     string `json:"name"`
	Method   string `json:"method"`
	Pattern  string `json:"pattern"`
	URL      string `json:"url"`
	Resource string `json:"resource"`
	Mutating bool   `json:"mutating"`

	// Requests is how many measured requests were made.
	Requests int `json:"requests"`
	// Statuses counts HTTP status codes; a failed request is status 0.
	Statuses map[int]int `json:"statuses"`
	// Errors holds transport-level errors, deduplicated.
	Errors []string `json:"errors,omitempty"`
	// Overlapped is how many requests zdapd saw running concurrently with
	// another measured request. Anything above zero makes the ZFS numbers for
	// this endpoint unreliable.
	Overlapped uint64 `json:"overlapped"`

	// ClientMs is round-trip time seen by the benchmark, ServerMs is the time
	// zdapd spent inside the handler.
	ClientMsAvg float64 `json:"client_ms_avg"`
	ClientMsMin float64 `json:"client_ms_min"`
	ClientMsMax float64 `json:"client_ms_max"`
	ServerMsAvg float64 `json:"server_ms_avg"`
	ServerMsMax float64 `json:"server_ms_max"`

	// RespBytesAvg is the average response size.
	RespBytesAvg float64 `json:"resp_bytes_avg"`

	// Ops is the ZFS work per request: counter name to average count. See
	// internal/zfs/zfsstat for what each counter means.
	Ops map[string]float64 `json:"ops_per_request"`
	// ZfsCallsPerRequest is the sum of the op.* counters. The json name is
	// kept as it was so that baselines recorded before the go-libzfs removal
	// still compare against runs made after it.
	ZfsCallsPerRequest float64 `json:"libzfs_calls_per_request"`
}

// SkippedTarget is an endpoint that was not benchmarked, and why.
type SkippedTarget struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Report is a whole benchmark run.
type Report struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Addr        string          `json:"addr"`
	Resource    string          `json:"resource"`
	Requests    int             `json:"requests_per_endpoint"`
	Warmup      int             `json:"warmup_per_endpoint"`
	Results     []Result        `json:"results"`
	Skipped     []SkippedTarget `json:"skipped,omitempty"`
}

// measure benchmarks one endpoint. The route counters are reset first, so the
// delta read afterwards belongs to this endpoint and nothing else.
func measure(c *client, t target, n, warmup int) (Result, error) {
	res := Result{
		Name:     t.Name,
		Method:   t.Method,
		Pattern:  t.Pattern,
		URL:      t.URL,
		Resource: t.Resource,
		Mutating: t.Mutating,
		Statuses: map[int]int{},
		Ops:      map[string]float64{},
	}

	call := func(measured bool) error {
		u := t.URL
		if t.Setup != nil {
			var err error
			if u, err = t.Setup(); err != nil {
				return fmt.Errorf("setup: %w", err)
			}
		}

		res.URL = u

		start := time.Now()
		r := c.do(t.Method, u)
		elapsed := float64(time.Since(start).Microseconds()) / 1000

		if measured {
			res.Requests++
			res.Statuses[r.Status]++
			if r.Err != nil {
				res.addError(r.Err.Error())
			} else if r.Status < 200 || r.Status > 299 {
				res.addError(fmt.Sprintf("HTTP %d: %s", r.Status, truncate(string(r.Body), 200)))
			}
			res.ClientMsAvg += elapsed
			if res.ClientMsMin == 0 || elapsed < res.ClientMsMin {
				res.ClientMsMin = elapsed
			}
			if elapsed > res.ClientMsMax {
				res.ClientMsMax = elapsed
			}
			res.RespBytesAvg += float64(r.Bytes)
		}

		if t.Teardown != nil {
			if err := t.Teardown(); err != nil {
				return fmt.Errorf("teardown: %w", err)
			}
		}
		return nil
	}

	for i := 0; i < warmup; i++ {
		if err := call(false); err != nil {
			return res, err
		}
	}

	// Reset after the warmup so that setup, teardown and warmup traffic stay
	// out of the numbers.
	if err := c.resetRouteStats(); err != nil {
		return res, fmt.Errorf("resetting route stats: %w", err)
	}

	for i := 0; i < n; i++ {
		if err := call(true); err != nil {
			return res, err
		}
	}

	if res.Requests > 0 {
		res.ClientMsAvg /= float64(res.Requests)
		res.RespBytesAvg /= float64(res.Requests)
	}

	report, err := c.ops()
	if err != nil {
		return res, fmt.Errorf("reading counters: %w", err)
	}
	applyRouteStats(&res, report, t)
	return res, nil
}

// applyRouteStats folds the counters zdapd attributed to this endpoint's route
// into the result. Setup and teardown calls hit other routes, so they show up
// under those routes and are simply ignored here.
func applyRouteStats(res *Result, report opsReport, t target) {
	for _, r := range report.Routes {
		if r.Method != t.Method || r.Requests == 0 {
			continue
		}
		if !sameEndpoint(r, t) {
			continue
		}
		res.Overlapped += r.Overlapped
		per := float64(r.Requests)
		for k, v := range r.Ops {
			res.Ops[k] += float64(v) / per
		}
		res.ServerMsAvg = float64(r.DurationNs) / per / 1e6
		res.ServerMsMax = float64(r.MaxDurationNs) / 1e6
	}
	for k, v := range res.Ops {
		if len(k) > 3 && k[:3] == "op." {
			res.ZfsCallsPerRequest += v
		}
	}
}

// sameEndpoint matches a route zdapd reported against the target that was
// driven. zdapd reports the route pattern ("/resources/:resource/snaps"), the
// target holds a concrete URL. Setup and teardown calls land on other
// patterns, so their cost stays out of the result.
func sameEndpoint(r routeStats, t target) bool {
	return r.Method == t.Method && r.Path == t.Pattern
}

func (r *Result) addError(msg string) {
	for _, e := range r.Errors {
		if e == msg {
			return
		}
	}
	r.Errors = append(r.Errors, msg)
}

func truncate(s string, n int) string {
	s = trimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\n' && c != '\t' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
