package core

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modfin/zdap/internal/zfs"
)

// newTestCore builds a Core over a temp config dir.
//
// The docker client is nil and the pool points at a dataset that does not
// exist: nothing in these tests gets as far as using either. A clone pool's
// action() calls Z.Load first, which fails without root and a real pool, logs
// and returns, so a pool started here spins once and then idles.
func newTestCore(t *testing.T, files map[string]string) (*Core, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		writeFile(t, dir, name, body)
	}
	c, err := NewCore(dir, "127.0.0.1", 0, nil, zfs.NewZFS("zdap/does-not-exist"))
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	return c, dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func resourceYAML(name string) string {
	return "name: " + name + "\ndocker:\n  image: postgres:18\n  port: 5432\n"
}

func pooledResourceYAML(name string, minClones int) string {
	return resourceYAML(name) +
		"clone_pool:\n  min_clones: " + strconv.Itoa(minClones) + "\n  max_clones: 5\n"
}

func names(c *Core) []string {
	return c.GetResourcesNames()
}

func TestLoadResourcesRejectsDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.resource.yml", resourceYAML("same"))
	writeFile(t, dir, "b.resource.yml", resourceYAML("same"))

	_, err := loadResources(dir)
	if err == nil {
		t.Fatal("want an error for two resources with one name")
	}
	// A duplicate would otherwise leave two clone pools driving one resource,
	// so the message has to name both files.
	for _, want := range []string{"same", "a.resource.yml", "b.resource.yml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadResourcesRejectsUnnamedAndUnparsable(t *testing.T) {
	t.Run("no name", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "x.resource.yml", "docker:\n  image: postgres:18\n")
		if _, err := loadResources(dir); err == nil {
			t.Fatal("want an error for a resource with no name")
		}
	})
	t.Run("bad yaml", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "x.resource.yml", "name: x\n  docker: [unclosed\n")
		if _, err := loadResources(dir); err == nil {
			t.Fatal("want an error for unparsable yaml")
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		// Must be an error rather than an empty set: a config directory that
		// has gone missing would otherwise empty the cron on the next reload.
		if _, err := loadResources(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("want an error for a config dir that does not exist")
		}
	})
}

func TestReloadPicksUpAddedAndRemovedResources(t *testing.T) {
	c, dir := newTestCore(t, map[string]string{"a.resource.yml": resourceYAML("alpha")})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if got := names(c); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("names = %v, want [alpha]", got)
	}

	writeFile(t, dir, "b.resource.yml", resourceYAML("beta"))
	changed, err := c.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("changed = false after adding a resource")
	}
	if got := names(c); len(got) != 2 {
		t.Fatalf("names = %v, want two", got)
	}

	if err := os.Remove(filepath.Join(dir, "a.resource.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := names(c); len(got) != 1 || got[0] != "beta" {
		t.Fatalf("names = %v, want [beta]", got)
	}
}

// Rebuilding the cron on every filesystem event would reset every schedule, so
// a reload that finds the same content has to be a no-op.
func TestReloadOfUnchangedFilesIsANoOp(t *testing.T) {
	c, dir := newTestCore(t, map[string]string{"a.resource.yml": resourceYAML("alpha")})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}

	changed, err := c.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true for identical files")
	}

	// Rewriting the same bytes is still no change.
	writeFile(t, dir, "a.resource.yml", resourceYAML("alpha"))
	changed, err = c.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("changed = true after rewriting identical content")
	}
}

// A half-written file must never be able to empty the cron.
func TestReloadKeepsTheRunningConfigWhenAFileIsBroken(t *testing.T) {
	c, dir := newTestCore(t, map[string]string{
		"a.resource.yml": resourceYAML("alpha"),
		"b.resource.yml": resourceYAML("beta"),
	})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	before := names(c)

	writeFile(t, dir, "b.resource.yml", "name: beta\n\tbroken: [\n")
	changed, err := c.Reload()
	if err == nil {
		t.Fatal("want an error for a broken resource file")
	}
	if changed {
		t.Error("changed = true although the reload failed")
	}
	if got := names(c); len(got) != len(before) {
		t.Errorf("names = %v, want the previous %v kept", got, before)
	}

	// And it recovers once the file is whole again.
	writeFile(t, dir, "b.resource.yml", resourceYAML("beta-renamed"))
	if changed, err := c.Reload(); err != nil || !changed {
		t.Fatalf("recovery reload: changed=%v err=%v", changed, err)
	}
}

