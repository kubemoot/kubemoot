package api

import (
	"net/http"
	"testing"

	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

func TestMCPCacheBuildsOncePerEntryAndStartsOverOnANewSnapshot(t *testing.T) {
	var c mcpCache
	built := 0
	build := func() http.Handler { built++; return http.NotFoundHandler() }
	snap1, snap2 := &registry.Snapshot{}, &registry.Snapshot{}
	e1, e2 := &registry.Entry{}, &registry.Entry{}

	a := c.handlerFor(snap1, e1, build)
	if again := c.handlerFor(snap1, e1, build); built != 1 || again == nil {
		t.Fatalf("the same entry must reuse its handler, built %d", built)
	}
	c.handlerFor(snap1, e2, build)
	if built != 2 {
		t.Fatalf("another entry gets its own handler, built %d", built)
	}
	c.handlerFor(snap2, e1, build)
	if built != 3 || len(c.handlers) != 1 {
		t.Fatalf("a new snapshot starts over: built %d, cached %d", built, len(c.handlers))
	}
	_ = a
}

func TestAnMCPCallUsesTheCachedServerAcrossCalls(t *testing.T) {
	r := newRig(t)
	for range 3 {
		if isErr, text := r.callTool(t, `{"query":"q"}`); isErr {
			t.Fatal(text)
		}
	}
	if len(r.h.mcpCache.handlers) != 1 {
		t.Fatalf("cached %d handlers", len(r.h.mcpCache.handlers))
	}
}
