package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const engineSQLite = "sqlite"

// dialect holds what differs between the engines' SQL.
type dialect struct {
	engine string
	// phase and objName read a nested status.phase and metadata.name.
	phase, objName string
	// slow is a query that never finishes.
	slow string
	// writes are statements, beyond the shared list, the engine must refuse.
	writes []string
}

var dialects = []dialect{
	{
		engine: engineSQLite, phase: "status->>'phase'", objName: "metadata->>'name'",
		slow: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c",
		writes: []string{
			"VACUUM INTO '/tmp/kmwq-out.db'", "UPDATE t SET a = 2", "DELETE FROM t", "REPLACE INTO t VALUES (1)",
			"PRAGMA writable_schema = 1", "PRAGMA query_only = OFF", "DETACH DATABASE main", "BEGIN", "ANALYZE",
		},
	},
	{
		engine: engineDuckDB, phase: "status.phase", objName: "metadata.name",
		slow: "SELECT count(*) FROM range(100000000000) a, range(1000) b",
		writes: []string{
			"COPY (SELECT 1) TO '/tmp/out.csv'", "INSTALL httpfs", "SET enable_external_access=true",
		},
	},
}

type missingStore struct{}

func (missingStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("no such object %q", key)
}

// newService builds a Service over a temp artifact directory holding the given
// artifacts (object key to content), backed by the named engine. DuckDB tests skip
// when the program is absent.
func newService(t *testing.T, engine string, files map[string]string) *Service {
	t.Helper()
	var eng Engine = SQLite{}
	if engine == engineDuckDB {
		eng = DuckDB{Bin: duckBin(t)}
	}
	dir := t.TempDir()
	for key, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{
		Dir: dir, Engine: eng, Store: missingStore{}, MaxArtifactBytes: 1 << 20,
		Limits: Limits{Timeout: 10 * time.Second, MaxRows: 100, MemoryLimit: mem256},
	}
}

