package pgstore

import (
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

// The pool lifecycle needs no database: pools connect on first use, and these tests never
// use one.

func snapshotOf(conns ...registry.Conn) *registry.Snapshot {
	entries := map[string]*registry.Entry{}
	for i, c := range conns {
		entries[string(rune('a'+i))] = &registry.Entry{Conn: c}
	}
	return &registry.Snapshot{Entries: entries}
}

func connTo(db string) registry.Conn {
	return registry.Conn{Host: loopback, Port: 1, DBName: db, User: "u"}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	s := New()
	c := &clock{t: time.Unix(1000, 0)}
	s.now = c.now
	t.Cleanup(s.Close)
	return s, c
}

func TestARequestThatStartedBeforeASwapStillFindsItsPool(t *testing.T) {
	s, c := newStore(t)
	old, next := connTo("old"), connTo("new")
	if err := s.Sync(snapshotOf(old)); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(snapshotOf(next)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool(old); err != nil {
		t.Fatalf("the retired pool must serve a request in flight: %v", err)
	}
	c.t = c.t.Add(RetireGrace - time.Second)
	if _, err := s.pool(old); err != nil {
		t.Fatalf("still within the grace period: %v", err)
	}
	c.t = c.t.Add(2 * time.Second)
	if _, err := s.pool(old); err == nil {
		t.Fatal("after the grace period the pool is closed and gone")
	}
	if _, err := s.pool(next); err != nil {
		t.Fatal(err)
	}
}

func TestAConnectionThatComesBackTakesItsRetiredPoolBack(t *testing.T) {
	s, _ := newStore(t)
	a, b := connTo("a"), connTo("b")
	_ = s.Sync(snapshotOf(a))
	first, _ := s.pool(a)
	_ = s.Sync(snapshotOf(b))
	_ = s.Sync(snapshotOf(a))
	again, err := s.pool(a)
	if err != nil || again != first {
		t.Fatalf("the same pool must be reused, got %v %v", again, err)
	}
	if len(s.retired) != 1 || len(s.pools) != 1 {
		t.Fatalf("pools %d retired %d", len(s.pools), len(s.retired))
	}
}

func TestASyncThatFailsChangesNothing(t *testing.T) {
	s, _ := newStore(t)
	good := connTo("good")
	if err := s.Sync(snapshotOf(good)); err != nil {
		t.Fatal(err)
	}
	bad := registry.Conn{Host: loopback, Port: 70000, DBName: "x", User: "u"}
	if err := s.Sync(snapshotOf(connTo("other"), bad)); err == nil {
		t.Fatal("a connection that cannot be parsed must fail the sync")
	}
	if _, err := s.pool(good); err != nil || len(s.pools) != 1 || len(s.retired) != 0 {
		t.Fatalf("the previous pools must be as they were: %v, %d active, %d retired", err, len(s.pools), len(s.retired))
	}
}

func TestCloseReleasesActiveAndRetiredPools(t *testing.T) {
	s, _ := newStore(t)
	_ = s.Sync(snapshotOf(connTo("a")))
	_ = s.Sync(snapshotOf(connTo("b")))
	s.Close()
	if len(s.pools) != 0 || len(s.retired) != 0 {
		t.Fatalf("%d active, %d retired", len(s.pools), len(s.retired))
	}
}

// loopback is a host that refuses connections on the ports these tests name.
const loopback = "127.0.0.1"
