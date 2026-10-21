package toolsdoor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// recordingGateway lists one tool and keeps the body and headers of the last call.
type recordingGateway struct {
	*httptest.Server
	mu     sync.Mutex
	body   map[string]any
	header http.Header
}

func newRecordingGateway(t *testing.T) *recordingGateway {
	t.Helper()
	g := &recordingGateway{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tools": []map[string]any{{keyName: toolTime}}})
	})
	mux.HandleFunc("POST /tools/{name}/call", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.body, g.header = map[string]any{}, r.Header.Clone()
		_ = json.Unmarshal(raw, &g.body)
		g.mu.Unlock()
		_, _ = w.Write([]byte(`{"isError":false,"content":"ok"}`))
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

func callTime(t *testing.T, s *mcp.ClientSession) {
	t.Helper()
	if _, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: toolTime, Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
}

func TestTheRequestersDateAndZoneTravelInMeta(t *testing.T) {
	g := newRecordingGateway(t)
	reg := turn.NewRegistry()
	reg.Open("k", turn.Info{RequestDate: "2026-10-20T21:30:00-04:00", TimeZone: "America/New_York"})
	callTime(t, connect(t, Config{Gateway: g.URL}, reg))
	meta, _ := g.body["_meta"].(map[string]any)
	if meta["requestDate"] != "2026-10-20T21:30:00-04:00" || meta["timeZone"] != "America/New_York" {
		t.Fatalf("_meta %v", g.body)
	}
}

func TestWithoutARequesterThereIsNoMeta(t *testing.T) {
	g := newRecordingGateway(t)
	reg := turn.NewRegistry()
	reg.Open("k", turn.Info{})
	callTime(t, connect(t, Config{Gateway: g.URL}, reg))
	if _, has := g.body["_meta"]; has {
		t.Fatalf("a turn with no requester must send the call it always sent: %v", g.body)
	}
	if len(requesterMeta(nil)) != 0 {
		t.Fatal("no turn, no meta")
	}
}

func TestOnlyTheZoneIsPassedWhenThatIsAllThereIs(t *testing.T) {
	m := turn.NewMeter(turn.Info{TimeZone: "Europe/Paris"})
	if got := requesterMeta(m); len(got) != 1 || got["timeZone"] != "Europe/Paris" {
		t.Fatalf("%v", got)
	}
}

func TestTheTraceContextReachesTheGateway(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	g := newRecordingGateway(t)
	door := httptest.NewServer(obs.Handler(New(Config{Gateway: g.URL}, turn.NewRegistry(), nil).Handler(), "tools-door"))
	t.Cleanup(door.Close)
	tr := &traceTransport{traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}
	client := mcp.NewClient(&mcp.Implementation{Name: "h", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: door.URL, HTTPClient: &http.Client{Transport: tr}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	callTime(t, sess)
	g.mu.Lock()
	defer g.mu.Unlock()
	if !strings.Contains(g.header.Get(obs.TraceparentHeader), "0af7651916cd43dd8448eb211c80319c") {
		t.Fatalf("the gateway saw %q", g.header.Get(obs.TraceparentHeader))
	}
}

// traceTransport stamps a harness's traceparent on every request.
type traceTransport struct{ traceparent string }

func (t *traceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(obs.TraceparentHeader, t.traceparent)
	return http.DefaultTransport.RoundTrip(r)
}
