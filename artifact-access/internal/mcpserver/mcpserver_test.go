package mcpserver

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeFetcher struct {
	data map[string]string
	err  error
}

func (f fakeFetcher) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	s, ok := f.data[key]
	if !ok {
		return nil, fmt.Errorf("no such key %q", key)
	}
	return io.NopCloser(strings.NewReader(s)), nil
}

func newServer(data map[string]string) *Server {
	return New(fakeFetcher{data: data})
}

func TestHeadHandler(t *testing.T) {
	s := newServer(map[string]string{"k": "a\nb\nc\nd\n"})
	res, out, err := s.head(context.Background(), nil, headIn{Key: "k", Lines: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "a\nb\n[lines 0-1 of 4]\n" {
		t.Fatalf("structured out %q", out.Output)
	}
	if got := text(res); got != "a\nb\n[lines 0-1 of 4]\n" {
		t.Fatalf("content %q", got)
	}
}

func TestTailHandler(t *testing.T) {
	s := newServer(map[string]string{"k": "a\nb\nc\nd\n"})
	_, out, err := s.tail(context.Background(), nil, tailIn{Key: "k", Lines: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "c\nd\n[lines 2-3 of 4]\n" {
		t.Fatalf("got %q", out.Output)
	}
	if _, _, err := s.tail(context.Background(), nil, tailIn{Key: ""}); err == nil {
		t.Fatal("empty key must error")
	}
}

func TestGrepHandler(t *testing.T) {
	s := newServer(map[string]string{"k": "apple\nbanana\navocado\n"})
	_, out, err := s.grep(context.Background(), nil, grepIn{Key: "k", Pattern: "^a"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "apple\navocado\n" {
		t.Fatalf("got %q", out.Output)
	}
	if _, _, err := s.grep(context.Background(), nil, grepIn{Key: "k", Pattern: "("}); err == nil {
		t.Fatal("bad pattern must surface as a tool error")
	}
}

func TestRowsHandler(t *testing.T) {
	s := newServer(map[string]string{"k": "r0\nr1\nr2\nr3\n"})
	_, out, err := s.rows(context.Background(), nil, rowsIn{Key: "k", Start: 1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "r1\nr2\n[lines 1-2 of 4]\n" {
		t.Fatalf("got %q", out.Output)
	}
	if _, _, err := s.rows(context.Background(), nil, rowsIn{Key: "missing", Start: 0, Limit: 1}); err == nil {
		t.Fatal("missing key must error")
	}
}

func TestCountHandler(t *testing.T) {
	s := newServer(map[string]string{"k": "a\nb\nc\n"})
	_, out, err := s.count(context.Background(), nil, keyIn{Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "3\n" {
		t.Fatalf("got %q", out.Output)
	}
}

func TestCountHandlerSkipsHeaderAndBlanks(t *testing.T) {
	// A kubectl-style table: header + 2 data rows + trailing blank -> the tool
	// reports the entity count (2), not the raw line count (4).
	s := newServer(map[string]string{"k": "APIVERSION KIND NAME\nv1 Namespace a\nv1 Namespace b\n\n"})
	_, out, err := s.count(context.Background(), nil, keyIn{Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "2\n" {
		t.Fatalf("got %q (want \"2\\n\" - header and blank excluded)", out.Output)
	}
}

func TestJqHandler(t *testing.T) {
	s := newServer(map[string]string{"k": `{"a":[1,2,3]}`})
	_, out, err := s.jq(context.Background(), nil, jqIn{Key: "k", Program: ".a | length"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "3\n" {
		t.Fatalf("got %q", out.Output)
	}
}

func TestSelectHandler(t *testing.T) {
	s := newServer(map[string]string{"k": "name,ns\npod,default\n"})
	_, out, err := s.selectCSV(context.Background(), nil, selectIn{Key: "k", Columns: []string{"ns"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != "ns\ndefault\n" {
		t.Fatalf("got %q", out.Output)
	}
}

func TestEmptyKeyIsError(t *testing.T) {
	s := newServer(nil)
	if _, _, err := s.head(context.Background(), nil, headIn{Key: ""}); err == nil {
		t.Fatal("empty key must error")
	}
}

func TestFetchErrorPropagates(t *testing.T) {
	s := New(fakeFetcher{err: fmt.Errorf("nats down")})
	if _, _, err := s.head(context.Background(), nil, headIn{Key: "k"}); err == nil {
		t.Fatal("fetch error must propagate as a tool error")
	}
}

func TestCapOutputTruncatesAtLineBoundary(t *testing.T) {
	s := &Server{MaxOutputBytes: 10}
	got := s.capOutput("line1\nline2\nline3\n")
	if !strings.HasPrefix(got, "line1\n") {
		t.Fatalf("should keep whole first line, got %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("should note truncation, got %q", got)
	}
}

func TestMCPBuilds(t *testing.T) {
	if newServer(nil).MCP() == nil {
		t.Fatal("MCP() must build a server")
	}
}

func TestDelimiterOrComma(t *testing.T) {
	if delimiterOrComma("") != ',' {
		t.Fatal("empty must default to comma")
	}
	if delimiterOrComma(";") != ';' {
		t.Fatal("first rune used")
	}
}

func text(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// A page larger than the output cap keeps its position line, and the range still
// matches the lines returned.
func TestLargePageKeepsItsPosition(t *testing.T) {
	body := strings.Repeat("0123456789012345678901234567890123456789\n", 5000) // about 205 KB
	s := newServer(map[string]string{"k": body})
	_, out, err := s.rows(context.Background(), nil, rowsIn{Key: "k", Start: 0, Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Output, "[truncated at") {
		t.Fatal("the output cap cut the page; the position line must fit within it")
	}
	lines := strings.Split(strings.TrimSuffix(out.Output, "\n"), "\n")
	last := lines[len(lines)-1]
	var a, b, total int
	if _, err := fmt.Sscanf(last, "[lines %d-%d of %d]", &a, &b, &total); err != nil {
		t.Fatalf("no position line at the end: %q", last)
	}
	if len(lines)-1 != b-a+1 || total != 5000 {
		t.Fatalf("returned %d lines, position says %d-%d of %d", len(lines)-1, a, b, total)
	}
}
