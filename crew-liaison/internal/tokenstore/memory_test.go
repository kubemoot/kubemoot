package tokenstore

import (
	"context"
	"testing"
	"time"
)

func TestMemoryMeetsTheStoreContract(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := NewMemory(func() time.Time { return now })
	contract(t, m, func(d time.Duration) { now = now.Add(d) })
	refusals(t, m)
}

func TestMemoryKeepsItsOwnCopyOfAValue(t *testing.T) {
	m := NewMemory(nil)
	ctx := context.Background()
	value := []byte("abc")
	if err := m.Put(ctx, "k", value, time.Hour); err != nil {
		t.Fatal(err)
	}
	value[0] = 'X'
	got, _ := m.Get(ctx, "k")
	got[1] = 'Y'
	if again, _ := m.Get(ctx, "k"); string(again) != "abc" {
		t.Errorf("the kept value changed with the caller's slice: %q", again)
	}
}

func TestMemorySweepDropsOnlyExpiredValues(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := NewMemory(func() time.Time { return now })
	ctx := context.Background()
	_ = m.Put(ctx, "gone", []byte("v"), time.Minute)
	_ = m.Put(ctx, "kept", []byte("v"), time.Hour)
	now = now.Add(2 * time.Minute)
	m.Sweep()
	if len(m.values) != 1 {
		t.Fatalf("after the sweep: %d values", len(m.values))
	}
	if _, err := m.Get(ctx, "kept"); err != nil {
		t.Errorf("the sweep dropped a live value: %v", err)
	}
}
