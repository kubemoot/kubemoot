package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/runner"
)

// codeArgs builds execute_code and validate_code arguments.
func codeArgs(language, code string) map[string]any {
	return map[string]any{"language": language, "code": code}
}

func newServer(t *testing.T) *Server {
	t.Helper()
	return &Server{Runner: &runner.Runner{Timeout: 10 * time.Second, MaxOutput: 64 * 1024, TempDir: t.TempDir()}}
}

func allText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// connect starts the server and a client over in-memory transports.
func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	srv, err := s.MCP()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	return res
}

func TestToolsAreListedWithTheLanguageEnum(t *testing.T) {
	session := connect(t, newServer(t))
	list, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		schema := tool.InputSchema.(map[string]any)
		lang := schema["properties"].(map[string]any)["language"].(map[string]any)
		if got := lang["enum"]; len(got.([]any)) != 2 {
			t.Errorf("%s language enum = %v", tool.Name, got)
		}
		if req := schema["required"].([]any); len(req) != 2 {
			t.Errorf("%s required = %v, want language and code", tool.Name, req)
		}
	}
	if strings.Join(names, ",") != "execute_code,validate_code" {
		t.Fatalf("tools = %v", names)
	}
}

func TestExecuteOverTheProtocol(t *testing.T) {
	session := connect(t, newServer(t))
	res := call(t, session, "execute_code", map[string]any{"language": "python", "code": "print(6 * 7)", "filename": "answer.py"})
	body := allText(res)
	for _, want := range []string{"42", "Exit code: 0", "File: answer.py"} {
		if !strings.Contains(body, want) {
			t.Errorf("result lacks %q:\n%s", want, body)
		}
	}
	if res.IsError {
		t.Fatal("a successful run is not a tool error")
	}
}

func TestExecuteReadingStdinFinishes(t *testing.T) {
	session := connect(t, newServer(t))
	res := call(t, session, "execute_code", codeArgs("python",
		"import sys\ndata = sys.stdin.read().splitlines()\nprint(data[0])"))
	body := allText(res)
	if !strings.Contains(body, "IndexError") || !strings.Contains(body, "Exit code: 1") {
		t.Fatalf("want the program to fail fast on empty stdin, got:\n%s", body)
	}
}

func TestExecuteFailureShowsStderrAndExitCode(t *testing.T) {
	s := newServer(t)
	res, out, err := s.execute(context.Background(), nil, CodeIn{Language: "bash", Code: "echo broken >&2; exit 2"})
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 2 || out.Stderr != "broken" {
		t.Fatalf("got %+v", out)
	}
	if body := allText(res); !strings.Contains(body, "stderr:\nbroken") {
		t.Fatalf("got:\n%s", body)
	}
}

func TestExecuteTimeoutIsReported(t *testing.T) {
	s := newServer(t)
	s.Runner.Timeout = 300 * time.Millisecond
	res, out, err := s.execute(context.Background(), nil, CodeIn{Language: "bash", Code: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.TimedOut || !strings.Contains(allText(res), "Stopped after 300ms") {
		t.Fatalf("got %+v:\n%s", out, allText(res))
	}
}

func TestExecuteTruncationIsReported(t *testing.T) {
	s := newServer(t)
	s.Runner.MaxOutput = 10
	res, _, err := s.execute(context.Background(), nil, CodeIn{Language: "python", Code: "print('y' * 100)"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(allText(res), "Output was cut short") {
		t.Fatalf("got:\n%s", allText(res))
	}
}

func TestUnsupportedLanguageIsAToolError(t *testing.T) {
	session := connect(t, newServer(t))
	for _, tool := range []string{"execute_code", "validate_code"} {
		res := call(t, session, tool, codeArgs("cobol", "x"))
		if !res.IsError {
			t.Fatalf("%s: want a tool error, got:\n%s", tool, allText(res))
		}
	}
}

func TestValidate(t *testing.T) {
	session := connect(t, newServer(t))
	ok := call(t, session, "validate_code", codeArgs("python", "print(1)"))
	if ok.IsError || !strings.Contains(allText(ok), "Syntax is valid.") {
		t.Fatalf("got:\n%s", allText(ok))
	}
	bad := call(t, session, "validate_code", codeArgs("python", "def f(:"))
	if !bad.IsError || !strings.Contains(allText(bad), "SyntaxError") {
		t.Fatalf("got:\n%s", allText(bad))
	}
}

func TestCleanRepairsACutRune(t *testing.T) {
	if got := clean("  caf\xc3 "); got != "caf" {
		t.Fatalf("got %q", got)
	}
}
