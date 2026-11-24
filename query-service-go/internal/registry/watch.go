package registry

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// RescanInterval is how often the registry file and its credentials are read again
// when no change event arrived. It is a safety net for mounts that do not deliver
// events (and the only way a rotated Secret is noticed), not the normal path.
const RescanInterval = 30 * time.Second

// Holder keeps the current snapshot; Load and Swap are safe from any goroutine.
type Holder struct{ p atomic.Pointer[Snapshot] }

// Load returns the current snapshot, or nil before the first successful read.
func (h *Holder) Load() *Snapshot { return h.p.Load() }

// Loader reads the registry file into a Holder and tells OnSwap about each new snapshot.
type Loader struct {
	Path   string
	Env    Env
	Holder *Holder
	// OnSwap runs just before a new snapshot becomes current, with the one it replaces
	// (nil at the first load), so what the snapshot needs (connection pools) exists by the
	// time a request can see it. When it fails the snapshot is not published and the
	// current one stays, as for an invalid file.
	OnSwap func(prev, next *Snapshot) error
	Log    *slog.Logger
	// Rescan overrides RescanInterval when set.
	Rescan time.Duration
}

// Reload reads the file once. An invalid file leaves the current snapshot in place and
// returns the error; an unchanged file does nothing.
func (l *Loader) Reload() error {
	data, err := os.ReadFile(l.Path)
	if err != nil {
		return err
	}
	next, err := Build(data, l.Env)
	if err != nil {
		return err
	}
	prev := l.Holder.Load()
	if prev.Same(next) {
		return nil
	}
	if l.OnSwap != nil {
		if err := l.OnSwap(prev, next); err != nil {
			return err
		}
	}
	l.Holder.p.Store(next)
	l.Log.Info("registry loaded", "version", next.Version, "sources", len(next.Entries))
	return nil
}

// Watch reloads when the file's directory changes (a ConfigMap mount swaps a symlink in
// that directory) and on a slow timer, until ctx ends.
func (l *Loader) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	if err := w.Add(filepath.Dir(l.Path)); err != nil {
		l.Log.Warn("registry directory not watched; relying on the periodic rescan", "error", err)
	}
	tick := time.NewTicker(l.rescanInterval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-w.Events:
			if !l.relevant(ev.Name) {
				continue
			}
		case <-tick.C:
		case err := <-w.Errors:
			l.Log.Warn("registry watch error", "error", err)
			continue
		}
		l.reloadLogged()
	}
}

func (l *Loader) rescanInterval() time.Duration {
	if l.Rescan > 0 {
		return l.Rescan
	}
	return RescanInterval
}

func (l *Loader) reloadLogged() {
	if err := l.Reload(); err != nil {
		l.Log.Error("registry reload failed; keeping the current registry", "error", err)
	}
}

// kubeletSwap is the prefix of the entries a Kubernetes volume update swaps in the mount
// directory (..data and its temporary), which is how a ConfigMap file changes.
const kubeletSwap = "..data"

// relevant reports whether a change to name in the registry's directory can be a change to
// the registry: the file itself, or a kubelet volume swap. Other files in the directory (a
// log, a sibling) must not cause a reload.
func (l *Loader) relevant(name string) bool {
	base := filepath.Base(name)
	return base == filepath.Base(l.Path) || strings.HasPrefix(base, kubeletSwap)
}
