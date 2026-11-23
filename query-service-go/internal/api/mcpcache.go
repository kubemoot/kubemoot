package api

import (
	"net/http"
	"sync"

	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

// mcpCache keeps one MCP handler per source of the current registry snapshot, so a tool
// call does not rebuild the server and its schema. It starts over when the snapshot
// changes.
type mcpCache struct {
	mu       sync.Mutex
	snapshot *registry.Snapshot
	handlers map[*registry.Entry]http.Handler
}

// handlerFor returns the handler of an entry, building it with build the first time the
// snapshot is seen with it.
func (c *mcpCache) handlerFor(snap *registry.Snapshot, e *registry.Entry, build func() http.Handler) http.Handler {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snapshot != snap || c.handlers == nil {
		c.snapshot, c.handlers = snap, map[*registry.Entry]http.Handler{}
	}
	h, ok := c.handlers[e]
	if !ok {
		h = build()
		c.handlers[e] = h
	}
	return h
}
