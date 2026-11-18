package workspace

import (
	"strings"
	"testing"
)

const (
	toolPods   = "pods_list"
	toolEvents = "events_list"
	toolGet    = "resources_get"
	oneRowTwo  = "n\n2\n(1 row)\n"
)

// The tooler's contribution is written by the agent runtime as
// "[tool]\n" + result + "\n\n" for each call, then stripped; a result over the cap is
// cut and gets " ...[truncated]". These helpers write the same bytes.
func toolerArtifact(calls ...[2]string) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString("[" + c[0] + "]\n" + c[1] + "\n\n")
	}
	return strings.TrimSpace(b.String())
}

func runtimeTruncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + truncatedMarker
}

// podsListTable is the table kubernetes-mcp-server's pods_list returns.
const podsListTable = `NAMESPACE     APIVERSION   KIND   NAME            READY   STATUS      RESTARTS   AGE   IP            NODE      NOMINATED NODE   READINESS GATES   LABELS
default       v1           Pod    web-1           1/1     Running     0          5d    10.244.0.11   node-a    <none>           <none>            app=web
default       v1           Pod    web-2           0/1     Pending     0          2m                  node-b    <none>           <none>            app=web
kube-system   v1           Pod    coredns-abc     1/1     Running     3          40d   10.244.0.2    node-a    <none>           <none>            k8s-app=kube-dns
kube-system   v1           Pod    coredns-def     1/1     Running     1          40d   10.244.1.3    node-b    <none>           <none>            k8s-app=kube-dns
monitoring    v1           Pod    prometheus-0    2/2     Running     0          9d    10.244.1.9    node-b    <none>           <none>            app=prometheus`

const podsListMore = `NAMESPACE   APIVERSION   KIND   NAME      READY   STATUS    RESTARTS   AGE   IP            NODE     NOMINATED NODE   READINESS GATES   LABELS
default     v1           Pod    web-3     1/1     Running   0          1m    10.244.0.12   node-a   <none>           <none>            app=web`

const eventsTable = `NAMESPACE   LAST SEEN   TYPE      REASON      OBJECT        MESSAGE
default     3m          Warning   BackOff     pod/web-2     Back-off restarting failed container
kube-system 40m         Normal    Scheduled   pod/coredns   Successfully assigned`

const podsJSON = `{"apiVersion":"v1","kind":"PodList","items":[
 {"metadata":{"name":"web-1","namespace":"default"},"status":{"phase":"Running"}},
 {"metadata":{"name":"web-2","namespace":"default"},"status":{"phase":"Pending"}},
 {"metadata":{"name":"dns","namespace":"kube-system"},"status":{"phase":"Running"}}]}`

const countPodsSQL = "SELECT count(*) AS n FROM pods"

const nsCountSQL = "SELECT namespace, COUNT(*) AS n FROM pods GROUP BY namespace ORDER BY namespace"

func TestSplitSections(t *testing.T) {
	cases := []struct {
		name      string
		data      string
		wantTools []string
		wantOK    bool
	}{
		{"one section", toolerArtifact([2]string{toolPods, podsListTable}), []string{toolPods}, true},
		{"two calls of one tool", toolerArtifact([2]string{toolPods, podsListTable}, [2]string{toolPods, podsListMore}),
			[]string{toolPods, toolPods}, true},
		{"two tools", toolerArtifact([2]string{toolPods, podsListTable}, [2]string{toolEvents, eventsTable}),
			[]string{toolPods, toolEvents}, true},
		{"hyphen and dot in the name", "[get-pods.v2]\nNAME   AGE\nx      1d", []string{"get-pods.v2"}, true},
		{"plain table has no header", podsListTable, nil, false},
		{"prose that starts with a bracket", "[note] the cluster is healthy\nnothing to report", nil, false},
		{"bracketed word with trailing text", "[pods_list] NAME AGE\nx 1d", nil, false},
		{"json list of one string", `["pods_list"]`, nil, false},
		{"header after prose", "intro\n\n[pods_list]\nNAME   AGE\nx   1d", nil, false},
		{"empty", "", nil, false},
		{"header not after a blank line is body", "[a]\nrow\n[b]\nrow", []string{"a"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secs, ok := splitSections([]byte(tc.data))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			var tools []string
			for _, s := range secs {
				tools = append(tools, s.tool)
			}
			if strings.Join(tools, ",") != strings.Join(tc.wantTools, ",") {
				t.Fatalf("tools = %v, want %v", tools, tc.wantTools)
			}
		})
	}
}

