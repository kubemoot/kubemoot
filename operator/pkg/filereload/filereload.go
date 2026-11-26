// Package filereload keeps a value built from files and builds it again when any of them changes,
// so a certificate or CA bundle the operator renews is used without a restart. It is the one
// reload-on-change cache of the liaison's and the gateways' TLS settings.
package filereload

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// stamp is what identifies a version of a file: when it changed and how large it is.
type stamp struct {
	modTime time.Time
	size    int64
}

// Loader builds a value from files and rebuilds it when a file's modification time or size changes.
type Loader[T any] struct {
	files []string
	build func() (T, error)
	// replaced, when set, is called with the value that a new one has replaced.
	replaced func(old T)

	mu      sync.Mutex
	stamps  []stamp
	current T
	loaded  bool
}

// New returns a Loader of the value build makes from files. It reads nothing until Get.
func New[T any](build func() (T, error), files ...string) *Loader[T] {
	return &Loader[T]{files: files, build: build}
}

// OnReplace sets a function called with the old value each time a rebuild replaces it, for a
// value that holds resources (idle connections). It returns the Loader.
func (l *Loader[T]) OnReplace(fn func(old T)) *Loader[T] {
	l.replaced = fn
	return l
}

// Get returns the value as the files hold it now. A file that cannot be read, or a build that
// fails, is an error and never the previous value: a caller that trusts the files fails closed.
func (l *Loader[T]) Get() (T, error) {
	var zero T
	stamps := make([]stamp, len(l.files))
	for i, file := range l.files {
		info, err := os.Stat(file)
		if err != nil {
			return zero, fmt.Errorf("%s is not readable: %w", file, err)
		}
		stamps[i] = stamp{info.ModTime(), info.Size()}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.loaded && sameStamps(l.stamps, stamps) {
		return l.current, nil
	}
	next, err := l.build()
	if err != nil {
		return zero, err
	}
	if l.loaded && l.replaced != nil {
		l.replaced(l.current)
	}
	l.current, l.stamps, l.loaded = next, stamps, true
	return next, nil
}

func sameStamps(a, b []stamp) bool {
	for i := range a {
		if !a[i].modTime.Equal(b[i].modTime) || a[i].size != b[i].size {
			return false
		}
	}
	return true
}
