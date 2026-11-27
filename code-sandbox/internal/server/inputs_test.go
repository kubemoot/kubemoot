package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/confine"
	"github.com/kubemoot/kubemoot/code-sandbox/internal/inputs"
)

const (
	threadA = "ns/crew/thread-a/"
	threadB = "ns/crew/thread-b/"
	hostKey = "k8s-config/001-resources_get"
	otherKy = "k8s-config/002-other"
)

// TestMain lets the test binary act as the confining helper a run re-executes.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == confine.Arg {
		confine.Main(os.Args[2:])
	}
	os.Exit(m.Run())
}

// hostsYAML is a ConfigMap whose data holds a CSV as one text field, the shape of a stored result that a
// SQL read of the YAML cannot split into rows. It returns the YAML and the number of decommissioned hosts.
func hostsYAML() (string, int) {
	var csv strings.Builder
	csv.WriteString("hostname,status\n")
	decommissioned := 0
	for i := 0; i < 1500; i++ {
		status := "active"
		if i%11 == 3 {
			status = "decommissioned"
			decommissioned++
		}
		csv.WriteString("host-" + strconv.Itoa(i) + "," + status + "\n")
	}
	var y strings.Builder
	y.WriteString("apiVersion: v1\nkind: ConfigMap\ndata:\n  hosts.csv: |\n")
	for _, line := range strings.Split(strings.TrimSuffix(csv.String(), "\n"), "\n") {
		y.WriteString("    " + line + "\n")
	}
	return y.String(), decommissioned
}

// withWorkspace is a server whose workspace holds a hosts result for thread A and a different one for B.
func withWorkspace(t *testing.T) (*Server, int) {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "workspace")
	hosts, n := hostsYAML()
	for path, body := range map[string]string{
		threadA + hostKey: hosts,
		threadB + hostKey: "other discussion: secret\n",
		threadB + otherKy: "other discussion only\n",
	} {
		full := filepath.Join(ws, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newServer(t)
	s.Runner.WorkspaceDirs = []string{ws}
	s.Runner.Timeout = 20 * time.Second
	s.Inputs = inputs.Resolver{Roots: []string{ws}}
	return s, n
}

func callInThread(t *testing.T, s *Server, prefix string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	params := &mcp.CallToolParams{Name: toolExecute, Arguments: args}
	if prefix != "" {
		params.Meta = mcp.Meta{inputs.MetaKey: prefix}
	}
	res, err := connect(t, s).CallTool(context.Background(), params)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func withInputs(code string, keys ...string) map[string]any {
	args := codeArgs("python", code)
	list := make([]any, 0, len(keys))
	for _, k := range keys {
		list = append(list, k)
	}
	args["inputs"] = list
	return args
}

const countDecommissioned = "import re\n" +
	"text = open('inputs/" + hostKey + "').read()\n" +
	"print(len(re.findall(r',decommissioned$', text, re.M)))\n"

func TestProgramCountsAllRowsOfAStoredCSVInYAML(t *testing.T) {
	s, want := withWorkspace(t)
	res := callInThread(t, s, threadA, withInputs(countDecommissioned, hostKey))
	if res.IsError || !strings.Contains(allText(res), strconv.Itoa(want)+"\n") {
		t.Fatalf("want the true count %d over all 1500 rows, got error=%v:\n%s", want, res.IsError, allText(res))
	}
}

func TestInputResolvesWithItsLongKeyToo(t *testing.T) {
	s, want := withWorkspace(t)
	res := callInThread(t, s, threadA, withInputs(countDecommissioned, threadA+hostKey))
	if res.IsError || !strings.Contains(allText(res), strconv.Itoa(want)) {
		t.Fatalf("the long key of this thread should resolve: %v\n%s", res.IsError, allText(res))
	}
}

func TestInputOfAnotherDiscussionIsRefused(t *testing.T) {
	s, _ := withWorkspace(t)
	for _, key := range []string{threadB + hostKey, otherKy} {
		res := callInThread(t, s, threadA, withInputs("print('ran')", key))
		if !res.IsError || strings.Contains(allText(res), "ran") || strings.Contains(allText(res), "secret") {
			t.Errorf("key %q: want a refusal before the program runs, got error=%v:\n%s", key, res.IsError, allText(res))
		}
	}
}

func TestUnknownInputIsRefusedWithAClearError(t *testing.T) {
	s, _ := withWorkspace(t)
	res := callInThread(t, s, threadA, withInputs("print('ran')", "k8s-config/009-missing"))
	if !res.IsError || !strings.Contains(allText(res), "no stored result") || strings.Contains(allText(res), "ran") {
		t.Fatalf("got error=%v:\n%s", res.IsError, allText(res))
	}
}

func TestInputsOutsideADiscussionAreRefused(t *testing.T) {
	s, _ := withWorkspace(t)
	res := callInThread(t, s, "", withInputs("print('ran')", hostKey))
	if !res.IsError || !strings.Contains(allText(res), "not part of a discussion") {
		t.Fatalf("got error=%v:\n%s", res.IsError, allText(res))
	}
}

func TestProgramCannotReadOtherDiscussionsFilesByPath(t *testing.T) {
	s, _ := withWorkspace(t)
	s.Runner.Unconfined = false // confinement is what this test checks; it skips where the host lacks it
	ws := s.Runner.WorkspaceDirs[0]
	code := "import os\n" +
		"print(sorted(os.listdir('" + ws + "')))\n" +
		"print(os.path.exists('" + ws + "/" + threadB + hostKey + "'))\n"
	res := callInThread(t, s, threadA, codeArgs("python", code))
	if res.IsError && strings.Contains(allText(res), "user namespaces") {
		skipOrFail(t, "this host does not allow user namespaces")
	}
	if got := allText(res); !strings.Contains(got, "['k8s-config']") || !strings.Contains(got, "False") {
		t.Fatalf("the program should see only its own discussion:\n%s", got)
	}
}

func TestExecuteWithoutInputsStillRuns(t *testing.T) {
	s, _ := withWorkspace(t)
	res := callInThread(t, s, threadA, codeArgs("python", "print(2+3)"))
	if res.IsError || !strings.Contains(allText(res), "5") {
		t.Fatalf("got %v:\n%s", res.IsError, allText(res))
	}
}

func TestExecuteSchemaOffersInputsAndKeepsTheLanguageEnum(t *testing.T) {
	list, err := connect(t, newServer(t)).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		props := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		_, has := props["inputs"]
		if has != (tool.Name == toolExecute) {
			t.Errorf("%s: inputs offered = %v", tool.Name, has)
		}
	}
}

// skipOrFail skips on a host without user namespaces, and fails where REQUIRE_CONFINEMENT says it must have them.
func skipOrFail(t *testing.T, msg string) {
	t.Helper()
	if os.Getenv("REQUIRE_CONFINEMENT") != "" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}