func query(t *testing.T, s *Service, artifacts map[string]string, sql string) string {
	t.Helper()
	out, err := s.Query(context.Background(), artifacts, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return out
}

// eachEngine runs fn once per engine that is available.
func eachEngine(t *testing.T, files map[string]string, fn func(t *testing.T, s *Service, d dialect)) {
	t.Helper()
	for _, d := range dialects {
		t.Run(d.engine, func(t *testing.T) { fn(t, newService(t, d.engine, files), d) })
	}
}

func TestQueryJSONList(t *testing.T) {
	eachEngine(t, map[string]string{podsKey: `[{"ns":"a","pods":3},{"ns":"b","pods":5},{"ns":"c","pods":0}]`},
		func(t *testing.T, s *Service, _ dialect) {
			got := query(t, s, map[string]string{"p": podsKey}, "SELECT ns, pods FROM p WHERE pods > 0 ORDER BY pods DESC;")
			if want := "ns | pods\nb | 5\na | 3\n(2 rows)\n"; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
			got = query(t, s, map[string]string{"p": podsKey}, "SELECT count(*) AS n, sum(pods) AS total FROM p")
			if want := "n | total\n3 | 8\n(1 row)\n"; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
}

func TestQueryKubectlJSONItems(t *testing.T) {
	eachEngine(t, map[string]string{"k": `{"apiVersion":"v1","kind":"List","items":[
		{"metadata":{"name":"a"},"status":{"phase":"Running"}},
		{"metadata":{"name":"b"},"status":{"phase":"Running"}},
		{"metadata":{"name":"c"},"status":{"phase":"Pending"}}]}`},
		func(t *testing.T, s *Service, d dialect) {
			got := query(t, s, map[string]string{tblPods: "k"},
				"SELECT "+d.phase+" AS phase, count(*) AS n FROM pods GROUP BY phase ORDER BY phase")
			if want := "phase | n\nPending | 1\nRunning | 2\n(2 rows)\n"; got != want {
				t.Fatalf("got %q", got)
			}
		})
}

func TestQueryKubectlTable(t *testing.T) {
	eachEngine(t, map[string]string{"t": kubectlPods}, func(t *testing.T, s *Service, _ dialect) {
		got := query(t, s, map[string]string{tblPods: "t"}, `SELECT "STATUS", count(*) AS n FROM pods GROUP BY 1 ORDER BY 1`)
		if want := "STATUS | n\nPending | 1\nRunning | 2\n(2 rows)\n"; got != want {
			t.Fatalf("got %q", got)
		}
		got = query(t, s, map[string]string{tblPods: "t"}, `SELECT count(*) FROM pods WHERE "NAMESPACE" = 'default'`)
		if !strings.Contains(got, "\n2\n") {
			t.Fatalf("got %q", got)
		}
		got = query(t, s, map[string]string{tblPods: "t"}, `SELECT sum("RESTARTS") AS r FROM pods`)
		if want := "r\n3\n(1 row)\n"; got != want {
			t.Fatalf("numbers must be numbers, got %q", got)
		}
	})
}

func TestQueryYAML(t *testing.T) {
	eachEngine(t, map[string]string{"y": podsYAML}, func(t *testing.T, s *Service, d dialect) {
		got := query(t, s, map[string]string{tblPods: "y"},
			"SELECT "+d.objName+" AS name, "+d.phase+" AS phase FROM pods ORDER BY name")
		if want := "name | phase\na | Running\nb | Pending\n(2 rows)\n"; got != want {
			t.Fatalf("got %q", got)
		}
	})
}

func TestQueryMarkdownTable(t *testing.T) {
	eachEngine(t, map[string]string{"m": "Summary\n\n| ns | pods |\n|----|------|\n| a | 3 |\n| b | 5 |\n"},
		func(t *testing.T, s *Service, _ dialect) {
			got := query(t, s, map[string]string{"m": "m"}, "SELECT sum(pods) AS total FROM m")
			if want := "total\n8\n(1 row)\n"; got != want {
				t.Fatalf("got %q", got)
			}
		})
}

func TestQueryPlainTextLines(t *testing.T) {
	eachEngine(t, map[string]string{"x": "alpha\nbeta error\ngamma error\n"}, func(t *testing.T, s *Service, _ dialect) {
		got := query(t, s, map[string]string{"log": "x"}, "SELECT count(*) AS n FROM log WHERE line LIKE '%error%'")
		if want := "n\n2\n(1 row)\n"; got != want {
			t.Fatalf("got %q", got)
		}
	})
}

func TestQueryCSVAndJoinAcrossArtifacts(t *testing.T) {
	eachEngine(t, map[string]string{"c": "ns,owner\na,team1\nb,team2\n", "j": `[{"ns":"a","pods":3},{"ns":"b","pods":5}]`},
		func(t *testing.T, s *Service, _ dialect) {
			got := query(t, s, map[string]string{"owners": "c", tblPods: "j"},
				"SELECT o.owner, p.pods FROM owners o JOIN pods p USING (ns) ORDER BY o.owner")
			if want := "owner | pods\nteam1 | 3\nteam2 | 5\n(2 rows)\n"; got != want {
				t.Fatalf("got %q", got)
			}
		})
}

func TestQueryOversizedResultIsCutAndSaysSo(t *testing.T) {
	eachEngine(t, map[string]string{"k": "n\n" + strings.Repeat("row\n", 500)}, func(t *testing.T, s *Service, _ dialect) {
		s.Limits.MaxRows = 5
		got := query(t, s, map[string]string{"t": "k"}, "SELECT line_no FROM t")
		if lines := strings.Count(got, "\n"); lines != 7 {
			t.Fatalf("want header + 5 rows + notice, got %d lines:\n%s", lines, got)
		}
		if !strings.Contains(got, "showing the first 5 rows") {
			t.Fatalf("missing truncation notice: %q", got)
		}
	})
}

func TestQueryMissingArtifact(t *testing.T) {
	eachEngine(t, nil, func(t *testing.T, s *Service, _ dialect) {
		_, err := s.Query(context.Background(), map[string]string{"p": "ns/crew/t/a/none-1"}, selectOne)
		if err == nil || !strings.Contains(err.Error(), `artifact "p"`) || !strings.Contains(err.Error(), "none-1") {
			t.Fatalf("want a missing artifact error naming it, got %v", err)
		}
	})
}

func TestQueryRefusesWritesAndOtherStatements(t *testing.T) {
	eachEngine(t, map[string]string{"k": jsonOneA}, func(t *testing.T, s *Service, d dialect) {
		stmts := append([]string{
			"DROP VIEW t", dropT, "CREATE TABLE x AS SELECT 1", insertT,
			"ATTACH '/tmp/other.db'", "ATTACH DATABASE '/tmp/other.db' AS o", "PRAGMA database_list",
			"SELECT 1; DROP VIEW t", "SELECT 1; SELECT 2", "SELECT 1;\n-- c\nDELETE FROM t",
		}, d.writes...)
		for _, sql := range stmts {
			_, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql)
			if !errors.Is(err, ErrNotReadOnly) {
				t.Errorf("%q: want ErrNotReadOnly, got %v", sql, err)
			}
		}
		for _, empty := range []string{" ; ", "", "-- only a comment"} {
			if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, empty); err == nil {
				t.Errorf("%q: an empty query must be refused", empty)
			}
		}
	})
}

func TestQueryCannotCloseTheWrapper(t *testing.T) {
	eachEngine(t, map[string]string{"k": jsonOneA}, func(t *testing.T, s *Service, _ dialect) {
		marker := filepath.Join(s.Dir, "pwned.db")
		sql := "1) LIMIT 1; ATTACH '" + marker + "' AS x; SELECT * FROM (SELECT 1"
		if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql); err == nil {
			t.Fatal("a statement hidden behind a closing parenthesis must be refused")
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("the injected ATTACH created a file")
		}
	})
}

