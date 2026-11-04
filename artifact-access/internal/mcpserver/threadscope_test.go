package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/artifact-access/internal/threadkey"
)

const (
	scopeThread = "crew-homelab-pilot/homelab-pilot/0071d018-24cb-4532-a560-fc12a788e654/"
	scopeShort  = "k8s-workloads/001-resources_list"
	scopeBody   = "NAME SCHEDULE\nbackup 0 2 * * *\nlog-cleanup @daily\n"
	keyArg      = "key"
	okRows      = "ok\n"
	// the second failure of thread 0071d018: the thread id typed with one hyphen too many
	mistypedThread = "crew-homelab-pilot/homelab-pilot/0071d018-24cb-45-32-a560-fc12a788e654/"
)

func scopedServer() *Server {
	s := newServer(map[string]string{scopeThread + scopeShort: scopeBody})
	s.Query = &fakeQuerier{out: okRows}
	return s
}

// callScoped runs one tool over an in-memory MCP connection with the thread in _meta, as the
// agent runtime sends it through the gateway. A nil meta sends none.
func callScoped(t *testing.T, s *Server, meta map[string]any, name string, args map[string]any) (string, error) {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := s.MCP().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: testClient, Version: "v1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: meta})
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", &toolError{text(res)}
	}
	return text(res), nil
}

type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

func threadMeta() map[string]any { return map[string]any{threadkey.MetaKey: scopeThread} }

func TestShortKeyReadsInsideTheThread(t *testing.T) {
	s := scopedServer()
	for _, tool := range []string{"artifact_head", "artifact_tail", "artifact_rows"} {
		got, err := callScoped(t, s, threadMeta(), tool, map[string]any{keyArg: scopeShort})
		if err != nil || !strings.Contains(got, "log-cleanup") {
			t.Fatalf("%s with a short key: %q, %v", tool, got, err)
		}
	}
	got, err := callScoped(t, s, threadMeta(), "artifact_grep", map[string]any{keyArg: scopeShort, "pattern": "backup"})
	if err != nil || !strings.Contains(got, "backup") {
		t.Fatalf("artifact_grep with a short key: %q, %v", got, err)
	}
	got, err = callScoped(t, s, threadMeta(), "artifact_count", map[string]any{keyArg: scopeShort})
	if err != nil || strings.TrimSpace(got) != "2" {
		t.Fatalf("artifact_count with a short key: %q, %v", got, err)
	}
}

func TestLongKeyOfTheThreadStillWorks(t *testing.T) {
	got, err := callScoped(t, scopedServer(), threadMeta(), "artifact_head", map[string]any{keyArg: scopeThread + scopeShort})
	if err != nil || !strings.Contains(got, "backup") {
		t.Fatalf("long key: %q, %v", got, err)
	}
}

func TestMistypedThreadIdIsRefusedAndShortKeyNeedsNoId(t *testing.T) {
	s := scopedServer()
	_, err := callScoped(t, s, threadMeta(), "artifact_head", map[string]any{keyArg: mistypedThread + scopeShort})
	if err == nil || !strings.Contains(err.Error(), "another thread") {
		t.Fatalf("a key with a mistyped thread id must be refused as another thread, got %v", err)
	}
	if _, err := callScoped(t, s, threadMeta(), "artifact_head", map[string]any{keyArg: scopeShort}); err != nil {
		t.Fatalf("the short key has no id to mistype: %v", err)
	}
}

func TestKeyOfAnotherThreadIsRefused(t *testing.T) {
	other := "crew-homelab-pilot/homelab-pilot/11111111-2222-3333-4444-555555555555/" + scopeShort
	s := newServer(map[string]string{other: "secret\n"})
	_, err := callScoped(t, s, threadMeta(), "artifact_head", map[string]any{keyArg: other})
	if err == nil || !strings.Contains(err.Error(), "another thread") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

func TestWithoutAThreadKeysAreUnchanged(t *testing.T) {
	s := scopedServer()
	if _, err := callScoped(t, s, nil, "artifact_head", map[string]any{keyArg: scopeThread + scopeShort}); err != nil {
		t.Fatalf("long key without a thread: %v", err)
	}
	if _, err := callScoped(t, s, nil, "artifact_head", map[string]any{keyArg: scopeShort}); err == nil {
		t.Fatal("a short key without a thread has nothing to resolve against and must not be found")
	}
	bad := map[string]any{threadkey.MetaKey: "not-a-prefix"}
	if _, err := callScoped(t, s, bad, "artifact_head", map[string]any{keyArg: scopeShort}); err == nil {
		t.Fatal("a malformed thread value must behave like none")
	}
}

func TestQueryResolvesShortKeysAndPatterns(t *testing.T) {
	q := &fakeQuerier{out: okRows}
	s := newServer(nil)
	s.Query = q
	args := map[string]any{
		"artifacts": map[string]any{"cron": scopeShort, "all": "k8s-workloads/*-resources_list", "long": scopeThread + scopeShort},
		"sql":       selectOne,
	}
	if _, err := callScoped(t, s, threadMeta(), "workspace_query", args); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"cron": scopeThread + scopeShort,
		"all":  scopeThread + "k8s-workloads/*-resources_list",
		"long": scopeThread + scopeShort,
	}
	for table, key := range want {
		if q.artifacts[table] != key {
			t.Errorf("table %s resolved to %q, want %q", table, q.artifacts[table], key)
		}
	}
}

func TestQueryRefusesAKeyOfAnotherThread(t *testing.T) {
	q := &fakeQuerier{out: okRows}
	s := newServer(nil)
	s.Query = q
	args := map[string]any{
		"artifacts": map[string]any{"x": mistypedThread + scopeShort},
		"sql":       selectOne,
	}
	_, err := callScoped(t, s, threadMeta(), "workspace_query", args)
	if err == nil || !strings.Contains(err.Error(), "another thread") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if q.sql != "" {
		t.Fatal("the query must not run when a key is refused")
	}
}
