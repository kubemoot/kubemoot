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
		resp.Body.Close()
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
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{"s3cret"}},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "ask,get_answer,list_crews" {
		t.Fatalf("tools %v", names)
	}

	listed, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_crews", Arguments: map[string]any{}})
	if err != nil || listed.IsError {
		t.Fatalf("list_crews: %v %v", err, listed)
	}
	if !strings.Contains(textOf(listed), `"hello"`) {
		t.Fatalf("list_crews content: %s", textOf(listed))
	}

	asked, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ask", Arguments: map[string]any{"crew": "hello", "question": "Capital of France?", "waitSeconds": 0}})
	if err != nil || asked.IsError {
		t.Fatalf("ask: %v %v", err, asked)
	}
	var pending AskOutput
	if err := json.Unmarshal([]byte(textOf(asked)), &pending); err != nil || pending.ID == "" {
		t.Fatalf("ask output: %v %s", err, textOf(asked))
	}
	settle(t, svc, pending.ID)

	got, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_answer", Arguments: map[string]any{"ticket": pending.ID}})
	if err != nil || got.IsError {
		t.Fatalf("get_answer: %v %v", err, got)
	}
	var answer GetAnswerOutput
	if err := json.Unmarshal([]byte(textOf(got)), &answer); err != nil || answer.State != StateAnswered || !strings.Contains(answer.Answer, "Paris") {
		t.Fatalf("get_answer output: %v %s", err, textOf(got))
	}
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