// A line that starts with a dot is a shell command only to the duckdb program, and
// only at the start of an input; text inside a query must never run one.
func TestQueryRunsNoShellCommandFromTheSQLText(t *testing.T) {
	eachEngine(t, map[string]string{"k": jsonOneA}, func(t *testing.T, s *Service, _ dialect) {
		marker := filepath.Join(t.TempDir(), "marker")
		for _, sql := range []string{
			"SELECT 'a\n.shell touch " + marker + "\nb' AS x",
			"SELECT 1 -- note\n.shell touch " + marker,
			"SELECT 1\n.system touch " + marker,
			"SELECT 1\n.read " + marker,
		} {
			_, _ = s.Query(context.Background(), map[string]string{"t": "k"}, sql)
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("%q ran a shell command", sql)
			}
		}
		got := query(t, s, map[string]string{"t": "k"}, "SELECT 'x\n.shell id' AS v")
		if !strings.Contains(got, ".shell id") {
			t.Fatalf("a string literal must come back as text, got %q", got)
		}
	})
}

func TestQueryRefusesANulByte(t *testing.T) {
	eachEngine(t, map[string]string{"k": jsonOneA}, func(t *testing.T, s *Service, _ dialect) {
		if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, "SELECT 1\x00; DROP VIEW t"); err == nil {
			t.Fatal("a NUL byte in the SQL must not pass")
		}
	})
}

func TestQueryTimesOut(t *testing.T) {
	eachEngine(t, map[string]string{"k": jsonOneA}, func(t *testing.T, s *Service, d dialect) {
		s.Limits.Timeout = 400 * time.Millisecond
		start := time.Now()
		_, err := s.Query(context.Background(), map[string]string{"t": "k"}, d.slow)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("want ErrTimeout, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("the timed out query kept running for %s", elapsed)
		}
	})
}

func TestQueryReportsSQLErrorsForTheModelToFix(t *testing.T) {
	eachEngine(t, map[string]string{"k": jsonOneA}, func(t *testing.T, s *Service, _ dialect) {
		_, err := s.Query(context.Background(), map[string]string{"t": "k"}, "SELECT nope FROM t")
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("want an error naming the column, got %v", err)
		}
		if _, err = s.Query(context.Background(), map[string]string{"t": "k"}, "SELECT 1 +"); err == nil {
			t.Fatal("a syntax error must be reported")
		}
	})
}

func TestQueryLeavesNoWorkFilesBehind(t *testing.T) {
	eachEngine(t, map[string]string{"y": podsYAML}, func(t *testing.T, s *Service, _ dialect) {
		query(t, s, map[string]string{"p": "y"}, "SELECT count(*) FROM p")
		entries, err := os.ReadDir(s.Dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), workDirPrefix) {
				t.Fatalf("work directory %s was not removed", e.Name())
			}
		}
	})
}

func TestQueryQuotesAwkwardNames(t *testing.T) {
	eachEngine(t, map[string]string{"it's": jsonOneA}, func(t *testing.T, s *Service, _ dialect) {
		got := query(t, s, map[string]string{"t": "it's"}, "SELECT a FROM t")
		if want := "a\n1\n(1 row)\n"; got != want {
			t.Fatalf("got %q", got)
		}
	})
}
