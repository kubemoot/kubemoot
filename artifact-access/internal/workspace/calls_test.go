package workspace

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

const (
	callPrefix = "ns/crew/th1/k8s-workloads/"
	callPods1  = callPrefix + "001-pods_list"
	callPods2  = callPrefix + "002-pods_list"
	callEvents = callPrefix + "003-events_list"
	callBad    = callPrefix + "004-pods_list"
	callOther  = "ns/crew/th2/k8s-workloads/001-pods_list"
)

type object struct {
	body    string
	headers map[string]string
}

// callStore is an object store whose objects carry headers, as the runtime writes them.
type callStore struct{ objects map[string]object }

func (c callStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	o, ok := c.objects[key]
	if !ok {
		return nil, errors.New("no such object")
	}
	return io.NopCloser(strings.NewReader(o.body)), nil
}

func (c callStore) Describe(_ context.Context, key string) (store.Meta, error) {
	o, ok := c.objects[key]
	if !ok {
		return store.Meta{}, errors.New("no such object")
	}
	return store.Meta{Key: key, Size: uint64(len(o.body)), Headers: o.headers}, nil
}

func (c callStore) List(_ context.Context, prefix string) ([]store.Meta, error) {
	var out []store.Meta
	for k, o := range c.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, store.Meta{Key: k, Size: uint64(len(o.body)), Headers: o.headers})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// call is a stored tool call: the headers the runtime sets, with the extra ones given.
func call(tool, body string, extra ...string) object {
	h := map[string]string{
		headerOperation: operationExecuteTool, headerTool: tool, headerError: "false", headerFormat: "text",
	}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return object{body: body, headers: h}
}

func callService(t *testing.T, objects map[string]object) *Service {
	t.Helper()
	return &Service{
		Dir: t.TempDir(), Engine: SQLite{}, Store: callStore{objects}, MaxArtifactBytes: 1 << 20,
		Limits: Limits{Timeout: 10 * time.Second, MaxRows: 100, MemoryLimit: mem256},
	}
}

const (
	podsA = `{"items":[{"kind":"Pod","metadata":{"name":"a","namespace":"x"}},{"kind":"Pod","metadata":{"name":"b","namespace":"y"}}]}`
	podsB = `{"items":[{"kind":"Pod","metadata":{"name":"c","namespace":"x"}}]}`
	errJS = `{"error": "forbidden"}`
)

func TestPerCallKeyIsOneTable(t *testing.T) {
	s := callService(t, map[string]object{callPods1: call(toolPods, podsA, headerFormat, "json")})

	got := query(t, s, map[string]string{tblPods: callPods1},
		"SELECT metadata->>'namespace' AS ns, count(*) AS n FROM pods GROUP BY ns ORDER BY ns")

	if want := "ns | n\nx | 1\ny | 1\n(2 rows)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPerCallKeyReadsATextTableByDetection(t *testing.T) {
	s := callService(t, map[string]object{callPods1: call(toolPods, podsListTable)})

	got := query(t, s, map[string]string{tblPods: callPods1}, countPodsSQL)

	if want := oneRowFive; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

const oneRowFive = "n\n5\n(1 row)\n"

func TestHeaderFormatJSONIsBelievedForAJSONBodyDetectionWouldCallText(t *testing.T) {
	// A bare list of numbers is valid JSON, and a header saying json reads it as JSON.
	s := callService(t, map[string]object{callPods1: call(toolPods, `[{"v":1},{"v":2}]`, headerFormat, "json")})

	got := query(t, s, map[string]string{"t": callPods1}, "SELECT sum(v) AS total FROM t")

	if want := "total\n3\n(1 row)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHeaderSayingJSONOverABodyThatIsNotJSONFallsBackToDetection(t *testing.T) {
	s := callService(t, map[string]object{callPods1: call(toolPods, podsListTable, headerFormat, "json")})

	got := query(t, s, map[string]string{tblPods: callPods1}, countPodsSQL)

	if got != oneRowFive {
		t.Fatalf("got %q", got)
	}
}

func TestAnErrorCallQueriedDirectlyIsReadAndNoted(t *testing.T) {
	s := callService(t, map[string]object{callBad: call(toolPods, errJS, headerError, "true", headerFormat, "json")})

	got := query(t, s, map[string]string{"t": callBad}, "SELECT error FROM t")

	if want := "error\nforbidden\n(1 row)\nnote: this call returned a tool error\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAKeyWithAStarIsAllCallsOfOneToolAsOneTable(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1:  call(toolPods, podsA, headerFormat, "json"),
		callPods2:  call(toolPods, podsB, headerFormat, "json"),
		callEvents: call(toolEvents, eventsTable),
		callOther:  call(toolPods, podsB, headerFormat, "json"),
	})

	got := query(t, s, map[string]string{tblPods: callPrefix + "*-pods_list"}, countPodsSQL)

	if want := "n\n3\n(1 row)\n" + "note: table pods holds the output of pods_list, pods_list\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAStarAcrossAgentsWithinOneThread(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1:                              call(toolPods, podsA, headerFormat, "json"),
		"ns/crew/th1/k8s-config/001-pods_list": call(toolPods, podsB, headerFormat, "json"),
		callOther:                              call(toolPods, podsB, headerFormat, "json"),
	})

	got := query(t, s, map[string]string{tblPods: "ns/crew/th1/*/*-pods_list"}, countPodsSQL)

	if !strings.HasPrefix(got, "n\n3\n") {
		t.Fatalf("got %q", got)
	}
}

