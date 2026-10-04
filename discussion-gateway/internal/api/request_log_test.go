package api

import (
	"testing"
	"time"
)

func TestRequestLogNotBeforeIsQueuedTimeLessSkew(t *testing.T) {
	now := time.Date(2026, 9, 27, 6, 0, 0, 0, time.UTC)
	l := newRequestLog(time.Hour)
	l.now = func() time.Time { return now }
	l.record(testConv)
	if got, want := l.notBefore(testConv), now.Add(-clockSkew); !got.Equal(want) {
		t.Fatalf("notBefore = %v, want %v", got, want)
	}
	if got := l.notBefore("never-queued"); !got.IsZero() {
		t.Fatalf("unknown conversation: want zero time, got %v", got)
	}
}

func TestRequestLogKeepsTheLatestTurnAndForgetsOldConversations(t *testing.T) {
	now := time.Date(2026, 9, 27, 6, 0, 0, 0, time.UTC)
	l := newRequestLog(time.Hour)
	l.now = func() time.Time { return now }
	l.record("old")
	l.record(testConv)
	now = now.Add(30 * time.Second)
	l.record(testConv)
	if got, want := l.notBefore(testConv), now.Add(-clockSkew); !got.Equal(want) {
		t.Fatalf("second turn: notBefore = %v, want %v", got, want)
	}
	now = now.Add(2 * time.Hour)
	l.record("conv-2")
	if !l.notBefore("old").IsZero() {
		t.Fatal("a conversation past the time to live should be forgotten")
	}
}
