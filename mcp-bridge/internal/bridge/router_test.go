package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func field(t *testing.T, raw []byte, name string) string {
	t.Helper()
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	return string(msg[name])
}

func TestOutboundRewritesARequestIDAndInboundRestoresIt(t *testing.T) {
	r := newRouter()
	out := r.outbound("s1", []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x"}}`))
	bridgeID := field(t, out, "id")
	if bridgeID == "7" || !strings.HasPrefix(bridgeID, `"kmb-`) {
		t.Fatalf("id not rewritten: %s", out)
	}
	if field(t, out, "params") != `{"name":"x"}` {
		t.Fatalf("params changed: %s", out)
	}
	session, back, _ := r.inbound([]byte(`{"jsonrpc":"2.0","id":` + bridgeID + `,"result":{"ok":true}}`))
	if session != "s1" || field(t, back, "id") != "7" || field(t, back, "result") != `{"ok":true}` {
		t.Fatalf("got session %q, %s", session, back)
	}
	if r.pending() != 0 {
		t.Fatalf("route not cleared after its reply")
	}
}

func TestOutboundPassesThroughWhatIsNotARoutableRequest(t *testing.T) {
	r := newRouter()
	cases := map[string]struct{ session, body string }{
		"notification":        {"s1", `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		"reply to the server": {"s1", `{"jsonrpc":"2.0","id":3,"result":{}}`},
		"null id":             {"s1", `{"jsonrpc":"2.0","id":null,"method":"ping"}`},
		"no session":          {"", `{"jsonrpc":"2.0","id":1,"method":"ping"}`},
		"not an object":       {"s1", `[1,2,3]`},
	}
	for name, c := range cases {
		if got := string(r.outbound(c.session, []byte(c.body))); got != c.body {
			t.Errorf("%s: changed to %s", name, got)
		}
	}
	if r.pending() != 0 {
		t.Fatalf("pass-through messages must not create routes, got %d", r.pending())
	}
}

func TestTwoSessionsUsingTheSameIDEachGetTheirOwnReply(t *testing.T) {
	r := newRouter()
	a := field(t, r.outbound("a", []byte(`{"id":1,"method":"tools/call"}`)), "id")
	b := field(t, r.outbound("b", []byte(`{"id":1,"method":"tools/call"}`)), "id")
	if a == b {
		t.Fatalf("both sessions' requests got id %s", a)
	}
	sb, replyB, _ := r.inbound([]byte(`{"id":` + b + `,"result":"for b"}`))
	sa, replyA, _ := r.inbound([]byte(`{"id":` + a + `,"result":"for a"}`))
	if sa != "a" || field(t, replyA, "result") != `"for a"` || field(t, replyA, "id") != "1" {
		t.Fatalf("a got %q %s", sa, replyA)
	}
	if sb != "b" || field(t, replyB, "result") != `"for b"` || field(t, replyB, "id") != "1" {
		t.Fatalf("b got %q %s", sb, replyB)
	}
}

func TestInboundBroadcastsWhatItCannotRoute(t *testing.T) {
	r := newRouter()
	for _, line := range []string{
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		`{"jsonrpc":"2.0","id":"srv-1","method":"sampling/createMessage"}`,
		`{"jsonrpc":"2.0","id":"unknown","result":{}}`,
		`not json`,
	} {
		if session, out, drop := r.inbound([]byte(line)); session != "" || drop || string(out) != line {
			t.Errorf("%s: routed to %q (drop %v) as %s", line, session, drop, out)
		}
	}
}

func TestForgetDropsOnlyThatSessionsRoutes(t *testing.T) {
	r := newRouter()
	r.outbound("gone", []byte(`{"id":1,"method":"a"}`))
	r.outbound("gone", []byte(`{"id":2,"method":"b"}`))
	kept := field(t, r.outbound("here", []byte(`{"id":1,"method":"c"}`)), "id")
	r.forget("gone")
	if r.pending() != 1 {
		t.Fatalf("want 1 pending route, got %d", r.pending())
	}
	if session, _, _ := r.inbound([]byte(`{"id":` + kept + `,"result":{}}`)); session != "here" {
		t.Fatalf("the remaining session lost its route: %q", session)
	}
}

// The whole path through the bridge: a POST from one session reaches the server
// with a rewritten id, and the server's reply reaches that session only.
func TestBridgeDeliversAReplyOnlyToTheAskingSession(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")
	stdin := &strings.Builder{}
	b.stdin = nopWriteCloser{stdin}
	b.connected.Store(true)
	chA, chB := make(chan []byte, 4), make(chan []byte, 4)
	b.clients["a"], b.clients["b"] = chA, chB

	req := httptest.NewRequest(http.MethodPost, "/message?sessionId=a",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	rec := httptest.NewRecorder()
	b.handleMessage(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST status %d", rec.Code)
	}
	sent := strings.TrimSpace(stdin.String())
	bridgeID := field(t, []byte(sent), "id")

	reply := `{"jsonrpc":"2.0","id":` + bridgeID + `,"result":{"protocolVersion":"2024-11-05"}}` + "\n"
	if err := b.readStdoutPipe(strings.NewReader(reply), 1<<20); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-chA:
		if field(t, got, "id") != "1" {
			t.Fatalf("session a got id %s", field(t, got, "id"))
		}
	default:
		t.Fatal("session a got no reply")
	}
	if len(chB) != 0 {
		t.Fatalf("session b received a reply meant for a: %s", <-chB)
	}
	if !b.initialized.Load() {
		t.Fatal("an initialize answered through a rewritten id must still mark the server ready")
	}
}

type nopWriteCloser struct{ *strings.Builder }

func (nopWriteCloser) Close() error { return nil }

func TestAReplyForAForgottenSessionIsDroppedNotBroadcast(t *testing.T) {
	r := newRouter()
	id := field(t, r.outbound("gone", []byte(`{"id":1,"method":"tools/call"}`)), "id")
	r.forget("gone")
	session, _, drop := r.inbound([]byte(`{"id":` + id + `,"result":"late"}`))
	if !drop || session != "" {
		t.Fatalf("want dropped, got session %q drop %v", session, drop)
	}
}

func TestACancellationFollowsItsRequest(t *testing.T) {
	r := newRouter()
	bridgeID := field(t, r.outbound("s1", []byte(`{"id":5,"method":"tools/call"}`)), "id")
	out := r.outbound("s1", []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5,"reason":"user"}}`))
	var params map[string]json.RawMessage
	if err := json.Unmarshal([]byte(field(t, out, "params")), &params); err != nil {
		t.Fatal(err)
	}
	if string(params["requestId"]) != bridgeID || string(params["reason"]) != `"user"` {
		t.Fatalf("cancellation not rewritten: %s", out)
	}
	other := `{"method":"notifications/cancelled","params":{"requestId":5}}`
	if got := string(r.outbound("s2", []byte(other))); got != other {
		t.Fatalf("another session's cancellation must not touch s1's request: %s", got)
	}
	unknown := `{"method":"notifications/cancelled","params":{"requestId":99}}`
	if got := string(r.outbound("s1", []byte(unknown))); got != unknown {
		t.Fatalf("a cancellation for no pending request passes through: %s", got)
	}
}

func TestRoutesExpire(t *testing.T) {
	r := newRouter()
	start := time.Now()
	r.now = func() time.Time { return start }
	r.outbound("s1", []byte(`{"id":1,"method":"tools/call"}`))
	r.now = func() time.Time { return start.Add(routeTTL + time.Minute) }
	r.outbound("s1", []byte(`{"id":2,"method":"tools/call"}`))
	if r.pending() != 1 {
		t.Fatalf("want only the fresh route, got %d", r.pending())
	}
}

func TestBridgeRejectsABatch(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")
	b.stdin = nopWriteCloser{&strings.Builder{}}
	b.connected.Store(true)
	req := httptest.NewRequest(http.MethodPost, "/message?sessionId=a",
		strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`))
	rec := httptest.NewRecorder()
	b.handleMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}
