// Package filereload keeps a value built from files and builds it again when any of them changes,
// so a certificate or CA bundle the operator renews is used without a restart. It is the one
// reload-on-change cache of the liaison's and the gateways' TLS settings.
package filereload

import (
	"bytes"
	"fmt"
	"os"
	"sync"
)

// Loader builds a value from the contents of files and rebuilds it when any content changes.
// A change is found by comparing contents, so a rewrite of the same size and time is seen.
type Loader[T any] struct {
	files []string
	build func(contents [][]byte) (T, error)
	// replaced, when set, is called with the value that a new one has replaced.
	replaced func(old T)

	mu      sync.Mutex
	last    [][]byte
	current T
	loaded  bool
}

// New returns a Loader of the value build makes from the contents of files, in order. It reads
// nothing until Get.
func New[T any](build func(contents [][]byte) (T, error), files ...string) *Loader[T] {
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
// A build that fails is tried once more on a fresh read, because files that an atomic swap
// replaces one after the other can be read between the two.
func (l *Loader[T]) Get() (T, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	contents, err := l.read()
	if err != nil {
		var zero T
		return zero, err
	}
	if l.loaded && equal(l.last, contents) {
		return l.current, nil
	}
	next, err := l.build(contents)
	if err != nil {
		if contents, err = l.read(); err == nil {
			next, err = l.build(contents)
		}
		if err != nil {
			var zero T
			return zero, err
		}
	}
	if l.loaded && l.replaced != nil {
		l.replaced(l.current)
	}
	l.current, l.last, l.loaded = next, contents, true
	return next, nil
}

func (l *Loader[T]) read() ([][]byte, error) {
	out := make([][]byte, len(l.files))
	for i, file := range l.files {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s is not readable: %w", file, err)
		}
		out[i] = b
	}
	return out, nil
}

func equal(a, b [][]byte) bool {
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}
