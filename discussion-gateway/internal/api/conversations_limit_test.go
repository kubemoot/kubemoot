package api

import (
	"testing"
	"time"
)

func TestNewestFirstWhereBoundsTheLimit(t *testing.T) {
	idx := conversationIndex{}
	for i, id := range []string{"a", "b", "c"} {
		idx[id] = &conversationAcc{id: id, last: time.Unix(int64(i), 0)}
	}
	cases := map[int]int{-5: 0, 0: 0, 2: 2, 3: 3, maxConversationLimit + 1000: 3}
	for limit, want := range cases {
		if got := len(idx.newestFirstWhere(limit, nil)); got != want {
			t.Errorf("limit %d: got %d conversations, want %d", limit, got, want)
		}
	}
}
