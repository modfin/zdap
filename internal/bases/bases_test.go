package bases

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/modfin/zdap/internal"
	"github.com/stretchr/testify/assert"
)

func resourceWithBinds(binds ...string) internal.Resource {
	return internal.Resource{
		Name:          "test",
		Docker:        internal.Docker{Volume: "/var/lib/postgresql/data"},
		RestoreParams: internal.ContainerParams{Binds: binds},
	}
}

func Test_restoreMounts(t *testing.T) {
	dir := t.TempDir()
	dumps := filepath.Join(dir, "dumps")
	scratch := filepath.Join(dir, "nested", "scratch")

	mounts, err := restoreMounts(resourceWithBinds(dumps+":/dumps:ro", scratch+":/scratch", dir+":/rw:rw"))
	assert.NoError(t, err)
	assert.Equal(t, []mount.Mount{
		{Type: mount.TypeBind, Source: dumps, Target: "/dumps", ReadOnly: true},
		{Type: mount.TypeBind, Source: scratch, Target: "/scratch"},
		{Type: mount.TypeBind, Source: dir, Target: "/rw"},
	}, mounts)

	// missing host directories are created, and made writable for everyone regardless of umask
	for _, p := range []string{dumps, scratch} {
		info, err := os.Stat(p)
		assert.NoError(t, err)
		assert.True(t, info.IsDir())
		assert.Equal(t, os.FileMode(0o777), info.Mode().Perm())
	}
}

func Test_restoreMounts_existingDirUntouched(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "existing")
	assert.NoError(t, os.Mkdir(dir, 0o750))

	_, err := restoreMounts(resourceWithBinds(dir + ":/existing"))
	assert.NoError(t, err)

	info, err := os.Stat(dir)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), info.Mode().Perm())
}

func Test_restoreMounts_none(t *testing.T) {
	mounts, err := restoreMounts(resourceWithBinds())
	assert.NoError(t, err)
	assert.Empty(t, mounts)
}

func Test_restoreMounts_invalid(t *testing.T) {
	dir := t.TempDir()
	invalid := map[string]string{
		"no colon":               dir,
		"relative host path":     "relative/dir:/dumps",
		"relative target":        dir + ":dumps",
		"empty target":           dir + ":",
		"unknown mode":           dir + ":/dumps:z",
		"too many parts":         dir + ":/dumps:ro:extra",
		"collides with volume":   dir + ":/var/lib/postgresql/data",
		"collides, unclean path": dir + ":/var/lib/postgresql/data/",
	}
	for name, bind := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := restoreMounts(resourceWithBinds(bind))
			assert.Error(t, err)
		})
	}
}

func Test_restoreMounts_invalidDoesNotCreateLaterDirs(t *testing.T) {
	// a bad bind fails the whole set; the host dir of a bind listed after it must not be created
	later := filepath.Join(t.TempDir(), "later")

	_, err := restoreMounts(resourceWithBinds("relative:/bad", later+":/later"))
	assert.Error(t, err)

	_, err = os.Stat(later)
	assert.True(t, os.IsNotExist(err))
}

// stubRunScript swaps the script runner out for the duration of a test. Tests
// using it must not run in parallel, since runScript is package state.
func stubRunScript(fn func(string, ...string) (string, error)) func() {
	prev := runScript
	runScript = fn
	return func() { runScript = prev }
}

func Test_retrieve_runsTheResourceScript(t *testing.T) {
	var got string
	defer stubRunScript(func(script string, args ...string) (string, error) {
		got = script
		assert.Empty(t, args, "retrieval takes no arguments")
		return "/zdap/raw/dump\n", nil
	})()

	file, err := retrieve("/zdap/config", internal.Resource{Retrieval: "./mfn/apollo.retrieval.sh"})
	assert.NoError(t, err)
	assert.Equal(t, "/zdap/config/mfn/apollo.retrieval.sh", got)
	// Output is passed through untouched; the creation scripts strip the newline.
	assert.Equal(t, "/zdap/raw/dump\n", file)
}

