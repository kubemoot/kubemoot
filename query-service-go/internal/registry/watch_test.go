package registry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type swaps struct {
	mu   sync.Mutex
	seen [][2]*Snapshot
	ch   chan struct{}
}

func newSwaps() *swaps { return &swaps{ch: make(chan struct{}, 16)} }

func (s *swaps) record(prev, next *Snapshot) error {
	s.mu.Lock()
	s.seen = append(s.seen, [2]*Snapshot{prev, next})
	s.mu.Unlock()
	s.ch <- struct{}{}
	return nil
}

func (s *swaps) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func newLoader(t *testing.T) (*Loader, *swaps, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.json")
	write(t, path, goodRegistry)
	sw := newSwaps()
	return &Loader{Path: path, Holder: &Holder{}, OnSwap: sw.record, Log: quietLog()}, sw, path
}

func TestReloadSwapsAndSkipsAnUnchangedFile(t *testing.T) {
	l, sw, _ := newLoader(t)
	if l.Holder.Load() != nil {
		t.Fatal("a holder starts empty")
	}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	first := l.Holder.Load()
	if first == nil || sw.seen[0][0] != nil {
		t.Fatal("the first load has no previous snapshot")
	}
	if err := l.Reload(); err != nil || sw.count() != 1 || l.Holder.Load() != first {
		t.Fatalf("an unchanged file must not swap: %v, %d swaps", err, sw.count())
	}
}

func TestReloadKeepsTheCurrentRegistryWhenTheFileIsInvalid(t *testing.T) {
	l, sw, path := newLoader(t)
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	good := l.Holder.Load()
	write(t, path, `{"sources":[{"name":"BAD"}]}`)
	if err := l.Reload(); err == nil {
		t.Fatal("expected an error")
	}
	if l.Holder.Load() != good || sw.count() != 1 {
		t.Fatal("an invalid file replaced the registry")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := l.Reload(); err == nil || l.Holder.Load() != good {
		t.Fatal("a missing file must keep the registry")
	}
}

func TestReloadReportsThePreviousSnapshot(t *testing.T) {
	l, sw, path := newLoader(t)
	_ = l.Reload()
	write(t, path, strings.Replace(goodRegistry, `"docs-v2"`, `"docs-v3"`, 1))
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	prev, next := sw.seen[1][0], sw.seen[1][1]
	if prev == nil || prev.Entries["docs"].VectorStore.Collection != "docs-v2" || next.Entries["docs"].VectorStore.Collection != "docs-v3" {
		t.Fatalf("prev %+v next %+v", prev, next)
	}
}

func TestWatchSwapsWhenTheFileIsReplacedAtomically(t *testing.T) {
	l, sw, path := newLoader(t)
	_ = l.Reload()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Watch(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-sw.ch // the first load

	// A ConfigMap mount replaces the file by renaming a new one over it.
	tmp := path + ".new"
	write(t, tmp, strings.Replace(goodRegistry, `"docs-v2"`, `"docs-v4"`, 1))
	// The watcher may still be registering; keep replacing until it notices.
	deadline := time.After(10 * time.Second)
	for {
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
		select {
		case <-sw.ch:
			if got := l.Holder.Load().Entries["docs"].VectorStore.Collection; got != "docs-v4" {
				t.Fatalf("collection %s", got)
			}
			return
		case <-time.After(200 * time.Millisecond):
			write(t, tmp, strings.Replace(goodRegistry, `"docs-v2"`, `"docs-v4"`, 1))
		case <-deadline:
			t.Fatal("the watcher never saw the new file")
		}
	}
}

func TestWatchFallsBackToTheRescanWhenTheDirectoryCannotBeWatched(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "later")
	l, sw, _ := newLoader(t)
	l.Path = filepath.Join(dir, "registry.json")
	l.Rescan = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Watch(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	// The directory does not exist yet; once the file appears the rescan loads it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, l.Path, goodRegistry)
	select {
	case <-sw.ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the rescan never loaded the file")
	}
}

func TestOnSwapRunsBeforeTheNewSnapshotIsCurrent(t *testing.T) {
	l, _, _ := newLoader(t)
	var current *Snapshot
	l.OnSwap = func(_, _ *Snapshot) error { current = l.Holder.Load(); return nil }
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	if current != nil || l.Holder.Load() == nil {
		t.Fatalf("a request must not see a snapshot whose pools are not open yet: during the hook %v", current)
	}
}

func TestAFailingOnSwapLeavesTheCurrentSnapshotAndRetriesOnTheNextReload(t *testing.T) {
	l, _, path := newLoader(t)
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	good := l.Holder.Load()
	fail := true
	l.OnSwap = func(_, _ *Snapshot) error {
		if fail {
			return errors.New("pools not opened")
		}
		return nil
	}
	write(t, path, strings.Replace(goodRegistry, `"docs-v2"`, `"docs-v5"`, 1))
	if err := l.Reload(); err == nil || l.Holder.Load() != good {
		t.Fatal("a failed hook must keep the current snapshot")
	}
	fail = false
	if err := l.Reload(); err != nil || l.Holder.Load() == good {
		t.Fatalf("the same file must be tried again: %v", err)
	}
}

func TestClampTopK(t *testing.T) {
	for in, want := range map[int]int{-3: 1, 0: 1, 7: 7, 100: 100, 500: 100} {
		if got := ClampTopK(in); got != want {
			t.Errorf("ClampTopK(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestOnlyTheRegistryFileAndAKubeletSwapCauseAReload(t *testing.T) {
	l := &Loader{Path: "/etc/kubemoot/query/registry.json"}
	for name, want := range map[string]bool{
		"/etc/kubemoot/query/registry.json":     true,
		"/etc/kubemoot/query/..data":            true,
		"/etc/kubemoot/query/..data_tmp":        true,
		"/etc/kubemoot/query/app.log":           false,
		"/etc/kubemoot/query/registry.json.new": false,
		"/etc/kubemoot/query/..2026_11_23":      false,
	} {
		if got := l.relevant(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}
