package bridge

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// bridgeIDPrefix marks the request ids the router hands the server. A reply
// carrying one that has no route answers a session that has gone away; it is
// dropped rather than broadcast, so it cannot reach another session.
const bridgeIDPrefix = "kmb-"

// routeTTL bounds how long a request waits for its reply before its route is
// dropped; a server that never answers must not grow the table without end.
const routeTTL = 15 * time.Minute

// router sends each reply from the MCP server back to the one SSE session whose
// request it answers. Several sessions share one stdio server, and their clients
// choose JSON-RPC ids independently, so two sessions can both send id 1. The router
// rewrites every request id to one unique within the bridge, remembers the session
// and the original id, and restores the original id on the way back. A
// notifications/cancelled refers to a request by its id, so the router rewrites
// its params.requestId the same way.
type router struct {
	next      atomic.Int64
	mu        sync.Mutex
	routes    map[string]route  // bridge id (raw JSON) -> where the reply goes
	byRequest map[string]string // session + client id -> bridge id, for cancellations
	now       func() time.Time
}

type route struct {
	session string
	id      json.RawMessage // the id the client sent
	created time.Time
}

func newRouter() *router {
	return &router{routes: make(map[string]route), byRequest: make(map[string]string), now: time.Now}
}

func requestKey(session string, id json.RawMessage) string {
	return session + "\x00" + string(id)
}

// outbound prepares a message from a session for the server. A request (method and
// id) gets a bridge-unique id, and a cancellation is pointed at that id; anything
// else passes through unchanged, as does every message when the session is unknown.
func (r *router) outbound(session string, body []byte) []byte {
	if session == "" {
		return body
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal(body, &msg) != nil {
		return body
	}
	if string(msg["method"]) == `"notifications/cancelled"` {
		return r.cancellation(session, msg, body)
	}
	id, hasID := msg["id"]
	if _, isRequest := msg["method"]; !isRequest || !hasID || string(id) == "null" {
		return body
	}
	bridgeID := strconv.Quote(bridgeIDPrefix + strconv.FormatInt(r.next.Add(1), 10))
	msg["id"] = json.RawMessage(bridgeID)
	out, err := json.Marshal(msg)
	if err != nil {
		return body
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweep()
	clientID := append(json.RawMessage(nil), id...)
	r.routes[bridgeID] = route{session: session, id: clientID, created: r.now()}
	r.byRequest[requestKey(session, clientID)] = bridgeID
	return out
}

// cancellation rewrites params.requestId from the client's id to the bridge id the
// server knows, when the request is still pending.
func (r *router) cancellation(session string, msg map[string]json.RawMessage, body []byte) []byte {
	var params map[string]json.RawMessage
	if json.Unmarshal(msg["params"], &params) != nil {
		return body
	}
	r.mu.Lock()
	bridgeID, ok := r.byRequest[requestKey(session, params["requestId"])]
	r.mu.Unlock()
	if !ok {
		return body
	}
	params["requestId"] = json.RawMessage(bridgeID)
	rewritten, err := json.Marshal(params)
	if err != nil {
		return body
	}
	msg["params"] = rewritten
	out, err := json.Marshal(msg)
	if err != nil {
		return body
	}
	return out
}

// inbound decides where a message from the server goes. A reply to a routed request
// returns its session and the message with the client's id restored. A reply to a
// bridge id with no route answers a session that is gone and is dropped. Anything
// else returns an empty session, meaning every session receives it.
func (r *router) inbound(line []byte) (session string, out []byte, drop bool) {
	var msg map[string]json.RawMessage
	if json.Unmarshal(line, &msg) != nil {
		return "", line, false
	}
	id, hasID := msg["id"]
	if _, isRequest := msg["method"]; isRequest || !hasID {
		return "", line, false
	}
	r.mu.Lock()
	rt, ok := r.routes[string(id)]
	if ok {
		delete(r.routes, string(id))
		delete(r.byRequest, requestKey(rt.session, rt.id))
	}
	r.mu.Unlock()
	if !ok {
		return "", line, strings.HasPrefix(string(id), `"`+bridgeIDPrefix)
	}
	msg["id"] = rt.id
	restored, err := json.Marshal(msg)
	if err != nil {
		return rt.session, line, false
	}
	return rt.session, restored, false
}

// forget drops the pending routes of a session that has gone away.
func (r *router) forget(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, rt := range r.routes {
		if rt.session == session {
			delete(r.routes, id)
			delete(r.byRequest, requestKey(rt.session, rt.id))
		}
	}
}

// sweep drops routes older than routeTTL. Called with r.mu held.
func (r *router) sweep() {
	cutoff := r.now().Add(-routeTTL)
	for id, rt := range r.routes {
		if rt.created.Before(cutoff) {
			delete(r.routes, id)
			delete(r.byRequest, requestKey(rt.session, rt.id))
		}
	}
}

// pending reports how many requests await a reply.
func (r *router) pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.routes)
}
