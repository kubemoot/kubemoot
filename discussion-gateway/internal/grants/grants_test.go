package grants

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

const canary = "CANARY-TOKEN-1"

var t0 = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func owner(id string) bus.Principal {
	return bus.Principal{Kind: bus.PrincipalKindSubscriber, Issuer: "oreilly", ID: id}
}

func newStore(max int) (*Memory, *clock) {
	c := &clock{t: t0}
	return NewMemory(max, c.now), c
}

func TestPutThenGetReturnsTheGrant(t *testing.T) {
	s, _ := newStore(0)
	if err := s.Put("c1", New(owner("p1"), "oreilly", canary, t0.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	g, err := s.Get("c1")
	if err != nil || g.Token() != canary || g.Owner.ID != "p1" || g.Verifier != "oreilly" {
		t.Fatalf("got %v, %v", g, err)
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown conversation: %v, want ErrNotFound", err)
	}
}

func TestAnExpiredGrantLosesItsTokenAndIsKeptBrieflyAsExpired(t *testing.T) {
	s, c := newStore(0)
	_ = s.Put("c1", New(owner("p1"), "oreilly", canary, t0.Add(time.Minute)))
	c.t = t0.Add(time.Minute)
	g, err := s.Get("c1")
	if !errors.Is(err, ErrExpired) || g.Token() != "" || g.Owner.ID != "p1" {
		t.Fatalf("at expiry: %v, %v", g, err)
	}
	c.t = t0.Add(time.Minute + expiredKept + time.Second)
	s.Sweep()
	if _, err := s.Get("c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("long after expiry: %v, want ErrNotFound", err)
	}
	if s.Len() != 0 {
		t.Fatalf("%d grants held", s.Len())
	}
}

func TestSweepStripsTheTokenOfAnExpiredGrantNobodyAskedFor(t *testing.T) {
	s, c := newStore(0)
	_ = s.Put("c1", New(owner("p1"), "oreilly", canary, t0.Add(time.Minute)))
	c.t = t0.Add(2 * time.Minute)
	s.Sweep()
	if g := s.grants["c1"]; g.token != "" {
		t.Fatal("the sweep left the token of an expired grant in memory")
	}
}

func TestTheStoreIsBoundedAndFreesRoomFromExpiredGrants(t *testing.T) {
	s, c := newStore(2)
	_ = s.Put("a", New(owner("p"), "v", "ta", t0.Add(time.Minute)))
	_ = s.Put("b", New(owner("p"), "v", "tb", t0.Add(time.Hour)))
	if err := s.Put("c", New(owner("p"), "v", "tc", t0.Add(time.Hour))); !errors.Is(err, ErrFull) {
		t.Fatalf("beyond the bound: %v, want ErrFull", err)
	}
	if err := s.Put("b", New(owner("p"), "v", "tb2", t0.Add(time.Hour))); err != nil {
		t.Fatalf("replacing a held grant is not growth: %v", err)
	}
	c.t = t0.Add(2 * time.Minute)
	if err := s.Put("c", New(owner("p"), "v", "tc", t0.Add(time.Hour))); err != nil {
		t.Fatalf("an expired grant gives its room: %v", err)
	}
	if s.Len() != 2 {
		t.Fatalf("%d grants held, want 2", s.Len())
	}
}

func TestTheDefaultBoundIsOneThousand(t *testing.T) {
	s := NewMemory(0, nil)
	for i := range DefaultMaxGrants {
		if err := s.Put(strconv.Itoa(i), New(owner("p"), "v", "t", time.Now().Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Put("one-more", New(owner("p"), "v", "t", time.Now().Add(time.Hour))); !errors.Is(err, ErrFull) {
		t.Fatalf("the 1001st: %v, want ErrFull", err)
	}
}

func TestDeleteDropsTheGrantAndForgivesAMissingOne(t *testing.T) {
	s, _ := newStore(0)
	_ = s.Put("c1", New(owner("p1"), "v", canary, t0.Add(time.Hour)))
	s.Delete("c1")
	s.Delete("never-held")
	if _, err := s.Get("c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestAGrantNeverPrintsOrMarshalsItsToken(t *testing.T) {
	g := New(owner("p1"), "oreilly", canary, t0)
	type holder struct {
		G Grant
		P *Grant
	}
	h := holder{G: g, P: &g}
	out := make([]string, 0, 24)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		out = append(out, fmt.Sprintf(verb, g), fmt.Sprintf(verb, h), fmt.Sprintf(verb, &g))
	}
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, string(raw), g.String(), g.GoString())
	for _, s := range out {
		if strings.Contains(s, canary) || strings.Contains(s, fmt.Sprintf("%x", canary)) {
			t.Fatalf("the token is in %q", s)
		}
	}
	if !strings.Contains(g.String(), "[redacted]") {
		t.Fatalf("String %q", g.String())
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	s := NewMemory(50, nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 200 {
				id := strconv.Itoa((i*200 + j) % 70)
				_ = s.Put(id, New(owner("p"), "v", "t", time.Now().Add(time.Hour)))
				_, _ = s.Get(id)
				s.Sweep()
				s.Delete(id)
			}
		}()
	}
	wg.Wait()
}

func TestRunSweepsUntilStopped(t *testing.T) {
	s := NewMemory(0, nil)
	_ = s.Put("c1", New(owner("p"), "v", canary, time.Now().Add(-time.Hour)))
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { s.Run(stop, time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.Len() != 0 {
		t.Fatal("the janitor did not forget a long expired grant")
	}
	close(stop)
	<-done
}

func TestPutIfDecidesUnderTheLockAndKeepsOrRefuses(t *testing.T) {
	s, c := newStore(0)
	_ = s.Put("c1", New(owner("p1"), "v", "held", t0.Add(time.Hour)))
	var sawErr error
	keep := func(_ Grant, err error) error { sawErr = err; return ErrKeepHeld }
	if err := s.PutIf("c1", New(owner("p1"), "v", "new", t0.Add(time.Hour)), keep); err != nil || sawErr != nil {
		t.Fatalf("keep: %v %v", err, sawErr)
	}
	if g, _ := s.Get("c1"); g.Token() != "held" {
		t.Fatal("ErrKeepHeld replaced the grant")
	}
	refuse := errors.New("no")
	if err := s.PutIf("c1", New(owner("p1"), "v", "new", t0.Add(time.Hour)), func(Grant, error) error { return refuse }); !errors.Is(err, refuse) {
		t.Fatalf("refuse: %v", err)
	}
	c.t = t0.Add(2 * time.Hour)
	var heldOwner string
	_ = s.PutIf("c1", New(owner("p1"), "v", "new", c.t.Add(time.Hour)), func(h Grant, err error) error {
		sawErr, heldOwner = err, h.Owner.ID
		return nil
	})
	if !errors.Is(sawErr, ErrExpired) || heldOwner != "p1" {
		t.Fatalf("an expired grant is shown with its owner: %v %q", sawErr, heldOwner)
	}
	if g, _ := s.Get("c1"); g.Token() != "new" {
		t.Fatal("PutIf did not write")
	}
}

func TestConcurrentPutIfNeverLetsAnEarlierGrantReplaceALaterOne(t *testing.T) {
	s := NewMemory(0, nil)
	var wg sync.WaitGroup
	for i := 1; i <= 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			end := time.Now().Add(time.Duration(i) * time.Minute)
			_ = s.PutIf("c1", New(owner("p"), "v", strconv.Itoa(i), end), func(h Grant, err error) error {
				if err == nil && !h.Expires.Before(end) {
					return ErrKeepHeld
				}
				return nil
			})
		}()
	}
	wg.Wait()
	if g, _ := s.Get("c1"); g.Token() != "50" {
		t.Fatalf("token %s held, want the latest-ending one, 50", g.Token())
	}
}
