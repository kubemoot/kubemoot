package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	langPython = "python"
	langBash   = "bash"
)

// connectHTTP serves s over Streamable HTTP and returns a client session that
// reached it through a real HTTP connection.
func connectHTTP(t *testing.T, s *Server) (*mcp.ClientSession, string) {
	t.Helper()
	handler, err := s.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + MCPPath}, nil)
	if err != nil {
		t.Fatalf("initialize over HTTP: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ts.URL
}

func TestHTTPListsTheTools(t *testing.T) {
	session, _ := connectHTTP(t, newServer(t))
	list, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "execute_code,validate_code" {
		t.Fatalf("tools = %v", names)
	}
}

func TestHTTPExecutesPythonAndBash(t *testing.T) {
	session, _ := connectHTTP(t, newServer(t))
	cases := []struct{ language, code, want string }{
		{langPython, "print(sum(range(1, 11)))", "55"},
		{langBash, "echo $((6 * 7))", "42"},
	}
	for _, c := range cases {
		res := call(t, session, "execute_code", codeArgs(c.language, c.code))
		body := allText(res)
		if res.IsError || !strings.Contains(body, c.want) || !strings.Contains(body, "Exit code: 0") {
			t.Errorf("%s: got error=%v:\n%s", c.language, res.IsError, body)
		}
	}
}

func TestHTTPValidateReportsASyntaxError(t *testing.T) {
	session, _ := connectHTTP(t, newServer(t))
	bad := call(t, session, "validate_code", codeArgs(langPython, "def f(:"))
	if !bad.IsError || !strings.Contains(allText(bad), "SyntaxError") {
		t.Fatalf("got:\n%s", allText(bad))
	}
}

func TestHTTPTimeLimitStopsTheProgram(t *testing.T) {
	s := newServer(t)
	s.Runner.Timeout = 300 * time.Millisecond
	session, _ := connectHTTP(t, s)
	body := allText(call(t, session, "execute_code", codeArgs(langBash, "sleep 30")))
	if !strings.Contains(body, "Stopped after 300ms") {
		t.Fatalf("got:\n%s", body)
	}
}

func TestHTTPOutputCapIsReported(t *testing.T) {
	s := newServer(t)
	s.Runner.MaxOutput = 10
	session, _ := connectHTTP(t, s)
	body := allText(call(t, session, "execute_code", codeArgs(langPython, "print('y' * 100)")))
	if !strings.Contains(body, "Output was cut short") {
		t.Fatalf("got:\n%s", body)
	}
}

func TestHTTPUnsupportedLanguageIsAToolError(t *testing.T) {
	session, _ := connectHTTP(t, newServer(t))
	if res := call(t, session, "execute_code", codeArgs("cobol", "x")); !res.IsError {
		t.Fatalf("want a tool error, got:\n%s", allText(res))
	}
}

func TestHTTPHealthPaths(t *testing.T) {
	_, base := connectHTTP(t, newServer(t))
	for _, path := range []string{HealthPath, ReadinessPath} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(base + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", resp.StatusCode)
	}
}

// TestHTTPRoundTrip reports how long one execute_code call takes over HTTP; it
// asserts nothing about speed.
func TestHTTPRoundTrip(t *testing.T) {
	session, _ := connectHTTP(t, newServer(t))
	const calls = 20
	args := codeArgs(langBash, "echo hi")
	// The first call also warms the connection; the result must be right.
	if body := allText(call(t, session, "execute_code", args)); !strings.Contains(body, "hi") {
		t.Fatalf("got:\n%s", body)
	}
	start := time.Now()
	for range calls {
		call(t, session, "execute_code", args)
	}
	t.Logf("execute_code bash echo over HTTP: %d calls, mean round trip %s",
		calls, time.Since(start)/calls)

	start = time.Now()
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("tools/list over HTTP: %s", time.Since(start))
}
