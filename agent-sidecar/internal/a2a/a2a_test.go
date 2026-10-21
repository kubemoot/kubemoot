package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const agreeSignal = "agree"

func TestInterpret(t *testing.T) {
	data := func(s string) []Part { return []Part{{Data: json.RawMessage(s)}} }
	done := func(parts ...Part) SendResponse {
		tk := &Task{Artifacts: []Artifact{{Parts: parts}}}
		tk.Status.State = "TASK_STATE_COMPLETED"
		return SendResponse{Task: tk}
	}
	state := func(s string) SendResponse {
		tk := &Task{}
		tk.Status.State = s
		return SendResponse{Task: tk}
	}
	cases := []struct {
		name string
		resp SendResponse
		err  error
		want Outcome
	}{
		{"plain text is agree", done(Part{Text: "two nodes"}), nil, Outcome{Signal: agreeSignal, Content: "two nodes"}},
		{"structured concern", done(data(`{"signal":"concern","content":"stale data","reason":"old"}`)...), nil,
			Outcome{Signal: "concern", Content: "stale data", Reason: "old"}},
		{"stand aside needs no content", done(data(`{"signal":"stand_aside","reason":"not my domain"}`)...), nil,
			Outcome{Signal: "stand_aside", Reason: "not my domain"}},
		{"empty text is a failure never a stand aside", done(Part{Text: "  "}), nil,
			Outcome{Signal: SignalFailure, FailureType: FailureEmpty}},
		{"agree with no content is a failure", done(data(`{"signal":"agree","content":""}`)...), nil,
			Outcome{Signal: SignalFailure, FailureType: FailureEmpty}},
		{"rejected", state("TASK_STATE_REJECTED"), nil, Outcome{Signal: "stand_aside", Reason: "rejected"}},
		{"input required", state("TASK_STATE_INPUT_REQUIRED"), nil, Outcome{Signal: SignalFailure, FailureType: "input_required"}},
		{"auth required", state("TASK_STATE_AUTH_REQUIRED"), nil, Outcome{Signal: SignalFailure, FailureType: "input_required"}},
		{"failed", state("TASK_STATE_FAILED"), nil, Outcome{Signal: SignalFailure, FailureType: "harness_failed", Reason: "TASK_STATE_FAILED"}},
		{"unknown signal in data falls back to text", done(data(`{"signal":"maybe"}`)[0], Part{Text: "hello"}), nil,
			Outcome{Signal: agreeSignal, Content: "hello"}},
		{"direct message reply", SendResponse{Message: &Message{Parts: []Part{{Text: "hi"}}}}, nil, Outcome{Signal: agreeSignal, Content: "hi"}},
		{"transport error", SendResponse{}, fmt.Errorf("boom"), Outcome{Signal: SignalFailure, FailureType: "harness_error", Reason: "boom"}},
		{"no task no message", SendResponse{}, nil, Outcome{Signal: SignalFailure, FailureType: FailureEmpty}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Interpret(c.resp, c.err); got != c.want {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

func harnessServer(t *testing.T, interfaces string, reply string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"name":"h","supportedInterfaces":%s}`, interfaces)
	})
	mux.HandleFunc("POST /message:send", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != ContentType || r.Header.Get("A2A-Version") != Version {
			http.Error(w, "bad headers", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(reply))
	})
	return httptest.NewServer(mux)
}

func TestDiscoverPicksHTTPJSONAndSendsATurn(t *testing.T) {
	h := harnessServer(t, "[]",
		`{"task":{"id":"1","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"parts":[{"text":"answer"}]}]}}`)
	defer h.Close()
	url := h.URL
	// The card names its own URL; point it at the test server.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"supportedInterfaces":[{"url":"grpc://x","protocolBinding":"GRPC"},{"url":%q,"protocolBinding":"HTTP+JSON","protocolVersion":"1.0"}]}`, url)
	})
	card := httptest.NewServer(mux)
	defer card.Close()

	c, _, err := Discover(context.Background(), card.URL)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := NewTurn("m1", "thread-1", "question?", TurnInput{Kind: "discussion", Agent: "a", Crew: "c", Phase: "EVALUATING", TurnKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Send(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := Interpret(resp, nil); got.Signal != agreeSignal || got.Content != "answer" {
		t.Fatalf("got %+v", got)
	}
}

func TestDiscoverRefusesACardWithoutHTTPJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"supportedInterfaces":[{"url":"grpc://x","protocolBinding":"GRPC"}]}`))
	})
	s := httptest.NewServer(mux)
	defer s.Close()
	if _, _, err := Discover(context.Background(), s.URL); err == nil {
		t.Fatal("expected an error for a card with no HTTP+JSON interface")
	}
}

func TestDiscoverFailsOnMissingCard(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	defer s.Close()
	if _, _, err := Discover(context.Background(), s.URL); err == nil {
		t.Fatal("expected an error when the card is missing")
	}
}

func TestSendReportsNon200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /message:send", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusInternalServerError) })
	s := httptest.NewServer(mux)
	defer s.Close()
	c := &Client{base: s.URL, http: s.Client()}
	if _, err := c.Send(context.Background(), Message{MessageID: "x", Role: "ROLE_USER"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestNewTurnCarriesProfileAndQuestion(t *testing.T) {
	m, err := NewTurn("m1", "th", "q", TurnInput{Agent: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Role != "ROLE_USER" || m.ContextID != "th" || m.Parts[0].Text != "q" {
		t.Fatalf("unexpected message %+v", m)
	}
	var in TurnInput
	if err := json.Unmarshal(m.Parts[1].Data, &in); err != nil || in.Profile != ProfileURI {
		t.Fatalf("profile missing: %v %+v", err, in)
	}
}