// The point of the split: a download must be able to run while another
// resource is part way through a restore.
func Test_retrieve_runsWhileABaseIsBeingBuilt(t *testing.T) {
	defer stubRunScript(func(string, ...string) (string, error) {
		return "/zdap/raw/dump", nil
	})()

	baseCreationMutex.Lock()
	defer baseCreationMutex.Unlock()

	done := make(chan string, 1)
	go func() {
		file, err := retrieve("/zdap/config", internal.Resource{Retrieval: "./r.sh"})
		assert.NoError(t, err)
		done <- file
	}()

	select {
	case file := <-done:
		assert.Equal(t, "/zdap/raw/dump", file)
	case <-time.After(5 * time.Second):
		t.Fatal("retrieve blocked while baseCreationMutex was held")
	}
}

func Test_retrieve_serializedAgainstOtherRetrievals(t *testing.T) {
	var mu sync.Mutex
	var inFlight, peak int

	defer stubRunScript(func(string, ...string) (string, error) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
		return "", nil
	})()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := retrieve("/zdap/config", internal.Resource{Retrieval: "./r.sh"})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, peak, "downloads must not stampede")
}

// A failed retrieval must give its prefetch slot back, or a handful of failures
// would wedge every later job. docker and zfs are nil on purpose: the job has to
// return before it reaches either of them.
func Test_CreateBaseAndSnap_failedRetrievalReleasesItsSlot(t *testing.T) {
	defer stubRunScript(func(string, ...string) (string, error) {
		return "", errors.New("retrieval failed")
	})()

	r := internal.Resource{Name: "test", Retrieval: "./r.sh"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < prefetchDepth+3; i++ {
			assert.Error(t, CreateBaseAndSnap(t.TempDir(), r, nil, nil, nil))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a failed retrieval leaked its prefetch slot")
	}
}

// Retrieval runs ahead of restore, but only so far.
func Test_CreateBaseAndSnap_prefetchIsBounded(t *testing.T) {
	started := make(chan struct{}, prefetchDepth+1)
	defer stubRunScript(func(string, ...string) (string, error) {
		started <- struct{}{}
		return "", errors.New("stop before docker")
	})()

	// Stand in for prefetchDepth jobs already in flight.
	for i := 0; i < prefetchDepth; i++ {
		prefetchSlots <- struct{}{}
	}

	failed := make(chan error, 1)
	go func() {
		failed <- CreateBaseAndSnap(t.TempDir(), internal.Resource{Name: "test", Retrieval: "./r.sh"}, nil, nil, nil)
	}()

	select {
	case <-started:
		// drain the slots we took so the goroutine can finish
		for i := 0; i < prefetchDepth; i++ {
			<-prefetchSlots
		}
		t.Fatal("retrieval started with no prefetch slot free")
	case <-time.After(200 * time.Millisecond):
	}

	// Free one slot; the waiting job should now get through.
	<-prefetchSlots
	select {
	case err := <-failed:
		assert.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("job did not proceed once a prefetch slot came free")
	}
	<-prefetchSlots
}

// The regression guard for the whole change: taking baseCreationMutex before
// retrieval, as this used to, puts the download back inside the global lock and
// leaves the pool idle for the length of every job.
func Test_CreateBaseAndSnap_retrievesWithoutWaitingForTheGlobalLock(t *testing.T) {
	started := make(chan struct{}, 1)
	defer stubRunScript(func(string, ...string) (string, error) {
		started <- struct{}{}
		return "", errors.New("stop before docker")
	})()

	baseCreationMutex.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			baseCreationMutex.Unlock()
		}
	}()

	failed := make(chan error, 1)
	go func() {
		failed <- CreateBaseAndSnap(t.TempDir(), internal.Resource{Name: "test", Retrieval: "./r.sh"}, nil, nil, nil)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("retrieval waited for baseCreationMutex")
	}

	baseCreationMutex.Unlock()
	unlocked = true

	select {
	case err := <-failed:
		assert.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
}
