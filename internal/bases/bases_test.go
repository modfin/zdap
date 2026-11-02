package bases

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/mount"
	"github.com/modfin/zdap/internal"
	"github.com/stretchr/testify/assert"
)

func resourceWithBinds(binds ...string) *internal.Resource {
	return &internal.Resource{
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
