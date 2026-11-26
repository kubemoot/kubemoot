package liaison

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const probeTool = "probe"

// probe is what a tool on a server set up like the liaison's can tell about its caller.
type probe struct {
	Version string `json:"version"`
	URL     bool   `json:"url"`
	Known   bool   `json:"known"`
}

// probeServer is a stateless, JSON-answering MCP endpoint, as the liaison's /mcp is, with
// one tool that reports the caller's protocol revision and whether it declared URL mode
// elicitation.
func probeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "probe-server", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: probeTool},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, probe, error) {
			caps := req.ClientCapabilities()
			return nil, probe{
				Version: req.ProtocolVersion(), Known: caps != nil,
				URL: caps != nil && caps.Elicitation != nil && caps.Elicitation.URL != nil,
			}, nil
		})
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(srv.Close)
	return srv
}

// askProbe calls the probe tool as a client with caps that speaks revision ("" for the
// SDK's latest).
func askProbe(t *testing.T, endpoint, revision string, caps *mcp.ClientCapabilities) probe {
	t.Helper()
	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "probe-client", Version: "0"}, &mcp.ClientOptions{Capabilities: caps}).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, &mcp.ClientSessionOptions{ProtocolVersion: revision})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: probeTool})
	if err != nil || res.IsError {
		t.Fatalf("probe: %v %v", err, res)
	}
	var out probe
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestWhatTheSDKTellsAStatelessServerAboutURLElicitation records what the MCP SDK in go.mod
// supports, so the connect-account design rests on behavior that is checked: which
// specification revisions it speaks, and when a stateless server such as the liaison can
// see that a client accepts URL mode elicitation.
func TestWhatTheSDKTellsAStatelessServerAboutURLElicitation(t *testing.T) {
	const stateless, handshake = "2026-07-28", "2025-11-25"
	versions := mcp.SupportedProtocolVersions()
	if !slices.Contains(versions, stateless) || !slices.Contains(versions, handshake) || versions[0] != stateless {
		t.Fatalf("the SDK speaks %v", versions)
	}
	srv := probeServer(t)
	urlMode := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}}
	formMode := &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}

	// 2026-07-28 carries the client's capabilities on every request, so a stateless server
	// sees them and can choose URL mode elicitation for that request.
	if got := askProbe(t, srv.URL, "", urlMode); got != (probe{Version: stateless, URL: true, Known: true}) {
		t.Errorf("a 2026-07-28 client that accepts URL mode: %+v", got)
	}
	if got := askProbe(t, srv.URL, "", formMode); got != (probe{Version: stateless, URL: false, Known: true}) {
		t.Errorf("a 2026-07-28 client with form mode only: %+v", got)
	}
	// 2025-11-25 declares capabilities once, at initialize. A stateless server keeps no
	// session, so on a later tool call it knows nothing about the client: it must not send
	// an elicitation and falls back to a connect link in the tool result.
	old := askProbe(t, srv.URL, handshake, urlMode)
	if old.URL || old.Known {
		t.Errorf("a 2025-11-25 client on a stateless server: %+v", old)
	}
}
