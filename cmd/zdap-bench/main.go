// Command zdap-bench measures how much ZFS work each zdapd REST endpoint does.
//
// It drives a running zdapd over HTTP and, for every endpoint, reads the ZFS
// operation counters that zdapd records per route (GET /debug/zfs-ops). The
// point is not latency: it is the number of times an endpoint reaches into ZFS
// and how much metadata it drags out, so that the move to zfs channel programs
// can be measured rather than guessed.
//
// Start a server first, for example:
//
//	NETWORK_ADDRESS=127.0.0.1 API_PORT=43211 ZPOOL=zdap/1/databases \
//	  CONFIG_DIR=/zdap/1/config go run ./cmd/zdapd/ serve
//
// then:
//
//	go run ./cmd/zdap-bench -addr http://127.0.0.1:43211 -n 5 -json baseline.json
//
// Later, after changing how zdapd fetches from ZFS:
//
//	go run ./cmd/zdap-bench -addr http://127.0.0.1:43211 -n 5 -compare baseline.json
//
// Requests are issued one at a time on purpose. zdapd's counters are process
// global, so overlapping requests cannot be attributed; zdap-bench reports it
// if that happened anyway, for instance because a cron job fired mid-run.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// mutatingConfirmation is the value -mutating must be given before zdap-bench
// will benchmark endpoints that create and destroy real clones and containers.
const mutatingConfirmation = "yes-create-and-destroy-clones"

func main() {
	var (
		addr     = flag.String("addr", "http://127.0.0.1:43211", "base url of the zdapd to benchmark")
		auth     = flag.String("auth", "zdap-bench", "value for the required auth header")
		n        = flag.Int("n", 5, "measured requests per endpoint")
		warmup   = flag.Int("warmup", 1, "unmeasured requests per endpoint before measuring")
		resource = flag.String("resource", "", "resource to exercise (default: the first one that has a snapshot)")
		out      = flag.String("json", "", "write the full result to this file")
		compare  = flag.String("compare", "", "compare this run against a result file written earlier by -json")
		mutating = flag.String("mutating", "", "also benchmark endpoints that create and destroy clones; pass "+mutatingConfirmation+" to confirm")
		timeout  = flag.Duration("timeout", 5*time.Minute, "per request timeout")
	)
	flag.Parse()

	c := &client{
		base: strings.TrimRight(*addr, "/"),
		auth: *auth,
		http: &http.Client{Timeout: *timeout},
	}

	if err := run(c, *n, *warmup, *resource, *out, *compare, *mutating == mutatingConfirmation); err != nil {
		fmt.Fprintln(os.Stderr, "zdap-bench:", err)
		os.Exit(1)
	}
}

func run(c *client, n, warmup int, resource, out, compare string, mutating bool) error {
	if n < 1 {
		return errors.New("-n must be at least 1")
	}

	if _, err := c.ops(); err != nil {
		return fmt.Errorf("reading %s: %w\n\tis this a zdapd built with the zfsstat instrumentation?", debugPath, err)
	}

	fmt.Printf("zdapd      %s\n", c.base)

	targets, err := discover(c, resource, mutating)
	if err != nil {
		return err
	}
	if mutating {
		warnMutating()
	}

	report := Report{
		GeneratedAt: time.Now(),
		Addr:        c.base,
		Requests:    n,
		Warmup:      warmup,
	}

	for _, t := range targets {
		if t.Skip != "" {
			report.Skipped = append(report.Skipped, SkippedTarget{Name: t.Name, Reason: t.Skip})
			continue
		}
		if report.Resource == "" {
			report.Resource = t.Resource
		}
		res, err := measure(c, t, n, warmup)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		report.Results = append(report.Results, res)
	}

	printReport(report)

	if out != "" {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("\nwrote %s\n", out)
	}

	if compare != "" {
		b, err := os.ReadFile(compare)
		if err != nil {
			return err
		}
		var base Report
		if err := json.Unmarshal(b, &base); err != nil {
			return fmt.Errorf("parsing %s: %w", compare, err)
		}
		printComparison(base, report)
	}

	return nil
}

// warnMutating spells out what a mutating run does to the server, because one
// of the endpoints it benchmarks destroys every clone of the resource,
// including clones somebody else created.
func warnMutating() {
	fmt.Println()
	fmt.Println("mutating run: this creates and destroys real clones and their docker")
	fmt.Println("containers on the server. DELETE /resources/:resource/clones destroys")
	fmt.Println("EVERY clone of the resource, not only the ones made here. Use a host")
	fmt.Println("and a resource whose clones are yours to lose.")
	fmt.Println()
}
