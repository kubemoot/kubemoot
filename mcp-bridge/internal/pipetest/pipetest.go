// Package pipetest holds helpers shared by the bridge's tests.
package pipetest

import (
	"os"
	"testing"
	"time"
)

// OpenDeadline bounds how long a test waits for the other end of a FIFO.
const OpenDeadline = 15 * time.Second

// Open opens a FIFO end, which blocks until the other end is opened. The
// deadline stops a peer that never opens its end from hanging the test; the
// file is closed when the test ends.
func Open(t testing.TB, path string, flag int) *os.File {
	t.Helper()
	type result struct {
		f   *os.File
		err error
	}
	opened := make(chan result, 1)
	go func() {
		f, err := os.OpenFile(path, flag, 0)
		opened <- result{f, err}
	}()
	select {
	case r := <-opened:
		if r.err != nil {
			t.Fatal(r.err)
		}
		t.Cleanup(func() { _ = r.f.Close() })
		return r.f
	case <-time.After(OpenDeadline):
		t.Fatalf("the other end of %s was never opened", path)
		return nil
	}
}
