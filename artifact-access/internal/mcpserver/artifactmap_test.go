package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testClient   = "test"
	queryTool    = "workspace_query"
	artifactsArg = "artifacts"
	sqlArg       = "sql"
	tableVMs     = "vms"
)

func decodeQuery(t *testing.T, raw string) (queryIn, error) {
	t.Helper()
	var in queryIn
	err := json.Unmarshal([]byte(raw), &in)
	return in, err
}

func TestArtifactsDecodesFromObject(t *testing.T) {
	in, err := decodeQuery(t, `{"artifacts":{"vms":"k1","nodes":"k2"},"sql":"SELECT 1"}`)
	if err != nil || len(in.Artifacts) != 2 || in.Artifacts["vms"] != "k1" || in.Artifacts["nodes"] != "k2" {
		t.Fatalf("in = %v err = %v", in.Artifacts, err)
	}
}

func TestArtifactsDecodesFromJSONString(t *testing.T) {
	in, err := decodeQuery(t, `{"artifacts":"{\"proxmox_vms\": \"k1\"}","sql":"SELECT 1"}`)
	if err != nil || len(in.Artifacts) != 1 || in.Artifacts["proxmox_vms"] != "k1" {
		t.Fatalf("in = %v err = %v", in.Artifacts, err)
	}
}

func TestArtifactsStringThatIsNotJSONFailsWithTheShape(t *testing.T) {
	_, err := decodeQuery(t, `{"artifacts":"proxmox_vms=k1"}`)
	if err == nil || !strings.Contains(err.Error(), artifactsShape) {
		t.Fatalf("err = %v", err)
	}
}

func TestArtifactsStringThatIsNotAnObjectFailsWithTheShape(t *testing.T) {
	for _, raw := range []string{`"[\"a\"]"`, `"42"`, `"\"k\""`, `""`} {
		_, err := decodeQuery(t, `{"artifacts":`+raw+`}`)
		if err == nil || !strings.Contains(err.Error(), artifactsShape) {
			t.Fatalf("%s: err = %v", raw, err)
		}
	}
}

func TestArtifactsStringHoldingNonStringValuesFailsWithTheShape(t *testing.T) {
	_, err := decodeQuery(t, `{"artifacts":"{\"a\": 1}"}`)
	if err == nil || !strings.Contains(err.Error(), artifactsShape) {
		t.Fatalf("err = %v", err)
	}
}

func TestArtifactsEmptyMapDecodes(t *testing.T) {
	for _, raw := range []string{`{}`, `"{}"`} {
		in, err := decodeQuery(t, `{"artifacts":`+raw+`}`)
		if err != nil || len(in.Artifacts) != 0 {
			t.Fatalf("%s: in = %v err = %v", raw, in.Artifacts, err)
		}
	}
}

// connect returns a client session on a real server, so the SDK's schema
// validation runs before the handler.
func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := s.MCP().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: testClient, Version: "v1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callQuery(t *testing.T, q *fakeQuerier, args any) (*mcp.CallToolResult, error) {
	t.Helper()
	s := newServer(nil)
	s.Query = q
	return connect(t, s).CallTool(context.Background(), &mcp.CallToolParams{Name: queryTool, Arguments: args})
}

// failure returns the message of a rejected call, or "" when the call succeeded.
func failure(res *mcp.CallToolResult, err error) string {
	if err != nil {
		return err.Error()
	}
	if res.IsError {
		return text(res)
	}
	return ""
}

func TestQueryEndToEndAcceptsObjectAndString(t *testing.T) {
	forms := map[string]any{
		"object": map[string]any{tableVMs: "k1"},
		"string": `{"vms": "k1"}`,
	}
	for name, artifacts := range forms {
		q := &fakeQuerier{out: "n\n3\n"}
		res, err := callQuery(t, q, map[string]any{artifactsArg: artifacts, sqlArg: selectOne})
		if msg := failure(res, err); msg != "" {
			t.Fatalf("%s: rejected: %s", name, msg)
		}
		if q.artifacts["vms"] != "k1" || q.sql != selectOne || text(res) != "n\n3\n" {
			t.Fatalf("%s: engine got %v %q, result %q", name, q.artifacts, q.sql, text(res))
		}
	}
}

func TestQueryEndToEndRejectsBadStringsWithTheShape(t *testing.T) {
	for _, artifacts := range []string{"not json", "[1]"} {
		q := &fakeQuerier{}
		res, err := callQuery(t, q, map[string]any{artifactsArg: artifacts, sqlArg: selectOne})
		if msg := failure(res, err); !strings.Contains(msg, artifactsShape) || q.sql != "" {
			t.Fatalf("%q: msg = %q engine sql = %q", artifacts, msg, q.sql)
		}
	}
}

func TestQueryEndToEndPassesAnEmptyMapToTheEngine(t *testing.T) {
	q := &fakeQuerier{out: "x"}
	res, err := callQuery(t, q, map[string]any{artifactsArg: "{}", sqlArg: selectOne})
	if msg := failure(res, err); msg != "" || len(q.artifacts) != 0 || q.sql != selectOne {
		t.Fatalf("msg = %q artifacts = %v sql = %q", msg, q.artifacts, q.sql)
	}
}

func TestQueryEndToEndStillRejectsOtherTypes(t *testing.T) {
	for _, artifacts := range []any{42, []string{"a"}, map[string]any{"vms": 1}} {
		q := &fakeQuerier{}
		res, err := callQuery(t, q, map[string]any{artifactsArg: artifacts, sqlArg: selectOne})
		if failure(res, err) == "" || q.sql != "" {
			t.Fatalf("%v reached the engine", artifacts)
		}
	}
}

func TestQueryEndToEndStillRejectsUnknownProperties(t *testing.T) {
	q := &fakeQuerier{}
	res, err := callQuery(t, q, map[string]any{artifactsArg: map[string]any{tableVMs: "k1"}, sqlArg: selectOne, "extra": 1})
	if failure(res, err) == "" || q.sql != "" {
		t.Fatal("an unknown property reached the engine")
	}
}

func TestQueryEndToEndRejectsNullArtifacts(t *testing.T) {
	q := &fakeQuerier{}
	res, err := callQuery(t, q, map[string]any{artifactsArg: nil, sqlArg: selectOne})
	if failure(res, err) == "" || q.sql != "" {
		t.Fatal("null artifacts reached the engine")
	}
}

func TestOtherToolsStayStrict(t *testing.T) {
	cs := connect(t, newServer(map[string]string{"k": "a\n"}))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "artifact_head", Arguments: map[string]any{"key": "k", "lines": "2"}})
	if failure(res, err) == "" {
		t.Fatal("artifact_head accepted lines as a string")
	}
}

func TestQueryToolSchemaPublishesBothForms(t *testing.T) {
	s := newServer(nil)
	s.Query = &fakeQuerier{}
	for tool, err := range connect(t, s).Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name != queryTool {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(raw), `"type":["object","string"]`) || !strings.Contains(string(raw), `"required":["artifacts","sql"]`) {
			t.Fatalf("schema = %s", raw)
		}
	}
}
