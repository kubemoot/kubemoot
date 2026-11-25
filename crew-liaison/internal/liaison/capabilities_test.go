package liaison

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The liaison runs on github.com/modelcontextprotocol/go-sdk (the revision in go.mod), which
// negotiates 2026-07-28 down to 2024-11-05 and serves the door stateless. These tests pin how
// a request's URL-elicitation capability is read, because the connect flow chooses between an
// elicitation and a link in the tool result by it.

type probeInput struct{}
type probeOutput struct {
	URL bool `json:"url"`
}

// probeServer is a server with one tool that reports clientElicitsURL, behind the same
// handler options as the door.
func probeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "probe", Description: "reports url elicitation"},
		func(_ context.Context, req *mcp.CallToolRequest, _ probeInput) (*mcp.CallToolResult, probeOutput, error) {
			return nil, probeOutput{URL: clientElicitsURL(req)}, nil
		})
	srv := httptest.NewServer(newMCPHandler(server))
	t.Cleanup(srv.Close)
	return srv
}

// post sends one JSON-RPC request and returns the decoded response body.
func post(t *testing.T, url, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if strings.Contains(body, "io.modelcontextprotocol/protocolVersion") {
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", "probe")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("response %q: %v", raw, err)
	}
	return out
}

// probed reads the probe tool's structured answer, or "" with the error message when the
// call failed.
func probed(t *testing.T, resp map[string]any) (url bool, errMsg string) {
	t.Helper()
	if e, ok := resp["error"].(map[string]any); ok {
		msg, _ := e["message"].(string)
		return false, msg
	}
	result, _ := resp["result"].(map[string]any)
	structured, _ := result["structuredContent"].(map[string]any)
	if structured == nil {
		t.Fatalf("no structured content in %v", resp)
	}
	url, _ = structured["url"].(bool)
	return url, ""
}

func newRevisionCall(caps string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe","arguments":{},"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientCapabilities":` + caps + `}}}`
}

// TestElicitationDetectedFromRequestMeta pins revision 2026-07-28: the capability rides on
// every request in _meta, and only a url entry counts.
func TestElicitationDetectedFromRequestMeta(t *testing.T) {
	srv := probeServer(t)
	for _, tc := range []struct {
		name, caps string
		want       bool
	}{
		{"none", `{}`, false},
		{"form only", `{"elicitation":{"form":{}}}`, false},
		{"empty elicitation means form", `{"elicitation":{}}`, false},
		{"url only", `{"elicitation":{"url":{}}}`, true},
		{"form and url", `{"elicitation":{"form":{},"url":{}}}`, true},
		{"other capabilities", `{"roots":{"listChanged":true}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := probed(t, post(t, srv.URL, newRevisionCall(tc.caps)))
			if msg != "" {
				t.Fatalf("call failed: %s", msg)
			}
			if got != tc.want {
				t.Errorf("url elicitation = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMalformedCapabilitiesRefuseTheCall pins that a request claiming 2026-07-28 with
// capabilities that are not an object never reaches a tool, so a tool never sees a guess.
func TestMalformedCapabilitiesRefuseTheCall(t *testing.T) {
	srv := probeServer(t)
	for _, caps := range []string{`"url"`, `42`, `null`} {
		_, msg := probed(t, post(t, srv.URL, newRevisionCall(caps)))
		if !strings.Contains(msg, "clientCapabilities") {
			t.Errorf("capabilities %s: message %q does not name the capabilities", caps, msg)
		}
	}
}

// TestOlderRevisionDeclaresNothingPerRequest pins the stateless door's limit for revisions
// before 2026-07-28: capabilities are sent once at initialize, and no session carries them to
// a later call, so a client on 2025-11-25 reads as not eliciting and gets the link. This is
// the behavior the connect flow relies on; if the SDK starts carrying them, this test changes
// with the flow.
func TestOlderRevisionDeclaresNothingPerRequest(t *testing.T) {
	srv := probeServer(t)
	init := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25",` +
		`"capabilities":{"elicitation":{"form":{},"url":{}}},"clientInfo":{"name":"c","version":"0"}}}`
	if resp := post(t, srv.URL, init); resp["error"] != nil {
		t.Fatalf("initialize: %v", resp["error"])
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe","arguments":{}}}`
	got, msg := probed(t, post(t, srv.URL, call))
	if msg != "" {
		t.Fatalf("call failed: %s", msg)
	}
	if got {
		t.Error("a stateless call read url elicitation from an earlier initialize")
	}
}

func TestElicitationCheckHandlesAbsentInput(t *testing.T) {
	if clientElicitsURL(nil) {
		t.Error("nil request elicits")
	}
	if capabilitiesElicitURL(nil) {
		t.Error("nil capabilities elicit")
	}
	if capabilitiesElicitURL(&mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{}}) {
		t.Error("empty elicitation capability reads as url")
	}
}
