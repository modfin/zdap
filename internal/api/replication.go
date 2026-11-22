package api

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/modfin/zdap/internal/config"
	"github.com/modfin/zdap/internal/core"
	"github.com/modfin/zdap/internal/replication"
	"github.com/modfin/zdap/internal/zfs"
)

// StartReplication serves the node-to-node API on its own mTLS listener.
//
// It is deliberately not folded into the developer API: that one is
// unauthenticated and every developer and CI job reaches it, so requiring
// client certificates there would mean issuing a certificate to each of them.
// Keeping the two apart also lets the replication port be firewalled on its own.
func StartReplication(ctx context.Context, cfg *config.Config, app *core.Core, z *zfs.ZFS, m *config.TLSMaterial) error {
	e := echo.New()

	// A panic in a handler here would otherwise take the whole daemon down with
	// it, stopping restores and clone serving on this node because a peer sent
	// something malformed.
	e.Use(middleware.Recover())

	recv := replication.NewReceiver(z.Pool(), servesResource(app))
	client := replication.NewClient(m, cfg.ReplicationPort)
	jobs := newJobs()

	e.POST("/replication/bases/:dataset", func(c *echo.Context) error {
		err := recv.Recv(c.Request().Context(), c.Param("dataset"), c.Request().Body)
		return replicationError(c, err, nil)
	})

	e.GET("/replication/bases/:dataset/resume", func(c *echo.Context) error {
		token, err := recv.ResumeToken(c.Request().Context(), c.Param("dataset"))
		return replicationError(c, err, map[string]string{"token": token})
	})

	// Build a base here. Asynchronous because a restore runs for hours; the
	// caller polls the job. Today only the local CLI uses this, but a central
	// scheduler would drive exactly the same endpoint.
	e.POST("/replication/resources/:resource/build", func(c *echo.Context) error {
		resource := c.Param("resource")
		if !app.HasResource(resource) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "no such resource"})
		}
		id := jobs.start(func(ctx context.Context) error {
			return app.CreateBaseAndSnap(ctx, resource, false)
		})
		return c.JSON(http.StatusAccepted, map[string]string{"job": id})
	})

	// Push an already-built base to explicit targets. The targets come from the
	// request rather than only from replicate_to so that a scheduler can steer
	// replication without rewriting config files on every host.
	e.POST("/replication/resources/:resource/replicate", func(c *echo.Context) error {
		resource := c.Param("resource")
		var body struct {
			Targets []string `json:"targets"`
			Dataset string   `json:"dataset"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		dataset := body.Dataset
		if dataset == "" {
			latest, err := latestBase(c.Request().Context(), z, resource)
			if err != nil {
				return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
			}
			dataset = latest
		}
		targets := body.Targets
		if len(targets) == 0 {
			r, ok := app.GetResource(resource)
			if !ok {
				return c.JSON(http.StatusNotFound, map[string]string{"error": "no such resource"})
			}
			targets = r.ReplicateTo
		}
		id := jobs.start(func(ctx context.Context) error {
			return replication.PushAll(ctx, client, z.Pool(), dataset, targets)
		})
		return c.JSON(http.StatusAccepted, map[string]string{"job": id, "dataset": dataset})
	})

	e.GET("/replication/jobs/:id", func(c *echo.Context) error {
		j, ok := jobs.get(c.Param("id"))
		if !ok {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "no such job"})
		}
		return c.JSON(http.StatusOK, j)
	})

	addr := fmt.Sprintf(":%d", cfg.ReplicationPort)
	ln, err := tls.Listen("tcp", addr, m.ServerTLS())
	if err != nil {
		return fmt.Errorf("could not listen for replication on %s: %w", addr, err)
	}
	// No read or write timeouts: a base is streamed in a single request that can
	// run for hours. The client's context bounds it instead.
	srv := &http.Server{Handler: e}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	fmt.Println("== Starting Replication Server ==", addr)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func servesResource(app *core.Core) func(string) bool {
	return func(resource string) bool { return app.HasResource(resource) }
}

func replicationError(c *echo.Context, err error, ok any) error {
	var rejected *replication.Rejected
	switch {
	case err == nil:
		if ok == nil {
			return c.NoContent(http.StatusOK)
		}
		return c.JSON(http.StatusOK, ok)
	case errors.As(err, &rejected):
		return c.JSON(http.StatusForbidden, map[string]string{"error": rejected.Reason})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func latestBase(ctx context.Context, z *zfs.ZFS, resource string) (string, error) {
	lst, err := z.Load(ctx)
	if err != nil {
		return "", err
	}
	rx, err := regexp.Compile("^zdap-" + regexp.QuoteMeta(resource) + "-base-")
	if err != nil {
		return "", err
	}
	var found []string
	for _, b := range lst.Bases() {
		if rx.MatchString(b) {
			found = append(found, b)
		}
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no bases for resource %q", resource)
	}
	sort.Strings(found)
	return found[len(found)-1], nil
}

// --- job tracking -----------------------------------------------------------

type job struct {
	ID       string     `json:"id"`
	State    string     `json:"state"` // running | done | failed
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Error    string     `json:"error,omitempty"`
}

type jobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*job
}

func newJobs() *jobRegistry { return &jobRegistry{jobs: map[string]*job{}} }

func (r *jobRegistry) start(fn func(context.Context) error) string {
	id := newJobID()
	j := &job{ID: id, State: "running", Started: time.Now()}
	r.mu.Lock()
	r.jobs[id] = j
	r.mu.Unlock()

	go func() {
		// Deliberately not the request context: the work outlives the request
		// that asked for it, and a poll-and-disconnect must not cancel a restore.
		err := fn(context.Background())
		now := time.Now()
		r.mu.Lock()
		defer r.mu.Unlock()
		j.Finished = &now
		if err != nil {
			j.State, j.Error = "failed", err.Error()
			return
		}
		j.State = "done"
	}()
	return id
}

func (r *jobRegistry) get(id string) (job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return job{}, false
	}
	return *j, true
}

// newJobID uses crypto/rand rather than the package's math/rand helper: job ids
// are handed out over the network, and a guessable id lets a peer poll jobs it
// did not start.
func newJobID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not something to paper over with a weak id.
		panic(fmt.Sprintf("could not generate job id: %v", err))
	}
	return hex.EncodeToString(b[:])
}
