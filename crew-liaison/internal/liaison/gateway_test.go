package liaison

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGateway imitates a crew's discussion gateway: POST starts, GET streams.
type fakeGateway struct {
	mu        sync.Mutex
	questions []string
	events    []string // raw SSE lines to stream
	startCode int
	srv       *httptest.Server
}

// Fixture names shared by the liaison tests.
const (
	testCrew     = "hello"
	dupCrew      = "dup"
	testQuestion = "Capital of France?"
)

func newFakeGateway(events ...string) *fakeGateway {
	f := &fakeGateway{events: events, startCode: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/discussions/{crew}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message string `json:"message"`
		}
		_ = decodeJSON(r, &body)
		f.mu.Lock()
		f.questions = append(f.questions, body.Message)
		code := f.startCode
		f.mu.Unlock()
		if code != http.StatusOK {
			http.Error(w, "no", code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"conversationId":"conv-42"}`)
	})
	mux.HandleFunc("GET /api/v1/discussions/{crew}/{conv}/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f.mu.Lock()
		events := append([]string(nil), f.events...)
		f.mu.Unlock()
		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeGateway) resolve(namespace, crew string) string {
	return f.srv.URL + "/api/v1/discussions/" + crew
}

func (f *fakeGateway) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.questions...)
}

func decodeJSON(r *http.Request, v any) error {
	return jsonDecode(r.Body, v)
}

func TestGatewayStartAndFollow(t *testing.T) {
	f := newFakeGateway(
		`{"type":"connected"}`,
		`{"type":"phase","agent":"k8s-tooler","status":"thinking"}`,
		`{"type":"finding","agent":"k8s-tooler","signal":"agree","summary":"secret"}`,
		`{"type":"synthesis","content":"The capital of France is Paris."}`,
		`{"type":"done"}`,
	)
	defer f.srv.Close()
	g := NewGatewayAt(f.resolve)

	conv, err := g.Start(context.Background(), testCrew, testCrew, "capital of France?")
	if err != nil || conv != "conv-42" {
		t.Fatalf("Start: %v %q", err, conv)
	}
	if got := f.asked(); len(got) != 1 || got[0] != "capital of France?" {
		t.Fatalf("question not forwarded: %v", got)
	}

	var seen []Event
	err = g.Follow(context.Background(), testCrew, testCrew, conv, func(ev Event) bool {
		seen = append(seen, ev)
		return ev.Type != EventSynthesis
	})
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if len(seen) != 4 || seen[3].Type != EventSynthesis || !strings.Contains(seen[3].Content, "Paris") {
		t.Fatalf("events: %+v", seen)
	}
}

func TestGatewayStartRefused(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	f.startCode = http.StatusServiceUnavailable
	g := NewGatewayAt(f.resolve)
	if _, err := g.Start(context.Background(), testCrew, testCrew, "q"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("want refused error, got %v", err)
	}
}

func TestGatewayUnreachable(t *testing.T) {
	g := NewGatewayAt(func(namespace, crew string) string { return "http://127.0.0.1:1/api/v1/discussions/" + crew })
	if _, err := g.Start(context.Background(), testCrew, testCrew, "q"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("want unreachable error, got %v", err)
	}
}

func TestReadEventsSkipsNoise(t *testing.T) {
	in := ": comment\n\ndata: not json\ndata: {\"type\":\"finding\"}\nevent: x\ndata: {\"type\":\"done\"}\n"
	var types []string
	if err := readEvents(strings.NewReader(in), func(ev Event) bool { types = append(types, ev.Type); return true }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(types, ",") != "finding,done" {
		t.Fatalf("types %v", types)
	}
}
