package modeldoor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

func guardedDoor(t *testing.T, up http.Handler, require bool) (*httptest.Server, *fakePlacer, *turn.Registry) {
	t.Helper()
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	placer := &fakePlacer{endpoint: upstream.URL}
	reg := turn.NewRegistry()
	door := httptest.NewServer(New(Config{Crew: "c", Agent: "a", RequireTurn: require,
		Aliases: map[string][]string{aliasDefault: {realModel}}}, placer, reg, nil))
	t.Cleanup(door.Close)
	return door, placer, reg
}

func TestACallOutsideAnyTurnIsRefusedWhenTurnsAreRequired(t *testing.T) {
	var seen map[string]any
	door, placer, reg := guardedDoor(t, jsonUpstream(&seen), true)
	resp := post(t, door.URL, "any", completions)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || len(placer.asked) != 0 {
		t.Fatalf("status %d; nothing may be placed for a call outside a turn (%d asked)", resp.StatusCode, len(placer.asked))
	}
	reg.Open("k", turn.Info{})
	resp = post(t, door.URL, "k", completions)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inside a turn: %d", resp.StatusCode)
	}
}

func TestListingModelsNeedsNoTurn(t *testing.T) {
	door, _, _ := guardedDoor(t, jsonUpstream(new(map[string]any)), true)
	resp, err := http.Get(door.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestHopByHopHeadersAreNotPassedOn(t *testing.T) {
	up := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Proxy-Authenticate", "Basic")
		w.Header().Set("X-Engine", "vllm")
		_, _ = w.Write([]byte(`{}`))
	})
	door, _, _ := guardedDoor(t, up, false)
	resp := post(t, door.URL, "k", completions)
	_ = resp.Body.Close()
	if resp.Header.Get("Keep-Alive") != "" || resp.Header.Get("Proxy-Authenticate") != "" {
		t.Errorf("connection headers leaked: %v", resp.Header)
	}
	if resp.Header.Get("X-Engine") != "vllm" {
		t.Errorf("end-to-end headers pass: %v", resp.Header)
	}
}

func TestEachKindOfCallIsNamedForTheMetrics(t *testing.T) {
	d := &Door{cfg: Config{Aliases: map[string][]string{aliasDefault: {realModel}, "triage": {"s"}}}, registry: turn.NewRegistry()}
	for body, want := range map[string]string{
		`{"model":"default","messages":[]}`:                               "simple",
		`{"model":"default","tools":[{"type":"function"}],"messages":[]}`: "tool-turn",
		`{"model":"triage","messages":[]}`:                                aliasTriage,
		`{"model":"unknown","tools":[{"type":"function"}]}`:               "tool-turn",
	} {
		var req map[string]any
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		c := d.newCall(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), req, len(body))
		if c.kind != want {
			t.Errorf("%s: kind %q, want %q", body, c.kind, want)
		}
	}
}
