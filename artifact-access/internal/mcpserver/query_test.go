package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const selectOne = "SELECT 1"

type fakeQuerier struct {
	artifacts map[string]string
	sql       string
	out       string
	err       error
}

func (f *fakeQuerier) Query(_ context.Context, artifacts map[string]string, sql string) (string, error) {
	f.artifacts, f.sql = artifacts, sql
	return f.out, f.err
}

func (f *fakeQuerier) Hint() string { return "The SQL is fake." }

// toolNames lists the tools a client sees on a connected server.
func toolNames(t *testing.T, s *Server) []string {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := s.MCP().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	var names []string
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	return names
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestQueryToolIsNotOfferedByDefault(t *testing.T) {
	names := toolNames(t, newServer(nil))
	if contains(names, "workspace_query") {
		t.Fatalf("workspace_query offered without an engine: %v", names)
	}
	if !contains(names, "artifact_jq") || !contains(names, "artifact_count") {
		t.Fatalf("the existing tools changed: %v", names)
	}
}

func TestQueryToolIsOfferedWhenAnEngineIsSet(t *testing.T) {
	s := newServer(nil)
	s.Query = &fakeQuerier{}
	names := toolNames(t, s)
	if !contains(names, "workspace_query") {
		t.Fatalf("workspace_query missing: %v", names)
	}
	if len(names) != 8 {
		t.Fatalf("want the 7 existing tools plus workspace_query, got %v", names)
	}
}

func TestQueryHandlerReturnsRows(t *testing.T) {
	q := &fakeQuerier{out: "n\n3\n(1 row)\n"}
	s := newServer(nil)
	s.Query = q
	res, out, err := s.query(context.Background(), nil, queryIn{Artifacts: map[string]string{"p": "k"}, SQL: selectOne})
	if err != nil {
		t.Fatal(err)
	}
	if out.Output != q.out || text(res) != q.out {
		t.Fatalf("out = %q content = %q", out.Output, text(res))
	}
	if q.artifacts["p"] != "k" || q.sql != selectOne {
		t.Fatalf("handler passed %v %q", q.artifacts, q.sql)
	}
}

func TestQueryHandlerReturnsErrorsAsToolErrors(t *testing.T) {
	s := newServer(nil)
	s.Query = &fakeQuerier{err: errors.New("query timed out")}
	if _, _, err := s.query(context.Background(), nil, queryIn{SQL: selectOne}); err == nil {
		t.Fatal("an engine error must reach the model as a tool error")
	}
}

func TestQueryHandlerCapsALargeResult(t *testing.T) {
	s := newServer(nil)
	s.MaxOutputBytes = 20
	s.Query = &fakeQuerier{out: strings.Repeat("row row row\n", 10)}
	_, out, err := s.query(context.Background(), nil, queryIn{SQL: selectOne})
	if err != nil || !strings.Contains(out.Output, "truncated") {
		t.Fatalf("out = %q err = %v", out.Output, err)
	}
}

func TestQueryToolDescriptionCarriesTheEngineHint(t *testing.T) {
	s := newServer(nil)
	s.Query = &fakeQuerier{}
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := s.MCP().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "workspace_query" && !strings.Contains(tool.Description, "The SQL is fake.") {
			t.Fatalf("description lacks the engine hint: %q", tool.Description)
		}
	}
}
