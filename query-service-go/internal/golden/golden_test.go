package golden

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func resp(status int, body string) Response { return decodeResponse(status, []byte(body)) }

func queryBody(collection string, hits ...string) string {
	return fmt.Sprintf(`{"query":"q","results":[%s],"collection":%q,"embedding_time_ms":1,"search_time_ms":2}`,
		strings.Join(hits, ","), collection)
}

func hitJSON(content, meta string, score float64) string {
	return fmt.Sprintf(`{"content":%q,"metadata":%s,"score":%v}`, content, meta, score)
}

func TestReadQuerySet(t *testing.T) {
	set, err := ReadQuerySet(strings.NewReader(`# comment

{"id":"a","source":"docs","body":{"query":"x","top_k":3}}
{"id":"b","source":"docs","method":"GET","path":"/info"}
{"id":"c","source":"docs","path":"/health"}
`))
	if err != nil || len(set) != 3 {
		t.Fatalf("%v %v", set, err)
	}
	if set[0].Method != http.MethodPost || set[0].Path != queryPath || string(set[0].Body) != `{"query":"x","top_k":3}` {
		t.Errorf("defaults: %+v", set[0])
	}
	if set[1].Method != http.MethodGet || set[1].Path != infoPath || set[2].Method != http.MethodGet {
		t.Errorf("get lines: %+v %+v", set[1], set[2])
	}
}

func TestReadQuerySetRejectsBadLines(t *testing.T) {
	for _, line := range []string{`{`, `{"source":"s"}`, `{"id":"x"}`, `{"id":"x","source":"s","path":"query"}`, `{"id":"x","source":"s","bogus":1}`} {
		if _, err := ReadQuerySet(strings.NewReader(line)); err == nil {
			t.Errorf("accepted %s", line)
		}
	}
}

func TestDiffMatchesIdenticalAnswersAndIgnoresTimings(t *testing.T) {
	q := Query{Path: queryPath}
	a := resp(200, queryBody("c", hitJSON("one", `{"k":"v"}`, 0.9), hitJSON("two", `{}`, 0.5)))
	b := resp(200, strings.ReplaceAll(queryBody("c", hitJSON("one", `{"k":"v"}`, 0.9), hitJSON("two", `{}`, 0.5)), `"search_time_ms":2`, `"search_time_ms":99`))
	if d := Diff(q, a, b, DefaultTolerance); len(d) != 0 {
		t.Fatalf("%v", d)
	}
}

func TestDiffScoresWithinToleranceMatch(t *testing.T) {
	q := Query{Path: queryPath}
	a := resp(200, queryBody("c", hitJSON("one", `{}`, 0.9)))
	near := resp(200, queryBody("c", hitJSON("one", `{}`, 0.9000004)))
	far := resp(200, queryBody("c", hitJSON("one", `{}`, 0.9001)))
	if d := Diff(q, a, near, DefaultTolerance); len(d) != 0 {
		t.Errorf("within 1e-6: %v", d)
	}
	if d := Diff(q, a, far, DefaultTolerance); len(d) != 1 || !strings.Contains(d[0], "score at rank 1") {
		t.Errorf("beyond 1e-6: %v", d)
	}
}

func TestDiffOrderMattersUnlessRowsTie(t *testing.T) {
	q := Query{Path: queryPath}
	a := resp(200, queryBody("c", hitJSON("x", `{}`, 0.9), hitJSON("y", `{}`, 0.5)))
	swapped := resp(200, queryBody("c", hitJSON("y", `{}`, 0.9), hitJSON("x", `{}`, 0.5)))
	if d := Diff(q, a, swapped, DefaultTolerance); len(d) == 0 {
		t.Error("a different order at different scores must differ")
	}
	tied := resp(200, queryBody("c", hitJSON("x", `{}`, 0.7), hitJSON("y", `{}`, 0.7), hitJSON("z", `{}`, 0.1)))
	tiedSwapped := resp(200, queryBody("c", hitJSON("y", `{}`, 0.7), hitJSON("x", `{}`, 0.7), hitJSON("z", `{}`, 0.1)))
	if d := Diff(q, tied, tiedSwapped, DefaultTolerance); len(d) != 0 {
		t.Errorf("tied rows are compared as a set: %v", d)
	}
	tiedOther := resp(200, queryBody("c", hitJSON("y", `{}`, 0.7), hitJSON("w", `{}`, 0.7), hitJSON("z", `{}`, 0.1)))
	if d := Diff(q, tied, tiedOther, DefaultTolerance); len(d) != 1 || !strings.Contains(d[0], "ranks 1-2") {
		t.Errorf("a different member of a tie: %v", d)
	}
}

func TestDiffComparesMetadataAndCollectionAndCount(t *testing.T) {
	q := Query{Path: queryPath}
	base := resp(200, queryBody("c", hitJSON("x", `{"a":1,"b":2}`, 0.9)))
	reordered := resp(200, queryBody("c", hitJSON("x", `{"b":2,"a":1}`, 0.9)))
	if d := Diff(q, base, reordered, DefaultTolerance); len(d) != 0 {
		t.Errorf("key order is not content: %v", d)
	}
	for name, other := range map[string]Response{
		"metadata":   resp(200, queryBody("c", hitJSON("x", `{"a":1,"b":3}`, 0.9))),
		"collection": resp(200, queryBody("d", hitJSON("x", `{"a":1,"b":2}`, 0.9))),
		"count":      resp(200, queryBody("c")),
	} {
		if d := Diff(q, base, other, DefaultTolerance); len(d) == 0 {
			t.Errorf("%s difference not found", name)
		}
	}
}