func TestAFailedCallIsLeftOutOfTheUnionAndNamed(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1: call(toolPods, podsA, headerFormat, "json"),
		callBad:   call(toolPods, errJS, headerError, "true", headerFormat, "json"),
	})

	got := query(t, s, map[string]string{tblPods: callPrefix + "*-pods_list"}, countPodsSQL)

	if !strings.HasPrefix(got, "n\n2\n(1 row)\n") ||
		!strings.Contains(got, "left out 1 object(s) that are failed calls or not stored tool calls: "+callBad) {
		t.Fatalf("got %q", got)
	}
}

func TestDifferentShapesBecomeSeparateTables(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1:  call(toolPods, podsA, headerFormat, "json"),
		callEvents: call(toolEvents, eventsTable),
	})

	got := query(t, s, map[string]string{"c": callPrefix + "*"},
		"SELECT (SELECT count(*) FROM c) AS pods, (SELECT count(*) FROM c_2) AS events")

	if !strings.HasPrefix(got, "pods | events\n2 | 2\n") {
		t.Fatalf("got %q", got)
	}
}

func TestACutCallKeepsItsCompleteItems(t *testing.T) {
	cut := `[{"v":1},{"v":2},{"v` + " ...[truncated]"
	s := callService(t, map[string]object{callPods1: call(toolPods, cut, headerTruncated, "true")})

	got := query(t, s, map[string]string{"t": callPods1}, "SELECT count(*) AS n FROM t")

	if !strings.HasPrefix(got, "n\n2\n(1 row)\n") || !strings.Contains(got, "was cut at the tool result size limit") {
		t.Fatalf("got %q", got)
	}
}

func TestAnObjectWithoutCallHeadersIsReadAsBefore(t *testing.T) {
	art := toolerArtifact([2]string{toolPods, podsListTable}, [2]string{toolPods, podsListMore})
	s := callService(t, map[string]object{"ns/crew/th1/agent/agree-u": {body: art, headers: map[string]string{}}})

	got := query(t, s, map[string]string{tblPods: "ns/crew/th1/agent/agree-u"}, countPodsSQL)

	if want := "n\n6\n(1 row)\n" + "note: table pods holds the output of pods_list, pods_list\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestKeyPatternsAreRefused(t *testing.T) {
	s := callService(t, map[string]object{callPods1: call(toolPods, podsA, headerFormat, "json")})
	cases := []struct{ name, key, want string }{
		{"no thread in the fixed part", "ns/*", "must start with the namespace, crew and thread"},
		{"star alone", "*", "must start with the namespace, crew and thread"},
		{"nothing matches", callPrefix + "*-nodes_list", "no object matches"},
		{"bad pattern", callPrefix + "*[", "bad key pattern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Query(context.Background(), map[string]string{"t": tc.key}, "SELECT 1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEveryMatchFailedIsAnError(t *testing.T) {
	s := callService(t, map[string]object{callBad: call(toolPods, errJS, headerError, "true")})

	_, err := s.Query(context.Background(), map[string]string{"t": callPrefix + "*"}, "SELECT 1")

	if err == nil || !strings.Contains(err.Error(), "no successful tool call matches") {
		t.Fatalf("err = %v", err)
	}
}

func TestTooManyMatchesAndTooMuchDataAreRefused(t *testing.T) {
	many := map[string]object{}
	for i := 0; i <= MaxGlobObjects; i++ {
		many[callPrefix+strings.Repeat("a", i+1)+"-t"] = call("t", "[]")
	}
	_, err := callService(t, many).Query(context.Background(), map[string]string{"t": callPrefix + "*"}, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "over the limit of") {
		t.Fatalf("err = %v", err)
	}

	big := strings.Repeat("x", 600)
	s := callService(t, map[string]object{callPods1: call("t", big), callPods2: call("t", big)})
	s.MaxArtifactBytes = 1000
	_, err = s.Query(context.Background(), map[string]string{"t": callPrefix + "*"}, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "together pass") {
		t.Fatalf("err = %v", err)
	}
}

func TestAStoreThatCannotListRefusesAStar(t *testing.T) {
	s := callService(t, nil)
	s.Store = missingStore{}

	_, err := s.Query(context.Background(), map[string]string{"t": callPrefix + "*"}, "SELECT 1")

	if err == nil || !strings.Contains(err.Error(), "cannot list objects") {
		t.Fatalf("err = %v", err)
	}
}

func TestAKeyWithAStarStaysInThePodsNamespace(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1:                        call(toolPods, podsA, headerFormat, "json"),
		"other/crew/th1/a/001-pods_list": call(toolPods, podsB, headerFormat, "json"),
	})
	s.Namespace = "ns"

	if got := query(t, s, map[string]string{tblPods: callPrefix + "*-pods_list"}, countPodsSQL); !strings.HasPrefix(got, "n\n2\n") {
		t.Fatalf("got %q", got)
	}
	_, err := s.Query(context.Background(), map[string]string{tblPods: "other/crew/th1/a/*"}, countPodsSQL)
	if err == nil || !strings.Contains(err.Error(), `may reach only the namespace "ns"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRowsHeader(t *testing.T) {
	meta := func(o object) store.Meta { return store.Meta{Headers: o.headers} }
	if n, ok := RowsHeader(meta(call("t", "", headerRows, "812"))); !ok || n != 812 {
		t.Fatalf("n = %d, ok = %v", n, ok)
	}
	for name, o := range map[string]object{
		"no header":       call("t", ""),
		"cut result":      call("t", "", headerRows, "5", headerTruncated, "true"),
		"not a number":    call("t", "", headerRows, "many"),
		"not a tool call": {headers: map[string]string{headerRows: "3"}},
	} {
		if _, ok := RowsHeader(meta(o)); ok {
			t.Errorf("%s: a count was returned", name)
		}
	}
}
