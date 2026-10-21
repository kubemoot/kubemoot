package bus

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestForgetLetsAMessageRunAgainAndGivesBackItsPermit(t *testing.T) {
	a := &Admission{Agent: "me", Role: roleTooler}
	for i := 0; i < maxPerThread; i++ {
		a.Judge(msg(string(rune('a'+i)), "reply", "human", "t"))
	}
	if v := a.Judge(msg("d", "reply", "human", "t")); v != Limited {
		t.Fatalf("the thread is at its limit: %d", v)
	}
	a.Forget("a", "t")
	if v := a.Judge(msg("a", "reply", "human", "t")); v != Run {
		t.Fatalf("the forgotten message runs again and its permit was given back: %d", v)
	}
	a.Forget("never-seen", "other") // forgetting what was never admitted changes nothing
}

func TestTruncateCutsOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("é", 10) // two bytes each
	got := truncate(s, 5)
	if !utf8.ValidString(got) || len(got) > 5 {
		t.Fatalf("%q", got)
	}
	if truncate("short", 100) != "short" {
		t.Error("a short string is returned whole")
	}
}
