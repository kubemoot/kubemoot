package workspace

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func describe(t *testing.T, s *Service, artifacts map[string]string) string {
	t.Helper()
	out, err := s.Describe(context.Background(), artifacts)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	return out
}

func TestDescribeJSONListGivesColumnsTypesNullsAndExamples(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{podsKey: `[{"ns":"a","pods":3,"ratio":0.5,"tags":["x"]},
		{"ns":"b","pods":5,"ratio":1.5,"tags":["y"]},{"ns":"c","pods":null,"ratio":2.5,"tags":["z"]}]`})

	got := describe(t, s, map[string]string{"p": podsKey})

	for _, want := range []string{
		"[TABLE p rows=3 columns=4]\n",
		`- ns: text; examples: "a", "b", "c"`,
		`- pods: integer; 1 null; examples: "3", "5"`,
		`- ratio: real; examples: "0.5", "1.5", "2.5"`,
		`- tags: text (JSON); examples: "[\"x\"]"`,
		"note: the types are the query engine's",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestDescribeGivesEveryEngineTheSameColumns(t *testing.T) {
	eachEngine(t, map[string]string{podsKey: `[{"ns":"a","pods":3},{"ns":"b","pods":5}]`},
		func(t *testing.T, s *Service, _ dialect) {
			got := describe(t, s, map[string]string{"p": podsKey})
			if !strings.Contains(got, "[TABLE p rows=2 columns=2]") || !strings.Contains(got, "- ns: ") ||
				!strings.Contains(got, "- pods: ") || !strings.Contains(got, `examples: "3", "5"`) {
				t.Fatalf("got\n%s", got)
			}
		})
}

func TestDescribeAKubectlTableAndMixedTypes(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{"t": "NAME   RESTARTS\nfoo    1\nbar    two\nbaz    3\n"})

	got := describe(t, s, map[string]string{"t": "t"})

	if !strings.Contains(got, "[TABLE t rows=3 columns=2]") || !strings.Contains(got, "- name: text;") {
		t.Fatalf("got\n%s", got)
	}
	if !strings.Contains(got, "- restarts: text;") {
		t.Fatalf("a column with a word in it is text:\n%s", got)
	}
}

func TestDescribeMeaningIsGivenOnlyForColumnsKubemootAdds(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1: call(toolPods, `[{"v":1,"line":"x"}]`, HeaderContentType, "application/json", headerArgs, `{"vmid":1}`),
		callPods2: call(toolPods, `[{"v":2,"line":"y"}]`, HeaderContentType, "application/json", headerArgs, `{"vmid":2}`),
	})

	got := describe(t, s, map[string]string{"c": callPrefix + "*-pods_list"})

	for _, want := range []string{
		"- _call: text; meaning: the key of the stored call the row came from;",
		"- _args: text (JSON); meaning: the arguments of that call, as JSON text;",
		"- _arg_vmid: ", "meaning: the value of the argument vmid in that call",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "- v: integer; meaning") || strings.Contains(got, "- line: text; meaning") {
		t.Errorf("a column of the data has no recorded meaning:\n%s", got)
	}
}

func TestDescribeAHeaderOnlyTableHasNoRows(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{"head": "a,b\n"})

	got := describe(t, s, map[string]string{"h": "head"})

	if !strings.Contains(got, "[TABLE h rows=0 columns=2]") || !strings.Contains(got, "- a: null") {
		t.Fatalf("got\n%s", got)
	}
}

func TestDescribeAnArtifactThatHasNoColumnsSaysSoLikeAQuery(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{"blank": `[]`})

	if _, err := s.Describe(context.Background(), map[string]string{"e": "blank"}); err == nil ||
		!strings.Contains(err.Error(), "no columns") {
		t.Fatalf("got %v", err)
	}
}

func TestDescribeCutsLongExamplesAndKeepsALineEach(t *testing.T) {
	long := strings.Repeat("x", 100)
	s := newService(t, engineSQLite, map[string]string{"t": fmt.Sprintf(`[{"a":"%s\nsecond line"}]`, long)})

	got := describe(t, s, map[string]string{"t": "t"})

	if !strings.Contains(got, strings.Repeat("x", maxExampleChars)+`..."`) || strings.Contains(got, "second line") {
		t.Fatalf("got\n%s", got)
	}
}

func TestDescribeOnlyTheFirstColumnsOfAWideTable(t *testing.T) {
	var cols []string
	for i := 0; i < maxDescribedColumns+7; i++ {
		cols = append(cols, fmt.Sprintf(`"c%03d":%d`, i, i))
	}
	s := newService(t, engineSQLite, map[string]string{"w": "[{" + strings.Join(cols, ",") + "}]"})

	got := describe(t, s, map[string]string{"w": "w"})

	if !strings.Contains(got, fmt.Sprintf("columns=%d]", maxDescribedColumns+7)) ||
		!strings.Contains(got, "(7 more columns are not described)") || strings.Contains(got, "- c206:") {
		t.Fatalf("got\n%s", got[:200])
	}
}

