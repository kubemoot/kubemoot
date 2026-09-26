package liaison

import (
	"testing"
	"time"
)

func TestTicketLifecycle(t *testing.T) {
	s := NewStore(time.Hour)
	tk := s.Create("hello", "hello", "conv-1", "q")
	if v := tk.Snapshot(); v.State != StatePending || v.Crew != "hello" || v.Contributions != 0 {
		t.Fatalf("new ticket: %+v", v)
	}
	if _, ok := s.Get(tk.ID); !ok {
		t.Fatal("ticket not stored")
	}
	tk.Contribution(time.Now())
	tk.Contribution(time.Now())
	tk.Answer("Paris.", time.Now())
	tk.Fail("too late", time.Now()) // ignored after Answer
	v := tk.Snapshot()
	if v.State != StateAnswered || v.Answer != "Paris." || v.Error != "" || v.Contributions != 2 {
		t.Fatalf("settled ticket: %+v", v)
	}
	select {
	case <-tk.Done():
	default:
		t.Fatal("Done not closed after Answer")
	}
}

func TestTicketFail(t *testing.T) {
	s := NewStore(time.Hour)
	tk := s.Create("hello", "hello", "conv-1", "q")
	tk.Fail("stream ended", time.Now())
	tk.Answer("late", time.Now()) // ignored after Fail
	if v := tk.Snapshot(); v.State != StateFailed || v.Error != "stream ended" || v.Answer != "" {
		t.Fatalf("failed ticket: %+v", v)
	}
}

func TestStoreSweep(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s := NewStore(time.Hour)
	s.now = func() time.Time { return now }
	settled := s.Create("a", "a", "c1", "q")
	settled.Answer("x", now)
	pending := s.Create("b", "b", "c2", "q")
	fresh := s.Create("c", "c", "c3", "q")

	now = now.Add(90 * time.Minute) // settled expired (1h), pending not (2h)
	fresh.Contribution(now)
	if got := s.Sweep(); got != 1 {
		t.Fatalf("sweep removed %d, want 1", got)
	}
	if _, ok := s.Get(settled.ID); ok {
		t.Fatal("settled ticket survived the sweep")
	}
	if _, ok := s.Get(pending.ID); !ok {
		t.Fatal("pending ticket removed too early")
	}
	now = now.Add(60 * time.Minute) // pending now past 2h
	if got := s.Sweep(); got != 1 {
		t.Fatalf("second sweep removed %d, want 1", got)
	}
	if s.Len() != 1 {
		t.Fatalf("store holds %d tickets, want 1 (the fresh one)", s.Len())
	}
}

func TestStoreGetUnknown(t *testing.T) {
	s := NewStore(time.Hour)
	if _, ok := s.Get("nope"); ok {
		t.Fatal("unknown ticket found")
	}
}

func TestStoreFindPending(t *testing.T) {
	s := NewStore(time.Hour)
	a := s.Create("hello", "hello", "c1", "what?")
	if got, ok := s.FindPending("hello", "hello", "what?"); !ok || got != a {
		t.Fatal("pending ticket for the same question not found")
	}
	if _, ok := s.FindPending("hello", "hello", "other?"); ok {
		t.Fatal("different question matched")
	}
	if _, ok := s.FindPending("hello", "elsewhere", "what?"); ok {
		t.Fatal("different namespace matched")
	}
	a.Answer("x", time.Now())
	if _, ok := s.FindPending("hello", "hello", "what?"); ok {
		t.Fatal("settled ticket returned as pending")
	}
}
