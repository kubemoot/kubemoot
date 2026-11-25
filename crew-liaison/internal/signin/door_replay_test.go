package signin

import (
	"fmt"
	"testing"
	"time"
)

func TestReplayCacheRemembersUntilExpiry(t *testing.T) {
	c := newReplayCache()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	if c.Remember("a", now, now.Add(time.Minute)) {
		t.Fatal("a first sight was reported as seen")
	}
	if !c.Remember("a", now.Add(30*time.Second), now.Add(time.Hour)) || !c.Has("a", now.Add(30*time.Second)) {
		t.Fatal("a second sight was not reported")
	}
	if c.Has("a", now.Add(time.Minute)) {
		t.Error("an expired id is still remembered")
	}
	if c.Remember("a", now.Add(2*time.Minute), now.Add(3*time.Minute)) {
		t.Error("an expired id counted as seen")
	}
}

func TestReplayCacheIsBounded(t *testing.T) {
	c := newReplayCache()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxRemembered+500; i++ {
		c.Remember(fmt.Sprint(i), now, now.Add(time.Duration(i+1)*time.Second))
	}
	if len(c.entries) > maxRemembered {
		t.Fatalf("%d entries kept, the bound is %d", len(c.entries), maxRemembered)
	}
	if !c.Has(fmt.Sprint(maxRemembered+499), now) {
		t.Error("the newest id was dropped before older ones")
	}
}
