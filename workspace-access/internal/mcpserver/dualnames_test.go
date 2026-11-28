package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func descriptions(t *testing.T, cs *mcp.ClientSession) map[string]string {
	t.Helper()
	out := map[string]string{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out[tool.Name] = tool.Description
	}
	return out
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("%s returned an error: %v", tool, res.Content)
	}
	return text(res)
}

func TestTheWorkspaceTextToolsAreServedBesideTheArtifactOnes(t *testing.T) {
	cs := connect(t, newServer(nil))
	got := descriptions(t, cs)
	for _, name := range []string{toolHead, toolTail, toolGrep,
		toolArtifactHead, toolArtifactTail, toolArtifactGrep} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s is not served: %v", name, got)
		}
	}
}

func TestEachOldNameAnswersLikeItsNewName(t *testing.T) {
	cs := connect(t, newServer(map[string]string{"k": "alpha\nbeta\ngamma\n"}))
	cases := []struct {
		old, current string
		args         map[string]any
	}{
		{toolArtifactHead, toolHead, map[string]any{keyArg: "k", argLines: 2}},
		{toolArtifactTail, toolTail, map[string]any{keyArg: "k", argLines: 2}},
		{toolArtifactGrep, toolGrep, map[string]any{keyArg: "k", patternArg: "a$"}},
	}
	for _, c := range cases {
		if o, n := call(t, cs, c.old, c.args), call(t, cs, c.current, c.args); o != n || o == "" {
			t.Errorf("%s = %q, %s = %q", c.old, o, c.current, n)
		}
	}
}

func TestOldNamesSayTheyAreDeprecatedAndNewOnesDoNot(t *testing.T) {
	s := newServer(nil)
	s.Query = &fakeQuerier{}
	got := descriptions(t, connect(t, s))
	for old, use := range map[string]string{
		toolArtifactHead: toolHead, toolArtifactTail: toolTail, toolArtifactGrep: toolGrep,
		"artifact_count": toolQuery, "artifact_rows": toolQuery,
		"artifact_select": toolQuery, "artifact_jq": toolQuery,
	} {
		d := got[old]
		if !strings.HasPrefix(d, "Deprecated:") || !strings.Contains(d, "use "+use) {
			t.Errorf("%s description %q does not point to %s", old, d, use)
		}
	}
	for _, name := range []string{toolHead, toolTail, toolGrep, toolQuery} {
		if strings.Contains(got[name], "Deprecated") {
			t.Errorf("%s is described as deprecated: %q", name, got[name])
		}
	}
}

func TestTheFirstCallUnderAnOldNameWarnsOnce(t *testing.T) {
	var mu sync.Mutex
	var warnings []string
	s := newServer(map[string]string{"k": "a\nb\n"})
	s.Warnf = func(f string, a ...any) { mu.Lock(); warnings = append(warnings, fmt.Sprintf(f, a...)); mu.Unlock() }
	cs := connect(t, s)

	call(t, cs, toolHead, map[string]any{keyArg: "k"})
	if len(warnings) != 0 {
		t.Fatalf("the new name warned: %v", warnings)
	}
	for i := 0; i < 3; i++ {
		call(t, cs, toolArtifactHead, map[string]any{keyArg: "k"})
	}
	call(t, cs, "artifact_rows", map[string]any{keyArg: "k"})
	if len(warnings) != 2 {
		t.Fatalf("want one warning per old tool, got %v", warnings)
	}
	if !strings.Contains(warnings[0], toolArtifactHead) || !strings.Contains(warnings[0], "use workspace_head") ||
		!strings.Contains(warnings[1], "use workspace_query") {
		t.Errorf("warnings = %v", warnings)
	}
}
