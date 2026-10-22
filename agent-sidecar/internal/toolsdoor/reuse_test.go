package toolsdoor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const (
	toolSearch   = "search"
	toolIdem     = "lookup"
	toolWrite    = "write"
	toolBare     = "bare"
	keyQuery     = "q"
	annReadOnly  = "readOnlyHint"
	annIdempoten = "idempotentHint"
)

// countingGateway lists tools with the given annotations and counts each tool's calls.
func countingGateway(t *testing.T) (*httptest.Server, func(string) int32) {
	t.Helper()
	counts := map[string]*atomic.Int32{}
	for _, n := range []string{toolSearch, toolIdem, toolWrite, toolBare} {
		counts[n] = &atomic.Int32{}
	}
	ann := map[string]map[string]any{
		toolSearch: {annReadOnly: true},
		toolIdem:   {annIdempoten: true},
		toolWrite:  {annReadOnly: false, annIdempoten: false},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		tools := make([]map[string]any, 0, len(counts))
		for n := range counts {
			tool := map[string]any{keyName: n, keyDescription: n}
			if a, ok := ann[n]; ok {
				tool["annotations"] = a
			}
			tools = append(tools, tool)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{keyTools: tools})
	})
	mux.HandleFunc("POST /tools/{name}/call", func(w http.ResponseWriter, r *http.Request) {
		n := counts[r.PathValue("name")].Add(1)
		_, _ = fmt.Fprintf(w, `{"isError":false,"content":"result %d"}`, n)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, func(name string) int32 { return counts[name].Load() }
}

func callText(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: %v %+v", tool, err, res)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func openTurn(t *testing.T, reg *turn.Registry, gwURL string) *mcp.ClientSession {
	t.Helper()
	reg.Open("", turn.Info{}) // the test client sends no bearer key, which is the empty key
	return connect(t, Config{Gateway: gwURL}, reg)
}

func TestIdenticalReadOnlyCallIsAnsweredFromTheTurn(t *testing.T) {
	gw, calls := countingGateway(t)
	s := openTurn(t, turn.NewRegistry(), gw.URL)
	first := callText(t, s, toolSearch, map[string]any{keyQuery: "go", "n": 2})
	again := callText(t, s, toolSearch, map[string]any{"n": 2, keyQuery: "go"}) // same arguments, other key order
	if first != "result 1" || again != reusedNote {
		t.Fatalf("first %q again %q", first, again)
	}
	if calls(toolSearch) != 1 {
		t.Fatalf("the gateway was called %d times", calls(toolSearch))
	}
}

func TestAnIdempotentToolIsReusedToo(t *testing.T) {
	gw, calls := countingGateway(t)
	s := openTurn(t, turn.NewRegistry(), gw.URL)
	callText(t, s, toolIdem, nil)
	if got := callText(t, s, toolIdem, nil); got != reusedNote || calls(toolIdem) != 1 {
		t.Fatalf("got %q after %d calls", got, calls(toolIdem))
	}
}

func TestAChangedArgumentCallsAgain(t *testing.T) {
	gw, calls := countingGateway(t)
	s := openTurn(t, turn.NewRegistry(), gw.URL)
	callText(t, s, toolSearch, map[string]any{keyQuery: "a"})
	if got := callText(t, s, toolSearch, map[string]any{keyQuery: "b"}); got != "result 2" || calls(toolSearch) != 2 {
		t.Fatalf("got %q after %d calls", got, calls(toolSearch))
	}
}

func TestToolsThatChangeThingsOrDeclareNothingAlwaysCall(t *testing.T) {
	gw, calls := countingGateway(t)
	s := openTurn(t, turn.NewRegistry(), gw.URL)
	for _, tool := range []string{toolWrite, toolBare} {
		callText(t, s, tool, map[string]any{keyQuery: "x"})
		if got := callText(t, s, tool, map[string]any{keyQuery: "x"}); strings.Contains(got, "already returned") || calls(tool) != 2 {
			t.Fatalf("%s: got %q after %d calls", tool, got, calls(tool))
		}
	}
}

func TestNothingIsReusedAcrossTurns(t *testing.T) {
	gw, calls := countingGateway(t)
	reg := turn.NewRegistry()
	s := openTurn(t, reg, gw.URL)
	callText(t, s, toolSearch, map[string]any{keyQuery: "go"})
	reg.Close("")
	reg.Open("", turn.Info{}) // the next turn
	if got := callText(t, s, toolSearch, map[string]any{keyQuery: "go"}); got != "result 2" || calls(toolSearch) != 2 {
		t.Fatalf("got %q after %d calls", got, calls(toolSearch))
	}
}

func TestACallWithNoOpenTurnIsNeverReused(t *testing.T) {
	gw, calls := countingGateway(t)
	s := connect(t, Config{Gateway: gw.URL}, turn.NewRegistry())
	callText(t, s, toolSearch, nil)
	callText(t, s, toolSearch, nil)
	if calls(toolSearch) != 2 {
		t.Fatalf("the gateway was called %d times", calls(toolSearch))
	}
}

func TestCallKeyIgnoresKeyOrderAndFallsBackOnBadJSON(t *testing.T) {
	a := callKey("t", json.RawMessage(`{"a":1,"b":2}`))
	b := callKey("t", json.RawMessage(`{ "b":2, "a":1 }`))
	if a != b || a == callKey("u", json.RawMessage(`{"a":1,"b":2}`)) {
		t.Fatalf("keys %q %q", a, b)
	}
	if callKey("t", json.RawMessage(`not json`)) == callKey("t", nil) {
		t.Fatal("unparseable arguments must still key apart from none")
	}
}
