package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// The counters that get a column in the summary table. The full set is in the
// JSON output and in internal/zfs/zfsstat.
const (
	opRecursiveOpen = "op.dataset_open_recursive"
	opUserProp      = "op.get_user_property"
	opPath          = "op.dataset_path"
	volHandles      = "vol.dataset_handles"
	volPropValues   = "vol.property_values"
	volIoctls       = "vol.kernel_ioctls_est"
)

func printReport(r Report) {
	fmt.Printf("requests   %d per endpoint (+%d warmup), serial\n\n", r.Requests, r.Warmup)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ENDPOINT\tOK\tms\tPOOL OPENS\tHANDLES\tPROP VALS\tUSERPROPS\tPATHS\tLIBZFS\tIOCTLS")
	fmt.Fprintln(w, "\t\t\tper req\tper req\tper req\tper req\tper req\tper req\tper req")

	var totIoctls, totHandles, totLibzfs float64
	for _, res := range r.Results {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			res.Name,
			okColumn(res),
			num(res.ClientMsAvg, 1),
			num(res.Ops[opRecursiveOpen], 1),
			num(res.Ops[volHandles], 1),
			num(res.Ops[volPropValues], 0),
			num(res.Ops[opUserProp], 0),
			num(res.Ops[opPath], 0),
			num(res.ZfsCallsPerRequest, 0),
			num(res.Ops[volIoctls], 0),
		)
		totIoctls += res.Ops[volIoctls]
		totHandles += res.Ops[volHandles]
		totLibzfs += res.ZfsCallsPerRequest
	}
	fmt.Fprintln(w, "\t\t\t\t\t\t\t\t\t")
	fmt.Fprintf(w, "one of each\t\t\t\t%s\t\t\t\t%s\t%s\n", num(totHandles, 0), num(totLibzfs, 0), num(totIoctls, 0))
	w.Flush()

	fmt.Println()
	fmt.Println("POOL OPENS  recursive opens of the whole zpool (zfs.DatasetOpen on the pool root)")
	fmt.Println("HANDLES     dataset handles materialised: one per filesystem and per snapshot,")
	fmt.Println("            each with its full native property table loaded")
	fmt.Println("PROP VALS   native property values read out of libzfs; zdap uses at most 8 of them")
	fmt.Println("USERPROPS   zdap:* user property reads")
	fmt.Println("LIBZFS      total libzfs entry point calls")
	fmt.Println("IOCTLS      estimated /dev/zfs ioctls, calibrated by tracing; see bench/zfs-ioctl-bench.sh")

	printProblems(r)
	printSkipped(r)
}

func okColumn(res Result) string {
	ok := 0
	for status, n := range res.Statuses {
		if status >= 200 && status <= 299 {
			ok += n
		}
	}
	return fmt.Sprintf("%d/%d", ok, res.Requests)
}

func printProblems(r Report) {
	var lines []string
	for _, res := range r.Results {
		if res.Overlapped > 0 {
			lines = append(lines, fmt.Sprintf("  %s: %d of %d requests overlapped other ZFS work, numbers are not reliable",
				res.Name, res.Overlapped, res.Requests))
		}
		for _, e := range res.Errors {
			lines = append(lines, fmt.Sprintf("  %s: %s", res.Name, e))
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Println("\nproblems")
	for _, l := range lines {
		fmt.Println(l)
	}
}

func printSkipped(r Report) {
	if len(r.Skipped) == 0 {
		return
	}
	fmt.Println("\nnot benchmarked")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range r.Skipped {
		fmt.Fprintf(w, "  %s\t%s\n", s.Name, s.Reason)
	}
	w.Flush()
}

// printComparison shows what changed between an earlier run and this one. It
// is the view that matters while migrating endpoints to channel programs: the
// ZFS work per request should fall, and nothing else should start failing.
func printComparison(base, now Report) {
	fmt.Printf("\ncompared with %s (%s)\n\n", base.GeneratedAt.Format("2006-01-02 15:04:05"), base.Addr)

	byName := map[string]Result{}
	for _, r := range base.Results {
		byName[r.Name] = r
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ENDPOINT\tHANDLES\tLIBZFS\tIOCTLS\tms")
	for _, r := range now.Results {
		b, ok := byName[r.Name]
		if !ok {
			fmt.Fprintf(w, "%s\t(new)\t\t\t\n", r.Name)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.Name,
			delta(b.Ops[volHandles], r.Ops[volHandles]),
			delta(b.ZfsCallsPerRequest, r.ZfsCallsPerRequest),
			delta(b.Ops[volIoctls], r.Ops[volIoctls]),
			delta(b.ClientMsAvg, r.ClientMsAvg),
		)
		delete(byName, r.Name)
	}
	var gone []string
	for name := range byName {
		gone = append(gone, name)
	}
	sort.Strings(gone)
	for _, name := range gone {
		fmt.Fprintf(w, "%s\t(gone)\t\t\t\n", name)
	}
	w.Flush()

	if poolChanged(base, now) {
		fmt.Println()
		fmt.Println("note: HANDLES is one per dataset in the pool, so a change in it means the")
		fmt.Println("      pool itself grew or shrank between the two runs. LIBZFS and IOCTLS")
		fmt.Println("      move with it, and only a change beyond that is down to the code.")
	}
}

// poolChanged reports whether an endpoint that still opens the whole pool
// materialised a different number of dataset handles than it did in the
// baseline, which means the pool holds a different number of datasets.
func poolChanged(base, now Report) bool {
	handles := func(r Report) float64 {
		var max float64
		for _, res := range r.Results {
			if h := res.Ops[volHandles]; h > max {
				max = h
			}
		}
		return max
	}
	b, n := handles(base), handles(now)
	return b > 0 && n > 0 && b != n
}

// delta renders "before -> after (-62%)".
func delta(before, after float64) string {
	if before == 0 && after == 0 {
		return "-"
	}
	s := fmt.Sprintf("%s -> %s", num(before, 1), num(after, 1))
	if before == 0 {
		return s
	}
	pct := (after - before) / before * 100
	if pct > -0.5 && pct < 0.5 {
		return s + " (=)"
	}
	return fmt.Sprintf("%s (%+.0f%%)", s, pct)
}

func num(v float64, decimals int) string {
	if v == 0 {
		return "0"
	}
	s := fmt.Sprintf("%.*f", decimals, v)
	// Drop a trailing ".0" so the table stays readable.
	if strings.HasSuffix(s, ".0") {
		s = strings.TrimSuffix(s, ".0")
	}
	return s
}
