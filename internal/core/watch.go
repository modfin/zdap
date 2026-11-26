package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// resourceFileSuffix marks the files that define a resource. The retrieval and
// creation scripts beside them are read by path when a job runs, so a change
// to one of those needs no reload at all.
const resourceFileSuffix = ".resource.yml"

const (
	// defaultDebounce is how long the config directory has to be quiet before
	// a reload is attempted. One save emits several events, and a resource's
	// yml and its scripts are usually edited together, so acting on the first
	// event would mean reloading halfway through an edit.
	defaultDebounce = 2 * time.Second

	// defaultBackstop is how often the files are re-read even if the watch has
	// said nothing. inotify does not reach every filesystem -- a bind mount or
	// a network mount can stay silent -- and a reload that finds no change
	// does nothing, so this costs one directory read.
	defaultBackstop = 5 * time.Minute
)

func isResourceFile(path string) bool {
	return strings.HasSuffix(path, resourceFileSuffix)
}

// WatchConfigDir reloads the resources whenever a resource file in the config
// directory changes, and returns when ctx is done.
//
// Errors from an individual reload are logged rather than returned: a bad file
// on disk should not take the watch down with it, and the running
// configuration is kept until the file parses again. Only a watch that cannot
// be set up at all is returned as an error.
func (c *Core) WatchConfigDir(ctx context.Context) error {
	return c.watchConfigDir(ctx, defaultDebounce, defaultBackstop, nil)
}

// started, when not nil, is closed once the watch is established. Tests use it
// to avoid writing a file into the window before the watch exists.
func (c *Core) watchConfigDir(ctx context.Context, debounce, backstop time.Duration, started chan<- struct{}) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("could not create a watcher for %s, %w", c.configDir, err)
	}
	defer w.Close()

	// The directory is watched, not the files in it. An editor saving, and a
	// deploy that writes a temp file and renames it into place, both replace
	// the inode; a watch held on the old inode goes quiet without ever saying
	// so, which is the worst way for this to fail.
	if err := w.Add(c.configDir); err != nil {
		return fmt.Errorf("could not watch %s, %w", c.configDir, err)
	}
	fmt.Println("[CORE] Watching", c.configDir, "for resource changes")
	if started != nil {
		close(started)
	}

	// The files may have changed between being loaded at startup and the watch
	// being established. Nothing would report that until the backstop, so
	// check once here; it is a no-op when nothing has changed.
	c.reloadAndLog("watch started")

	quiet := time.NewTimer(debounce)
	stopTimer(quiet)
	defer stopTimer(quiet)

	backstopTick := time.NewTicker(backstop)
	defer backstopTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			fmt.Println("[CORE] Error watching", c.configDir+":", err)

		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if !isResourceFile(ev.Name) {
				continue
			}
			// Chmod is left out: it arrives for things that do not change
			// content, and the backstop covers anything missed.
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			stopTimer(quiet)
			quiet.Reset(debounce)

		case <-quiet.C:
			c.reloadAndLog("resource files changed")

		case <-backstopTick.C:
			c.reloadAndLog("periodic check")
		}
	}
}

func (c *Core) reloadAndLog(why string) {
	changed, err := c.Reload()
	switch {
	case err != nil:
		fmt.Println("[CORE] Error:", err)
	case changed:
		fmt.Println("[CORE] Reloaded resources from", c.configDir, "("+why+")")
	}
}

// stopTimer leaves t stopped and its channel empty, so that it can be Reset
// whether or not it had already fired.
func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}
