package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

// lineFixture is the cases shared with agent-runtime's ThreadCatalogTest.
type lineFixture struct {
	Prefix string `json:"prefix"`
	Cases  []struct {
		Name    string            `json:"name"`
		Key     string            `json:"key"`
		Size    uint64            `json:"size"`
		Headers map[string]string `json:"headers"`
		Line    string            `json:"line"`
	} `json:"cases"`
}

var startedField = regexp.MustCompile(` started=\S+\]$`)

func TestCatalogLinesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/catalog-lines.json")
	if err != nil {
		t.Fatal(err)
	}
	var f lineFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			entries := catalogEntries([]store.Meta{{Key: c.Key, Size: c.Size, Headers: c.Headers}}, f.Prefix, "")
			if len(entries) != 1 {
				t.Fatalf("got %d entries", len(entries))
			}
			got := startedField.ReplaceAllString(entries[0].line(), "]")
			if got != c.Line {
				t.Errorf("got  %s\nwant %s", got, c.Line)
			}
		})
	}
}

func TestCatalogLineCarriesWhenTheCallStarted(t *testing.T) {
	e := catalogEntry{key: "a/001-x", tool: "x", typ: "text/plain", rows: -1, bytes: 5, started: "2026-11-15T10:00:00Z"}
	if got := e.line(); !strings.HasSuffix(got, " error=false started=2026-11-15T10:00:00Z]") {
		t.Errorf("got %s", got)
	}
}

const catalogPrefix = "ns/crew/th1/"

func catalogStore() callStore {
	return callStore{map[string]object{
		callPods1:               call(toolPods, podsA, HeaderContentType, "application/json", headerRows, "2", headerStarted, "2026-11-15T10:00:00Z"),
		callPods2:               call(toolPods, podsB, HeaderContentType, "application/json", headerRows, "1"),
		callEvents:              call(toolEvents, eventsTable, HeaderContentType, "text/plain; profile=kubectl-table"),
		callBad:                 call(toolPods, errJS, headerError, "true"),
		callOther:               call(toolPods, podsB),
		callPrefix + "005-note": {body: "not a stored call", headers: map[string]string{}},
	}}
}

func TestCatalogListsTheThreadsCallsOnly(t *testing.T) {
	got, err := Catalog(context.Background(), catalogStore(), catalogPrefix, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "[CATALOG calls=4 errors=1 bytes=") {
		t.Fatalf("totals: %q", got)
	}
	for _, want := range []string{"key=k8s-workloads/001-pods_list ", "key=k8s-workloads/003-events_list ",
		"key=k8s-workloads/004-pods_list ", "started=2026-11-15T10:00:00Z]", "started=unknown]", "error=true"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, not := range []string{"th2", "005-note", "ns/crew/th1"} {
		if strings.Contains(got, not) {
			t.Errorf("must not list %q: %q", not, got)
		}
	}
}

func TestCatalogFiltersByTool(t *testing.T) {
	got, err := Catalog(context.Background(), catalogStore(), catalogPrefix, toolEvents)
	if err != nil || !strings.HasPrefix(got, "[CATALOG calls=1 errors=0 ") || strings.Contains(got, "pods_list") {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = Catalog(context.Background(), catalogStore(), catalogPrefix, "no_such_tool")
	if err != nil || !strings.Contains(got, "calls=0") || !strings.Contains(got, "nothing has been stored") {
		t.Fatalf("an unknown tool lists nothing: %q, %v", got, err)
	}
}

func TestCatalogOfAnEmptyThread(t *testing.T) {
	got, err := Catalog(context.Background(), callStore{}, catalogPrefix, "")
	if err != nil || !strings.HasPrefix(got, "[CATALOG calls=0 errors=0 bytes=0]") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestCatalogNeedsAThread(t *testing.T) {
	if _, err := Catalog(context.Background(), catalogStore(), "", ""); !errors.Is(err, ErrNoThread) {
		t.Fatalf("got %v", err)
	}
}

type failingLister struct{}

func (failingLister) List(context.Context, string) ([]store.Meta, error) {
	return nil, errors.New("nats down")
}

func TestCatalogReportsAListingFailure(t *testing.T) {
	if _, err := Catalog(context.Background(), failingLister{}, catalogPrefix, ""); err == nil || !strings.Contains(err.Error(), "nats down") {
		t.Fatalf("got %v", err)
	}
}

func TestCatalogCapsTheCallLinesAndCountsTheRest(t *testing.T) {
	objects := map[string]object{}
	for i := 0; i < MaxCatalogCalls+5; i++ {
		objects[fmt.Sprintf("%sa/%04d-x", catalogPrefix, i)] = call("x", "body")
	}
	got, err := Catalog(context.Background(), callStore{objects}, catalogPrefix, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, catalogCallStart); n != MaxCatalogCalls {
		t.Errorf("%d call lines", n)
	}
	if !strings.HasPrefix(got, fmt.Sprintf("[CATALOG calls=%d ", MaxCatalogCalls+5)) ||
		!strings.Contains(got, "[5 more calls are not listed; pass tool") {
		t.Errorf("totals or the note are wrong: %q", got[:120])
	}
}

func TestArgsSummarySharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/catalog-lines.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct{ Name, Input, Output string } `json:"argsSummaries"`
	}
	if err := json.Unmarshal(raw, &f); err != nil || len(f.Cases) == 0 {
		t.Fatalf("fixture: %v (%d cases)", err, len(f.Cases))
	}
	for _, c := range f.Cases {
		if got := argsSummary(c.Input); got != c.Output {
			t.Errorf("%s: argsSummary(%q) = %q, want %q", c.Name, c.Input, got, c.Output)
		}
	}
}

func TestArgsSummary(t *testing.T) {
	cases := map[string]string{
		"":                       "{}",
		"  \n\t ":                "{}",
		`{"a": [1, 2]}`:          `{"a": (1, 2)}`,
		"{\"a\":\n\n  1}":        `{"a": 1}`,
		"{\"a\":   \t 1}":        `{"a": 1}`,
		strings.Repeat("x", 61):  strings.Repeat("x", 60) + "... (args cut at 60 of 61 chars)",
		strings.Repeat("é", 100): strings.Repeat("é", 60) + "... (args cut at 60 of 100 chars)",
	}
	for in, want := range cases {
		if got := argsSummary(in); got != want {
			t.Errorf("argsSummary(%q) = %q, want %q", in, got, want)
		}
	}
}
