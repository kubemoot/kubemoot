package toolsdoor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const (
	keyDescription = "description"
	keyName        = "name"
	keyTools       = "tools"
	toolFetch      = "fetch"
	toolShell      = "shell_exec"
	toolTime       = "time_get_current_time"
)

func gateway(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 3, keyTools: []map[string]any{
			{keyName: toolTime, keyDescription: "time"},
			{keyName: toolFetch, keyDescription: toolFetch},
			{keyName: toolShell, keyDescription: toolShell},
		}})
	})
	mux.HandleFunc("POST /tools/{name}/call", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") == toolFetch {
			_, _ = w.Write([]byte(`{"isError":true,"content":"upstream 500"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"isError":false,"content":"ran %s"}`, r.PathValue("name"))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func connect(t *testing.T, cfg Config, reg *turn.Registry) *mcp.ClientSession {
	t.Helper()
	door := httptest.NewServer(New(cfg, reg, nil).Handler())
	t.Cleanup(door.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-harness", Version: "1"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: door.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func names(t *testing.T, s *mcp.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}
	return out
}

func TestAllowListFiltersWhatTheHarnessSees(t *testing.T) {
	gw := gateway(t)
	s := connect(t, Config{Gateway: gw.URL, Enabled: []string{toolTime, toolFetch}}, turn.NewRegistry())
	got := strings.Join(names(t, s), ",")
	if strings.Contains(got, toolShell) || !strings.Contains(got, toolFetch) || !strings.Contains(got, toolTime) {
		t.Fatalf("tools seen: %s", got)
	}
}

func TestDisabledWinsOverEnabled(t *testing.T) {
	gw := gateway(t)
	s := connect(t, Config{Gateway: gw.URL, Enabled: []string{toolFetch, toolShell}, Disabled: []string{toolShell}}, turn.NewRegistry())
	if got := strings.Join(names(t, s), ","); got != toolFetch {
		t.Fatalf("tools seen: %s", got)
	}
}

func TestAHarnessCannotCallAToolItWasNotOffered(t *testing.T) {
	gw := gateway(t)
	s := connect(t, Config{Gateway: gw.URL, Enabled: []string{toolFetch}}, turn.NewRegistry())
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: toolShell, Arguments: map[string]any{}})
	if err == nil && (res == nil || !res.IsError) {
		t.Fatalf("shell_exec ran although the agent may not use it: %+v", res)
	}
}

func TestCallsAreCountedAndFailuresNamed(t *testing.T) {
	gw := gateway(t)
	reg := turn.NewRegistry()
	meter := reg.Open("only-turn", turn.Info{})
	s := connect(t, Config{Gateway: gw.URL, Enabled: []string{toolTime, toolFetch}}, reg)
	ok, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: toolTime, Arguments: map[string]any{}})
	if err != nil || ok.IsError {
		t.Fatalf("time call: %v %+v", err, ok)
	}
	bad, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: toolFetch, Arguments: map[string]any{"url": "x"}})
	if err != nil || !bad.IsError {
		t.Fatalf("fetch should report a tool error: %v %+v", err, bad)
	}
	snap := meter.Snapshot()
	if snap["failedTool"] != toolFetch || snap["failureCount"] != 1 {
		t.Fatalf("meter %v", snap)
	}
	if used, _ := snap["toolsCalled"].([]string); len(used) != 2 {
		t.Fatalf("toolsCalled %v", snap["toolsCalled"])
	}
}

// A harness built on another MCP SDK (goose uses rmcp) sends tools/list with the protocol version its
// initialize negotiated and without relying on a session. The door is stateless, so it answers.
func TestAnotherSDKsStatelessToolsListIsAnswered(t *testing.T) {
	gw := gateway(t)
	door := httptest.NewServer(New(Config{Gateway: gw.URL, Enabled: []string{toolFetch}}, turn.NewRegistry(), nil).Handler())
	defer door.Close()
	req, _ := http.NewRequest(http.MethodPost, door.URL, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"fetch"`) || strings.Contains(string(body), toolShell) {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}

func TestMissingDeclaredToolsAreNamed(t *testing.T) {
	cfg := Config{Enabled: []string{toolFetch, "nope"}}
	missing := cfg.MissingDeclared([]GatewayTool{{Name: toolFetch}})
	if len(missing) != 1 || missing[0] != "nope" {
		t.Fatalf("missing %v", missing)
	}
}

func TestAllowedWithNoListMeansEverythingNotDisabled(t *testing.T) {
	cfg := Config{Disabled: []string{"x"}}
	if !cfg.Allowed("y") || cfg.Allowed("x") {
		t.Fatal("empty enabled list should allow all but the disabled")
	}
}
