package bridge

import (
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
)

// router sends each reply from the MCP server back to the one SSE session whose
// request it answers. Several sessions share one stdio server, and their clients
// choose JSON-RPC ids independently, so two sessions can both send id 1. The router
// rewrites every request id to one unique within the bridge, remembers the session
// and the original id, and restores the original id on the way back.
type router struct {
	next   atomic.Int64
	mu     sync.Mutex
	routes map[string]route // rewritten id (raw JSON) -> where the reply goes
}

type route struct {
	session string
	id      json.RawMessage // the id the client sent
}

func newRouter() *router {
	return &router{routes: make(map[string]route)}
}

// outbound prepares a message from a session for the server. A request (method and
// id) gets a bridge-unique id; anything else passes through unchanged, as does
// every message when the session is unknown.
func (r *router) outbound(session string, body []byte) []byte {
	if session == "" {
		return body
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal(body, &msg) != nil {
		return body
	}
	id, hasID := msg["id"]
	if _, isRequest := msg["method"]; !isRequest || !hasID || string(id) == "null" {
		return body
	}
	bridgeID := strconv.Quote("kmb-" + strconv.FormatInt(r.next.Add(1), 10))
	msg["id"] = json.RawMessage(bridgeID)
	out, err := json.Marshal(msg)
	if err != nil {
		return body
	}
	r.mu.Lock()
	r.routes[bridgeID] = route{session: session, id: append(json.RawMessage(nil), id...)}
	r.mu.Unlock()
	return out
}

// inbound decides where a message from the server goes. A reply to a routed request
// returns its session and the message with the client's id restored; anything else
// returns an empty session, meaning every session receives it.
func (r *router) inbound(line []byte) (string, []byte) {
	var msg map[string]json.RawMessage
	if json.Unmarshal(line, &msg) != nil {
		return "", line
	}
	id, hasID := msg["id"]
	if _, isRequest := msg["method"]; isRequest || !hasID {
		return "", line
	}
	r.mu.Lock()
	rt, ok := r.routes[string(id)]
	delete(r.routes, string(id))
	r.mu.Unlock()
	if !ok {
		return "", line
	}
	msg["id"] = rt.id
	out, err := json.Marshal(msg)
	if err != nil {
		return rt.session, line
	}
	return rt.session, out
}

// forget drops the pending routes of a session that has gone away.
func (r *router) forget(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, rt := range r.routes {
		if rt.session == session {
			delete(r.routes, id)
		}
	}
}

// pending reports how many requests await a reply.
func (r *router) pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.routes)
}
