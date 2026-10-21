package modeldoor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

type fakePlacer struct {
	mu       sync.Mutex
	endpoint string
	err      error
	queued   string // when set, the placer reports a wait with this reason
	degraded bool
	asked    []placement.Request
	released []string
	reports  []placement.Release
}

func (f *fakePlacer) Place(_ context.Context, r placement.Request, onQueued func(string)) (placement.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r)
	if f.queued != "" && onQueued != nil {
		onQueued(f.queued)
	}
	if f.err != nil {
		return placement.Reply{}, f.err
	}
	rep := placement.Reply{Server: "gpu-1", Endpoint: f.endpoint, Model: realModel, Reason: "warm", Ticket: "tk1",
		Queued: f.queued != "", Degraded: f.degraded}
	if f.degraded {
		rep.Ticket = ""
	}
	return rep, nil
}

func (f *fakePlacer) Release(_ context.Context, r placement.Release) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, r.Ticket)
	f.reports = append(f.reports, r)
	return nil
}

const (
	realModel    = "real-model"
	threadNine   = "thread-9"
	aliasDefault = "default"
	aliasTriage  = "triage"
)

func (f *fakePlacer) askedFirst() placement.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[0]
}

func (f *fakePlacer) releasedTickets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.released...)
}

// waitReleased waits for the first release: a ticket is released as the handler returns, which can be just
// after the client has the whole reply.
func (f *fakePlacer) waitReleased(t *testing.T) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(f.releasedTickets()) < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	return f.releasedTickets()
}

func newDoor(t *testing.T, upstream http.Handler, placerErr error) (*httptest.Server, *fakePlacer, *turn.Registry) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	placer := &fakePlacer{endpoint: up.URL, err: placerErr}
	reg := turn.NewRegistry()
	door := httptest.NewServer(New(Config{Crew: "c", Agent: "a", Phase: "EVALUATING",
		Aliases: map[string][]string{aliasDefault: {realModel, "backup"}, aliasTriage: {"small"}}}, placer, reg, nil))
	t.Cleanup(door.Close)
	return door, placer, reg
}

func post(t *testing.T, url, key, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func jsonUpstream(seen *map[string]any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(seen)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`))
	})
}

func TestAliasIsRewrittenAndTicketReleased(t *testing.T) {
	var seen map[string]any
	door, placer, reg := newDoor(t, jsonUpstream(&seen), nil)
	meter := reg.Open("k1", turn.Info{ThreadID: threadNine})
	resp := post(t, door.URL, "k1", `{"model":"triage","messages":[]}`)
	defer func() { _ = resp.Body.Close() }()
	if seen["model"] != realModel {
		t.Fatalf("upstream saw model %v", seen["model"])
	}
	if got := placer.askedFirst().Models; len(got) != 1 || got[0] != "small" {
		t.Fatalf("alias triage should ask for [small], asked %v", got)
	}
	if got := placer.waitReleased(t); len(got) != 1 || got[0] != "tk1" {
		t.Fatalf("ticket not released: %v", got)
	}
	snap := meter.Snapshot()
	if snap["inputTokens"] != int64(7) || snap["outputTokens"] != int64(2) || snap["server"] != "gpu-1" {
		t.Fatalf("meter %v", snap)
	}
}

func TestUnknownAliasMeansDefault(t *testing.T) {
	var seen map[string]any
	door, placer, _ := newDoor(t, jsonUpstream(&seen), nil)
	resp := post(t, door.URL, "k", `{"model":"gpt-4o","messages":[]}`)
	_ = resp.Body.Close()
	if got := placer.askedFirst().Models; len(got) != 2 || got[0] != realModel {
		t.Fatalf("unknown model should be default, asked %v", got)
	}
}

func TestNoFitIsAnOpenAIShapedError(t *testing.T) {
	door, placer, _ := newDoor(t, jsonUpstream(new(map[string]any)), &placement.ErrNoFit{Reason: "gpu busy"})
	resp := post(t, door.URL, "k", `{"model":"default"}`)
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusServiceUnavailable || body.Error.Code != "kubemoot_no_fit" || body.Error.Message != "gpu busy" {
		t.Fatalf("status %d body %+v", resp.StatusCode, body)
	}
	if len(placer.releasedTickets()) != 0 {
		t.Fatal("nothing to release when nothing was placed")
	}
}

func TestPlacementServiceDownIsBadGateway(t *testing.T) {
	door, _, _ := newDoor(t, jsonUpstream(new(map[string]any)), fmt.Errorf("connection refused"))
	resp := post(t, door.URL, "k", `{"model":"default"}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestBadJSONIs400(t *testing.T) {
	door, _, _ := newDoor(t, jsonUpstream(new(map[string]any)), nil)
	resp := post(t, door.URL, "k", `{not json`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestModelsListsAliases(t *testing.T) {
	door, _, _ := newDoor(t, jsonUpstream(new(map[string]any)), nil)
	resp, err := http.Get(door.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"default"`) || !strings.Contains(string(b), `"triage"`) {
		t.Fatalf("models %s", b)
	}
}

// A stream must reach the client as the server produces it: the first event arrives while the server
// is still waiting to produce the rest.
func TestStreamingIsNotBuffered(t *testing.T) {
	release := make(chan struct{})
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		_ = http.NewResponseController(w).Flush()
		<-release
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6}}\n\ndata: [DONE]\n\n")
	})
	door, placer, reg := newDoor(t, up, nil)
	meter := reg.Open("ks", turn.Info{})
	resp := post(t, door.URL, "ks", `{"model":"default","stream":true}`)
	defer func() { _ = resp.Body.Close() }()
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Scan()
		line <- sc.Text()
	}()
	select {
	case l := <-line:
		if !strings.Contains(l, `"content":"a"`) {
			t.Fatalf("first line %q", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first event was held back while the server was still producing")
	}
	close(release)
	_, _ = io.Copy(io.Discard, resp.Body)
	if len(placer.waitReleased(t)) != 1 {
		t.Fatal("ticket not released after the stream ended")
	}
	if got := meter.Snapshot()["outputTokens"]; got != int64(6) {
		t.Fatalf("stream usage not captured: %v", got)
	}
}

func TestStreamAsksForUsage(t *testing.T) {
	var seen map[string]any
	door, _, _ := newDoor(t, jsonUpstream(&seen), nil)
	resp := post(t, door.URL, "k", `{"model":"default","stream":true}`)
	_ = resp.Body.Close()
	opts, _ := seen["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Fatalf("stream_options %v", seen["stream_options"])
	}
}
