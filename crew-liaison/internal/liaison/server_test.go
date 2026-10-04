package liaison

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBearerGate(t *testing.T) {
	f := newFakeGateway()
	defer f.srv.Close()
	svc := newTestService(t, f, 1)
	srv := httptest.NewServer(NewHandler(svc, "s3cret"))
	defer srv.Close()

	for _, tc := range []struct {
		name, path, auth string
		want             int
	}{
		{"health is open", "/health", "", http.StatusOK},
		{"mcp without token", "/mcp", "", http.StatusUnauthorized},
		{"mcp wrong token", "/mcp", "Bearer nope", http.StatusUnauthorized},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}

// TestMCPEndToEnd drives the real protocol: an SDK client initializes over Streamable
// HTTP, lists the tools, asks, and collects the answer, exactly as Claude Code would.
func TestMCPEndToEnd(t *testing.T) {
	f := newFakeGateway(synthesisEvents()...)
	defer f.srv.Close()
	svc := newTestService(t, f, 2)
	srv := httptest.NewServer(NewHandler(svc, "s3cret"))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session := connectSession(ctx, t, srv.URL+"/mcp", "s3cret")

	if names := toolNames(ctx, t, session); names != "ask,get_answer,list_crews" {
		t.Fatalf("tools %v", names)
	}
	listed := callTool(ctx, t, session, "list_crews", map[string]any{})
	if !strings.Contains(listed, `"hello"`) {
		t.Fatalf("list_crews content: %s", listed)
	}

	var pending AskOutput
	asked := callTool(ctx, t, session, "ask", map[string]any{"crew": testCrew, "question": testQuestion, "waitSeconds": 0})
	if err := json.Unmarshal([]byte(asked), &pending); err != nil || pending.ID == "" {
		t.Fatalf("ask output: %v %s", err, asked)
	}
	settle(t, svc, pending.ID)

	var answer GetAnswerOutput
	got := callTool(ctx, t, session, "get_answer", map[string]any{"ticket": pending.ID})
	err := json.Unmarshal([]byte(got), &answer)
	if err != nil || answer.State != StateAnswered || !strings.Contains(answer.Answer, "Paris") {
		t.Fatalf("get_answer output: %v %s", err, got)
	}
}

// connectSession opens an MCP client session over Streamable HTTP with a bearer token
// and closes it when the test ends.
func connectSession(ctx context.Context, t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearerTransport{token}},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// toolNames lists the session's tools as a comma-separated string of names.
func toolNames(ctx context.Context, t *testing.T, session *mcp.ClientSession) string {
	t.Helper()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	return strings.Join(names, ",")
}

// callTool calls a tool, fails the test on a transport or tool error, and returns the
// result's text.
func callTool(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: %v %v", name, err, res)
	}
	return textOf(res)
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