func TestSplitSectionsBodiesAndCut(t *testing.T) {
	full := toolerArtifact([2]string{toolPods, podsListTable}, [2]string{"note", "all good"})
	secs, _ := splitSections([]byte(full))
	if secs[0].body != podsListTable || secs[1].body != "all good" || secs[0].cut {
		t.Fatalf("sections = %+v", secs)
	}
	row := "kube-system   v1           Pod    coredns-abc     1/1"
	inRow := toolerArtifact([2]string{toolPods, runtimeTruncate(podsListTable, strings.Index(podsListTable, row)+len(row))})
	secs, _ = splitSections([]byte(inRow))
	if !secs[0].cut || !secs[0].partial || strings.HasSuffix(secs[0].body, truncatedMarker) {
		t.Fatalf("cut inside a row: %+v", secs[0])
	}
	atBreak := toolerArtifact([2]string{toolPods, runtimeTruncate(podsListTable, strings.Index(podsListTable, "kube-system"))})
	secs, _ = splitSections([]byte(atBreak))
	if !secs[0].cut || secs[0].partial {
		t.Fatalf("cut at a line break: %+v", secs[0])
	}
}

func TestSalvageJSON(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"list cut mid element", `[{"a":1},{"a":2},{"a":`, `[{"a":1},{"a":2}]`},
		{"items cut mid element", `{"kind":"List","items":[{"x":1},{"x":2},{"x"`, `[{"x":1},{"x":2}]`},
		{"list cut at an element", `[{"a":1},`, `[{"a":1}]`},
		{"nothing complete", `[{"a":`, ""},
		{"object without items", `{"a":1,"b":`, ""},
		{"not json", `hello`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := salvageJSON(tc.in)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("salvageJSON = %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}

type sectionCase struct {
	name     string
	artifact string
	sql      string
	want     string
}

func TestQuerySections(t *testing.T) {
	cutInRow := runtimeTruncate(podsListTable, strings.Index(podsListTable, "monitoring")+20)
	cutJSON := runtimeTruncate(podsJSON, strings.Index(podsJSON, `"dns"`)+8)
	cases := []sectionCase{
		{"kubectl table behind a header", toolerArtifact([2]string{toolPods, podsListTable}), nsCountSQL,
			"namespace | n\ndefault | 2\nkube-system | 2\nmonitoring | 1\n(3 rows)\n"},
		{"plain table with no header is unchanged", podsListTable, nsCountSQL,
			"namespace | n\ndefault | 2\nkube-system | 2\nmonitoring | 1\n(3 rows)\n"},
		{"two calls of one tool are one table",
			toolerArtifact([2]string{toolPods, podsListTable}, [2]string{toolPods, podsListMore}), nsCountSQL,
			"namespace | n\ndefault | 3\nkube-system | 2\nmonitoring | 1\n(3 rows)\n" +
				"note: table pods holds the output of pods_list, pods_list\n"},
		{"two tools: the second shape is its own table",
			toolerArtifact([2]string{toolPods, podsListTable}, [2]string{toolEvents, eventsTable}),
			"SELECT (SELECT count(*) FROM pods) AS p, (SELECT count(*) FROM pods_2 WHERE TYPE = 'Warning') AS w",
			"p | w\n5 | 1\n(1 row)\n" + "note: table pods holds the output of pods_list\n" +
				"note: table pods_2 holds the output of events_list\n"},
		{"a section cut inside a row loses that row",
			toolerArtifact([2]string{toolPods, cutInRow}), nsCountSQL,
			"namespace | n\ndefault | 2\nkube-system | 2\n(2 rows)\n" +
				"note: the output of pods_list was cut at the tool result size limit; table pods may be missing rows\n"},
		{"a JSON section cut mid element keeps the complete items",
			toolerArtifact([2]string{toolGet, cutJSON}),
			countPodsSQL,
			oneRowTwo +
				"note: the output of resources_get was cut at the tool result size limit; table pods may be missing rows\n"},
		{"text sections beside a table number their lines across their table",
			toolerArtifact([2]string{toolPods, podsListTable}, [2]string{"describe", "first\nsecond"}, [2]string{"describe", "third"}),
			"SELECT line_no, line FROM pods_2 ORDER BY line_no",
			"line_no | line\n1 | first\n2 | second\n3 | third\n(3 rows)\n" +
				"note: table pods holds the output of pods_list\nnote: table pods_2 holds the output of describe, describe\n"},
		{"sections that are all prose are read as one text, headings kept",
			"[server]\nport=1\n\n[db]\nname=x", countPodsSQL, "n\n5\n(1 row)\n"},
		{"prose that merely starts with a bracket is text", "[note] healthy\nall fine",
			countPodsSQL, oneRowTwo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runSectionCase(t, tc) })
	}
}

func runSectionCase(t *testing.T, tc sectionCase) {
	eachEngine(t, map[string]string{"k": tc.artifact}, func(t *testing.T, s *Service, d dialect) {
		got := query(t, s, map[string]string{tblPods: "k"}, tc.sql)
		if got != tc.want {
			t.Fatalf("got %q\nwant %q", got, tc.want)
		}
	})
}

func TestQuerySectionsJSON(t *testing.T) {
	artifact := toolerArtifact([2]string{toolGet, podsJSON}, [2]string{toolGet, podsJSON})
	eachEngine(t, map[string]string{"k": artifact}, func(t *testing.T, s *Service, d dialect) {
		sql := "SELECT " + d.phase + " AS phase, count(*) AS n FROM pods GROUP BY phase ORDER BY phase"
		got := query(t, s, map[string]string{tblPods: "k"}, sql)
		if want := "phase | n\nPending | 2\nRunning | 4\n(2 rows)\n" +
			"note: table pods holds the output of resources_get, resources_get\n"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// The failing query from the compute agent: a count by namespace over pods_list.
func TestQuerySectionsReproducesTheComputeAgentQuery(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{"k": toolerArtifact([2]string{toolPods, podsListTable})})
	got := query(t, s, map[string]string{tblPods: "k"}, "SELECT namespace, COUNT(*) FROM pods GROUP BY namespace")
	if !strings.Contains(got, "default | 2\n") || !strings.Contains(got, "kube-system | 2\n") ||
		!strings.Contains(got, "monitoring | 1\n") || !strings.Contains(got, "(3 rows)") {
		t.Fatalf("got %q", got)
	}
}

func TestQuerySectionsAvoidsATableNameTheCallerUses(t *testing.T) {
	art := toolerArtifact([2]string{toolPods, podsListTable}, [2]string{toolEvents, eventsTable})
	s := newService(t, engineSQLite, map[string]string{"k": art, "o": podsListTable})
	got := query(t, s, map[string]string{tblPods: "k", "pods_2": "o"},
		"SELECT (SELECT count(*) FROM pods_3) AS events, (SELECT count(*) FROM pods_2) AS other")
	if !strings.HasPrefix(got, "events | other\n2 | 5\n") || !strings.Contains(got, "table pods_3 holds the output of events_list") {
		t.Fatalf("got %q", got)
	}
}

func TestQuerySectionsAllEmptyReadsAsText(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{"k": "[pods_list]\n\n\n[events_list]\n"})
	got := query(t, s, map[string]string{tblPods: "k"}, countPodsSQL)
	if !strings.HasPrefix(got, "n\n") {
		t.Fatalf("got %q", got)
	}
}

func TestQuerySectionsKeepsKindsApart(t *testing.T) {
	pods := `{"items":[{"kind":"Pod","metadata":{"name":"a"}},{"kind":"Pod","metadata":{"name":"b"}}]}`
	nodes := `{"items":[{"kind":"Node","metadata":{"name":"n1"}}]}`
	art := toolerArtifact([2]string{toolGet, pods}, [2]string{toolGet, nodes})
	s := newService(t, engineSQLite, map[string]string{"k": art})
	got := query(t, s, map[string]string{tblPods: "k"}, "SELECT (SELECT count(*) FROM pods) AS p, (SELECT count(*) FROM pods_2) AS n")
	if !strings.HasPrefix(got, "p | n\n2 | 1\n") {
		t.Fatalf("got %q", got)
	}
}

func TestQuerySectionsCRLF(t *testing.T) {
	art := strings.ReplaceAll(toolerArtifact([2]string{toolPods, podsListTable}), "\n", "\r\n")
	s := newService(t, engineSQLite, map[string]string{"k": art})
	got := query(t, s, map[string]string{tblPods: "k"}, countPodsSQL)
	if !strings.HasPrefix(got, "n\n5\n") {
		t.Fatalf("got %q", got)
	}
}
