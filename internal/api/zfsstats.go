package api

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/modfin/zdap/internal/zfs/zfsstat"
)

// DebugZFSOpsPath is the route that exposes the ZFS operation counters. It is
// what cmd/zdap-bench reads to attribute ZFS work to REST endpoints.
const DebugZFSOpsPath = "/debug/zfs-ops"

var processStart = time.Now()

// zfsOpsReport is the payload of GET /debug/zfs-ops.
type zfsOpsReport struct {
	// UptimeSeconds is how long this zdapd has been running, so a caller can
	// tell whether Global covers startup work as well.
	UptimeSeconds float64 `json:"uptime_seconds"`
	// Global is every ZFS counter since process start, including work done by
	// the cron jobs and the clone pool GC, not just by HTTP requests.
	Global zfsstat.Stats `json:"global"`
	// Routes is the ZFS work attributed to each REST endpoint.
	Routes []zfsstat.RouteStats `json:"routes"`
	// Attributed is the number of libzfs calls Routes accounts for, and
	// Unattributed the rest of Global: background cron and clone pool work,
	// plus anything that ran before the last reset.
	Attributed   uint64 `json:"attributed_libzfs_calls"`
	Unattributed uint64 `json:"unattributed_libzfs_calls"`
}

// zfsStatsMiddleware attributes the ZFS counter delta of each request to its
// route. The counters are process global, so a delta is only trustworthy when
// nothing else touched ZFS at the same time; requests that overlapped another
// measured request are flagged in RouteStats.Overlapped. Benchmark serially.
func zfsStatsMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if c.Request().URL.Path == DebugZFSOpsPath {
			return next(c)
		}

		before := zfsstat.Snapshot()
		overlapped := zfsstat.Enter()
		start := time.Now()

		// Recorded in a defer so that a panicking handler (and zdapd has one,
		// see /status on ZFS versions newer than go-libzfs knows about) still
		// reports the work it managed to do.
		defer func() {
			dur := time.Since(start)
			zfsstat.Leave()
			zfsstat.RecordRequest(c.Request().Method, routePath(c), dur, zfsstat.Snapshot().Sub(before), overlapped)
		}()

		return next(c)
	}
}

func routePath(c *echo.Context) string {
	if p := c.RouteInfo().Path; p != "" {
		return p
	}
	return c.Request().URL.Path
}

// registerZFSOpsRoutes adds the counter endpoints used by cmd/zdap-bench.
func registerZFSOpsRoutes(e *echo.Echo) {
	e.GET(DebugZFSOpsPath, func(c *echo.Context) error {
		routes := zfsstat.Routes()
		global := zfsstat.Snapshot()

		var attributed uint64
		for _, r := range routes {
			attributed += r.Ops.LibzfsCalls()
		}
		total := global.LibzfsCalls()

		var unattributed uint64
		if total > attributed {
			unattributed = total - attributed
		}

		return c.JSON(http.StatusOK, zfsOpsReport{
			UptimeSeconds: time.Since(processStart).Seconds(),
			Global:        global,
			Routes:        routes,
			Attributed:    attributed,
			Unattributed:  unattributed,
		})
	})

	// Reset clears the per-route stats so a benchmark run starts from zero.
	// The global counters keep running, they are monotonic by design.
	e.DELETE(DebugZFSOpsPath, func(c *echo.Context) error {
		zfsstat.ResetRoutes()
		return c.NoContent(http.StatusOK)
	})
}