func TestDescribeNamesWithQuotesAreQuoted(t *testing.T) {
	nasty := `a"; DROP TABLE x;--`
	body := `[{"it's \"odd\"":1,"` + strings.ReplaceAll(nasty, `"`, `\"`) + `":"v"}]`
	eachEngine(t, map[string]string{"q": body}, func(t *testing.T, s *Service, _ dialect) {
		got := describe(t, s, map[string]string{"q": "q"})

		if !strings.Contains(got, `- it's "odd": `) || !strings.Contains(got, "- "+nasty+": ") ||
			!strings.Contains(got, "[TABLE q rows=1 columns=2]") {
			t.Fatalf("got\n%s", got)
		}
	})
}

func TestDescribeMarksNestedJSONForEveryEnginesTextType(t *testing.T) {
	if !isTextType(textType) || !isTextType("varchar") || isTextType("integer") || isTextType("struct(a integer)") {
		t.Fatal("isTextType")
	}
	eachEngine(t, map[string]string{"j": `[{"tags":"[\"x\"]"}]`}, func(t *testing.T, s *Service, _ dialect) {
		if got := describe(t, s, map[string]string{"j": "j"}); !strings.Contains(got, "- tags: text (JSON);") {
			t.Fatalf("got\n%s", got)
		}
	})
}

func TestDescribeRefusesBadRequests(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{"t": "[]"})
	cases := map[string]map[string]string{
		"no artifacts":       {},
		"a bad table name":   {"1x": "t"},
		"names that collide": {"a": "t", "A": "t"},
	}
	for name, artifacts := range cases {
		if _, err := s.Describe(context.Background(), artifacts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := s.Describe(context.Background(), map[string]string{"x": "no/such/key"}); err == nil ||
		!strings.Contains(err.Error(), `artifact "x"`) {
		t.Errorf("a missing artifact names the table: %v", err)
	}
	big := map[string]string{}
	for i := 0; i <= MaxArtifacts; i++ {
		big[fmt.Sprintf("t%d", i)] = "t"
	}
	if _, err := s.Describe(context.Background(), big); err == nil {
		t.Error("more than the most artifacts: no error")
	}
}

func TestDescribeCarriesTheNotesOfHowArtifactsWereRead(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1:  call(toolPods, podsA, HeaderContentType, "application/json"),
		callEvents: call(toolEvents, eventsTable),
	})

	got := describe(t, s, map[string]string{"c": callPrefix + "*"})

	if !strings.Contains(got, "note: table c holds the output of pods_list") {
		t.Fatalf("got\n%s", got)
	}
}

// cannedEngine answers the two queries Describe makes with fixed results.
type cannedEngine struct{ sample, typed Result }

func (c cannedEngine) Query(_ context.Context, _ []Relation, sql string, _ Limits) (Result, error) {
	if strings.Contains(sql, "typeof(") {
		return c.typed, nil
	}
	return c.sample, nil
}

func cannedService(t *testing.T, e cannedEngine) *Service {
	t.Helper()
	s := newService(t, engineSQLite, map[string]string{"t": `[{"a":7}]`})
	s.Engine = e
	return s
}

const colHeader = "col"

func TestDescribeCountsRowsFromTheFirstColumnByName(t *testing.T) {
	s := cannedService(t, cannedEngine{
		sample: Result{Columns: []string{"a", "b"}, Rows: [][]string{{"1", "x"}}},
		// b's rows come first and are fewer: the row count must still be a's.
		typed: Result{Columns: []string{colHeader, "ty", "n"}, Rows: [][]string{
			{"b", textType, "1"}, {"a", "integer", "3"}, {"a", nullType, "2"}, {"b", nullType, "4"}}},
	})

	got := describe(t, s, map[string]string{"t": "t"})

	if !strings.Contains(got, "[TABLE t rows=5 columns=2]") {
		t.Fatalf("got\n%s", got)
	}
}

func TestDescribeSaysWhenTheEngineReportedMoreTypesThanItAskedFor(t *testing.T) {
	var rows [][]string
	for i := 0; i < 10; i++ {
		rows = append(rows, []string{"a", fmt.Sprintf("type%d", i), "1"})
	}
	s := cannedService(t, cannedEngine{
		sample: Result{Columns: []string{"a"}, Rows: [][]string{{"1"}}},
		typed:  Result{Columns: []string{colHeader, "ty", "n"}, Rows: rows[:maxTypesPerColumn], Truncated: true},
	})

	got := describe(t, s, map[string]string{"t": "t"})

	if !strings.Contains(got, "(some columns have more types than are shown)") {
		t.Fatalf("got\n%s", got)
	}
}

func TestDescribeDescribesAColumnNameOnce(t *testing.T) {
	s := cannedService(t, cannedEngine{
		sample: Result{Columns: []string{"a", "b", "a"}, Rows: [][]string{{"1", "x", "9"}}},
		typed: Result{Columns: []string{colHeader, "ty", "n"}, Rows: [][]string{
			{"a", "integer", "1"}, {"b", textType, "1"}}},
	})

	got := describe(t, s, map[string]string{"t": "t"})

	if strings.Count(got, "\n- a: ") != 1 || !strings.Contains(got, "[TABLE t rows=1 columns=2]") ||
		!strings.Contains(got, "(1 columns repeat the name of an earlier column") || !strings.Contains(got, `examples: "1"`) {
		t.Fatalf("got\n%s", got)
	}
}
