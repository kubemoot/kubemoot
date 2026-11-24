package workspace

import (
	"strings"
	"testing"
)

const (
	flagOff   = "false"
	nodesKey  = "nodes.json"
	colStatus = "status"
	colInvObj = "InvolvedObject"
)

const nodeJSON = `{"items":[{"metadata":{"name":"n1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},` +
	`{"metadata":{"name":"n2"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`

func nestedService(t *testing.T, on bool) *Service {
	s := sqliteService(t, map[string]string{nodesKey: nodeJSON, "plain.csv": "name,namespace\na,b\n"})
	s.Limits.NestedHints = on
	return s
}

func TestPathInside(t *testing.T) {
	cases := []struct {
		guess, column, path string
		ok                  bool
	}{
		{"status.conditions", colStatus, "conditions", true},
		{"nodes.status.conditions.type", colStatus, "conditions.type", true},
		{"involved_object_name", colInvObj, nameCol, true},
		{"e.involvedObject.name", colInvObj, nameCol, true},
		{"involvedObject.fieldPath", "InvolvedObject", "fieldPath", true},
		{colStatus, colStatus, "", false},
		{"namespace", nameCol, "", false},
		{"cpu_usage", colStatus, "", false},
		{"ab.c", "ab", "", false},
	}
	for _, c := range cases {
		path, ok := pathInside(c.guess, c.column)
		if ok != c.ok || path != c.path {
			t.Errorf("pathInside(%q, %q) = %q, %v; want %q, %v", c.guess, c.column, path, ok, c.path, c.ok)
		}
	}
}

func TestNestedHintNamesTheColumnAndTheForm(t *testing.T) {
	s := nestedService(t, true)
	_, err := s.Query(t.Context(), map[string]string{"n": nodesKey}, "SELECT status.conditions FROM n")
	if err == nil {
		t.Fatal("a dotted guess must fail")
	}
	for _, want := range []string{"the columns are: n (", `status of n holds a nested object`, `"status"->>'$.conditions'`, "json_each"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	out := query(t, s, map[string]string{"n": nodesKey}, `SELECT "status"->>'$.conditions[0].type' FROM n LIMIT 1`)
	if !strings.Contains(out, "Ready") {
		t.Errorf("the form the hint names must read the field: %q", out)
	}
}

func TestNestedHintIsOffByDefault(t *testing.T) {
	s := nestedService(t, false)
	_, err := s.Query(t.Context(), map[string]string{"n": nodesKey}, "SELECT status.conditions FROM n")
	if err == nil || strings.Contains(err.Error(), "holds a nested") {
		t.Fatalf("the hint is off: %v", err)
	}
}

func TestNestedHintSkipsColumnsThatAreNotJSON(t *testing.T) {
	s := nestedService(t, true)
	_, err := s.Query(t.Context(), map[string]string{"p": "plain.csv"}, "SELECT name_first FROM p")
	if err == nil || strings.Contains(err.Error(), "holds a nested") {
		t.Fatalf("a text column is not read as JSON: %v", err)
	}
	_, err = s.Query(t.Context(), map[string]string{"n": nodesKey}, "SELECT cpu_usage FROM n")
	if err == nil || strings.Contains(err.Error(), "holds a nested") {
		t.Fatalf("a guess that reaches into no column gets no hint: %v", err)
	}
	if got := withNestedHint(t.Context(), nil, nil, nil); got != nil {
		t.Errorf("no error stays no error: %v", got)
	}
}

func TestLimitsFromEnvReadsTheNestedHintSwitch(t *testing.T) {
	on, off := true, false
	for value, want := range map[string]bool{"": off, flagOff: off, flagSet: on, " TRUE ": on, "yes": off} {
		lim, err := limitsFromEnv(func(k string) string {
			if k == EnvNestedHints {
				return value
			}
			return ""
		})
		if err != nil || lim.NestedHints != want {
			t.Errorf("%q: got %v, %v; want %v", value, lim.NestedHints, err, want)
		}
	}
}