func TestDiffStatusesAndErrorBodies(t *testing.T) {
	q := Query{Path: queryPath}
	if d := Diff(q, resp(200, queryBody("c")), resp(404, `{"detail":"x"}`), 0); len(d) != 1 || d[0] != "status: 200 vs 404" {
		t.Errorf("%v", d)
	}
	if d := Diff(q, resp(404, `{"detail":"x"}`), resp(404, `{"detail":"y"}`), 0); len(d) != 1 {
		t.Errorf("404 details are compared: %v", d)
	}
	if d := Diff(q, resp(500, `{"detail":"psycopg2 says"}`), resp(500, `{"detail":"pgx says"}`), 0); len(d) != 0 {
		t.Errorf("a 500 is compared by status only: %v", d)
	}
	py := resp(422, `{"detail":[{"type":"missing","loc":["body","query"],"msg":"Field required","input":{}}]}`)
	goV := resp(422, `{"detail":[{"type":"missing","loc":["body","query"],"msg":"Field required.","input":{"a":1}}]}`)
	if d := Diff(q, py, goV, 0); len(d) != 0 {
		t.Errorf("422 compares type and loc: %v", d)
	}
	other := resp(422, `{"detail":[{"type":"string_type","loc":["body","query"]}]}`)
	if d := Diff(q, py, other, 0); len(d) != 1 {
		t.Errorf("a different 422 type: %v", d)
	}
}

func TestDiffInfoComparesTheWholeBody(t *testing.T) {
	q := Query{Path: infoPath}
	a := resp(200, `{"stats":{"document_count":10},"x":1}`)
	if d := Diff(q, a, resp(200, `{"x":1,"stats":{"document_count":10}}`), 0); len(d) != 0 {
		t.Errorf("%v", d)
	}
	if d := Diff(q, a, resp(200, `{"stats":{"document_count":11},"x":1}`), 0); len(d) != 1 {
		t.Errorf("%v", d)
	}
}

func TestDiffReportsUnreadableResults(t *testing.T) {
	q := Query{Path: queryPath}
	if d := Diff(q, resp(200, `{"results":"x"}`), resp(200, `{"results":"x"}`), 0); len(d) == 0 {
		t.Error("an answer without a results list is a failure, not a match")
	}
}

// answerer is a stand-in query service answering every request with a fixed reply.
func answerer(t *testing.T, body string, paths *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunnerSendsEachQueryToBothBasesWithTheSourceFilledIn(t *testing.T) {
	var pa, pb []string
	body := queryBody("c", hitJSON("x", `{}`, 0.9))
	a, b := answerer(t, body, &pa), answerer(t, body, &pb)
	set := []Query{
		{ID: "1", Source: "docs", Method: http.MethodPost, Path: queryPath, Body: []byte(`{"query":"q"}`)},
		{ID: "2", Source: "resumes", Method: http.MethodPost, Path: queryPath, Body: []byte(`{"query":"q"}`)},
	}
	rep := (&Runner{A: a.URL + "/{source}-py", B: b.URL + "/sources/{source}"}).Run(context.Background(), set)
	if rep.Total != 2 || len(rep.Mismatches) != 0 {
		t.Fatalf("%+v", rep)
	}
	if strings.Join(pa, ",") != "POST /docs-py/query,POST /resumes-py/query" ||
		strings.Join(pb, ",") != "POST /sources/docs/query,POST /sources/resumes/query" {
		t.Errorf("a: %v b: %v", pa, pb)
	}
}

func TestRunnerReportsMismatchesAndUnreachableServices(t *testing.T) {
	var p []string
	a := answerer(t, queryBody("c", hitJSON("x", `{}`, 0.9)), &p)
	b := answerer(t, queryBody("c", hitJSON("other", `{}`, 0.9)), &p)
	set := []Query{{ID: "diff", Source: "s", Method: http.MethodPost, Path: queryPath, Body: []byte(`{"query":"q"}`)}}
	rep := (&Runner{A: a.URL, B: b.URL}).Run(context.Background(), set)
	if len(rep.Mismatches) != 1 || rep.Mismatches[0].ID != "diff" {
		t.Fatalf("%+v", rep)
	}
	b.Close()
	rep = (&Runner{A: a.URL, B: b.URL}).Run(context.Background(), set)
	if len(rep.Mismatches) != 1 || !strings.Contains(rep.Mismatches[0].Diffs[0], "request failed") {
		t.Fatalf("%+v", rep)
	}
}

const (
	queryPath = "/query"
	infoPath  = "/info"
)

func TestDiffTreatsJSONSyntaxErrorOffsetsAsParserDetail(t *testing.T) {
	q := Query{Path: queryPath}
	py := resp(422, `{"detail":[{"type":"json_invalid","loc":["body",14],"msg":"JSON decode error"}]}`)
	goV := resp(422, `{"detail":[{"type":"json_invalid","loc":["body",0],"msg":"JSON decode error"}]}`)
	if d := Diff(q, py, goV, 0); len(d) != 0 {
		t.Errorf("%v", d)
	}
}

func TestTheExampleQuerySetParses(t *testing.T) {
	f, err := os.Open("../../testdata/queries.example.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	set, err := ReadQuerySet(f)
	if err != nil || len(set) != 8 || set[6].Method != http.MethodGet || set[6].Path != infoPath {
		t.Fatalf("%d queries, %v", len(set), err)
	}
}

func TestANullBodyMeansNoBody(t *testing.T) {
	set, err := ReadQuerySet(strings.NewReader(`{"id":"a","source":"s","path":"/info","body":null}`))
	if err != nil || len(set[0].Body) != 0 || set[0].Method != http.MethodGet {
		t.Fatalf("%+v %v", set, err)
	}
}