// The resource a job starts with is a copy, so a reload cannot change the
// image, the scripts or the ZFS properties under a restore already running.
func TestResourceHeldByAJobSurvivesAReload(t *testing.T) {
	c, dir := newTestCore(t, map[string]string{"a.resource.yml": resourceYAML("alpha")})

	held, ok := c.getResource("alpha")
	if !ok {
		t.Fatal("alpha not found")
	}
	if held.Docker.Image != "postgres:18" {
		t.Fatalf("image = %q", held.Docker.Image)
	}

	writeFile(t, dir, "a.resource.yml", "name: alpha\ndocker:\n  image: postgres:19\n  port: 5432\n")
	if _, err := c.Reload(); err != nil {
		t.Fatal(err)
	}

	if held.Docker.Image != "postgres:18" {
		t.Errorf("the held copy changed to %q; a running job would have seen a reload", held.Docker.Image)
	}
	now, _ := c.getResource("alpha")
	if now.Docker.Image != "postgres:19" {
		t.Errorf("the new set has image %q, want postgres:19", now.Docker.Image)
	}
}

// GetResources must not hand out the slice that is in service.
func TestGetResourcesReturnsACopy(t *testing.T) {
	c, _ := newTestCore(t, map[string]string{"a.resource.yml": resourceYAML("alpha")})

	got := c.GetResources()
	got[0].Name = "mutated"

	if n := names(c); n[0] != "alpha" {
		t.Errorf("the set in service changed to %v", n)
	}
}

func TestClonePoolsFollowTheResourceFiles(t *testing.T) {
	c, dir := newTestCore(t, map[string]string{"a.resource.yml": pooledResourceYAML("alpha", 1)})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.StopClonePools()

	pool := c.poolFor("alpha")
	if pool == nil {
		t.Fatal("no pool for a resource with min_clones")
	}

	t.Run("unchanged resource keeps the same pool", func(t *testing.T) {
		if _, err := c.Reload(); err != nil {
			t.Fatal(err)
		}
		if got := c.poolFor("alpha"); got != pool {
			t.Error("the pool was replaced although nothing changed")
		}
	})

	t.Run("changed resource is given a new pool", func(t *testing.T) {
		writeFile(t, dir, "a.resource.yml", pooledResourceYAML("alpha", 2))
		if _, err := c.Reload(); err != nil {
			t.Fatal(err)
		}
		got := c.poolFor("alpha")
		if got == nil {
			t.Fatal("pool disappeared")
		}
		if got == pool {
			t.Error("the pool was kept although min_clones changed")
		}
		if got.Resource().ClonePool.MinClones != 2 {
			t.Errorf("new pool has min_clones %d, want 2", got.Resource().ClonePool.MinClones)
		}
		pool = got
	})

	t.Run("resource that stops wanting a pool loses it", func(t *testing.T) {
		writeFile(t, dir, "a.resource.yml", resourceYAML("alpha"))
		if _, err := c.Reload(); err != nil {
			t.Fatal(err)
		}
		if got := c.poolFor("alpha"); got != nil {
			t.Error("pool still running for a resource with no clone_pool")
		}
	})

	t.Run("removed resource loses its pool", func(t *testing.T) {
		writeFile(t, dir, "b.resource.yml", pooledResourceYAML("beta", 1))
		if _, err := c.Reload(); err != nil {
			t.Fatal(err)
		}
		if c.poolFor("beta") == nil {
			t.Fatal("no pool for beta")
		}
		if err := os.Remove(filepath.Join(dir, "b.resource.yml")); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Reload(); err != nil {
			t.Fatal(err)
		}
		if got := c.poolFor("beta"); got != nil {
			t.Error("pool still running for a resource that is gone")
		}
	})
}

func TestWatchConfigDirReloadsOnChange(t *testing.T) {
	c, dir := newTestCore(t, map[string]string{"a.resource.yml": resourceYAML("alpha")})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	started := make(chan struct{})
	go func() { done <- c.watchConfigDir(ctx, 50*time.Millisecond, time.Hour, started) }()
	<-started

	writeFile(t, dir, "b.resource.yml", resourceYAML("beta"))
	waitFor(t, "beta to be picked up", func() bool { return len(names(c)) == 2 })

	if err := os.Remove(filepath.Join(dir, "b.resource.yml")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "beta to be dropped", func() bool { return len(names(c)) == 1 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not return after the context was cancelled")
	}
}

// A file that is not a resource file must not cause a reload: the retrieval
// and creation scripts are read by path when a job runs.
func TestWatchConfigDirIgnoresScripts(t *testing.T) {
	c, dir := newTestCore(t, map[string]string{"a.resource.yml": resourceYAML("alpha")})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	go func() { _ = c.watchConfigDir(ctx, 50*time.Millisecond, time.Hour, started) }()
	<-started

	// Drop in a file that would be a parse error if it were ever loaded.
	writeFile(t, dir, "a.creation.sh", "#!/bin/bash\necho hello\n")
	time.Sleep(300 * time.Millisecond)

	if got := names(c); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("names = %v, want [alpha] still", got)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
