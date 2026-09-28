package bridge

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReady_FlipsAfterInitializeResponse pins the JIT-readiness contract
// the Kubernetes startupProbe + readinessProbe rely on. Without this gate,
// the Service would route tool calls to a pod whose pipes are connected
// but whose MCP server is still initialising — the exact race observed
// 2026-05-25 with FastMCP returning "Received request before initialization
// was complete" during the ~45s cold-start window.
func TestReady_FlipsAfterInitializeResponse(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")

	// Phase 1: brand new bridge, nothing initialised yet → 503
	rec := httptest.NewRecorder()
	b.handleReady(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fresh bridge: /readyz should be 503, got %d", rec.Code)
	}

	// Phase 2: pipes connect → still 503 (initialize handshake not done)
	b.connected.Store(true)
	rec = httptest.NewRecorder()
	b.handleReady(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("pipes connected but no init: /readyz should be 503, got %d", rec.Code)
	}

	// Phase 3: outgoing initialize is observed, response comes back → 200
	outgoing := []byte(`{"jsonrpc":"2.0","method":"initialize","id":1,"params":{}}`)
	b.trackOutgoingInitialize(outgoing)
	incoming := []byte(`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`)
	b.maybeMarkInitialized(incoming)

	rec = httptest.NewRecorder()
	b.handleReady(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("after initialize handshake: /readyz should be 200, got %d", rec.Code)
	}
}

// TestMaybeMarkInitialized_IgnoresNonInitializeResponses ensures the
// gate is specific to the actual initialize handshake. Random tool-call
// responses that happen to have matching IDs must NOT flip readiness.
func TestMaybeMarkInitialized_IgnoresNonInitializeResponses(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")
	b.connected.Store(true)

	// Tool-call response with id=42 — bridge never saw an outgoing
	// initialize with id=42, so this MUST NOT flip initialized.
	b.maybeMarkInitialized([]byte(`{"jsonrpc":"2.0","id":42,"result":{"content":"ok"}}`))
	if b.initialized.Load() {
		t.Fatalf("orphan response with id=42 should not flip initialized; pendingInits was empty")
	}
}

// TestMaybeMarkInitialized_IgnoresErrorResponses ensures we don't flip
// initialized when the MCP server actually FAILED its initialize call.
// A failed handshake means the server is broken — readiness should stay
// false so Kubernetes restarts the pod via the liveness path eventually.
func TestMaybeMarkInitialized_IgnoresErrorResponses(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")
	b.connected.Store(true)
	b.trackOutgoingInitialize([]byte(`{"jsonrpc":"2.0","method":"initialize","id":7}`))

	// Server replies with an error to the initialize request
	b.maybeMarkInitialized([]byte(`{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"init failed"}}`))
	if b.initialized.Load() {
		t.Fatalf("error response to initialize should not flip initialized")
	}
}

// TestTrackOutgoingInitialize_IgnoresOtherMethods ensures we don't
// pollute pendingInits with random tool calls. Only `initialize`
// requests are tracked.
func TestTrackOutgoingInitialize_IgnoresOtherMethods(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")

	b.trackOutgoingInitialize([]byte(`{"jsonrpc":"2.0","method":"tools/call","id":99,"params":{}}`))
	if _, ok := b.pendingInits.Load("99"); ok {
		t.Fatalf("tools/call should not be tracked as pending initialize")
	}
}

func TestReadStdoutPipe_NormalMessages(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")
	var received []string

	ch := make(chan []byte, 64)
	b.clientMu.Lock()
	b.clients["test"] = ch
	b.clientMu.Unlock()

	input := `{"jsonrpc":"2.0","id":1,"result":"ok"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"result":"done"}` + "\n"

	err := b.readStdoutPipe(strings.NewReader(input), 4*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(ch)
	for msg := range ch {
		received = append(received, string(msg))
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(received))
	}
	if received[0] != `{"jsonrpc":"2.0","id":1,"result":"ok"}` {
		t.Errorf("unexpected first message: %s", received[0])
	}
}

func TestReadStdoutPipe_SkipsEmptyAndNonJSON(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")

	ch := make(chan []byte, 64)
	b.clientMu.Lock()
	b.clients["test"] = ch
	b.clientMu.Unlock()

	input := "\n" + "not json\n" + `{"id":1}` + "\n"

	err := b.readStdoutPipe(strings.NewReader(input), 4*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(ch)
	var received []string
	for msg := range ch {
		received = append(received, string(msg))
	}

	if len(received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(received))
	}
}

func TestReadStdoutPipe_OversizedMessageRecovery(t *testing.T) {
	b := New("/tmp", 8080, "/healthz")

	ch := make(chan []byte, 64)
	b.clientMu.Lock()
	b.clients["test"] = ch
	b.clientMu.Unlock()

	// readStdoutPipe initialises bufio.Scanner with `Buffer(make([]byte, 64*1024), maxBytes)`.
	// Go's Scanner ignores `maxBytes` when it's smaller than the initial buffer
	// capacity — the effective ceiling is max(maxBytes, cap(initialBuf)) = 64 KiB.
	// To exercise the ErrTooLong recovery path the test must provide a line
	// LARGER than the initial buffer AND set maxBytes below the oversized line
	// length. 80 KiB max + a 100 KiB oversized line satisfies both constraints
	// without ballooning test memory.
	maxBytes := 80 * 1024
	oversized := `{"data":"` + strings.Repeat("x", 100*1024) + `"}`
	normal := `{"jsonrpc":"2.0","id":1,"result":"ok"}`

	// Build input: normal message, then oversized, then another normal message.
	// The oversized message should be skipped and both normal messages received.
	var buf bytes.Buffer
	buf.WriteString(normal + "\n")
	buf.WriteString(oversized + "\n")
	buf.WriteString(normal + "\n")

	err := b.readStdoutPipe(&buf, maxBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(ch)
	var received []string
	for msg := range ch {
		received = append(received, string(msg))
	}

	// Should have received both normal messages; the oversized one is skipped
	if len(received) != 2 {
		t.Fatalf("expected 2 messages after overflow recovery, got %d", len(received))
	}
	for i, msg := range received {
		if msg != normal {
			t.Errorf("message %d: expected normal JSON-RPC message, got first 80 chars: %.80s", i, msg)
		}
	}
}
