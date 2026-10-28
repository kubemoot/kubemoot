package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// duckBin returns the duckdb program the engine tests run, or skips them when none
// is installed. Set REQUIRE_DUCKDB=1 to fail instead of skip.
func duckBin(t *testing.T) string {
	t.Helper()
	name := "duckdb"
	if v := os.Getenv("ARTIFACT_DUCKDB_BIN"); v != "" {
		name = v
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("REQUIRE_DUCKDB") == "1" {
			t.Fatalf("duckdb program required: %v", err)
		}
		t.Skip("no duckdb program on PATH; engine tests skipped")
	}
	return bin
}

// newService builds a Service over a temp artifact directory holding the given
// artifacts (object key to content), backed by the real DuckDB engine.
func newService(t *testing.T, files map[string]string) *Service {
	t.Helper()
	bin := duckBin(t)
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
		Dir:              dir,
		Engine:           DuckDB{Bin: bin},
		Limits:           Limits{Timeout: 10 * time.Second, MaxRows: 100, MemoryLimit: "256MB"},
		MaxArtifactBytes: 1 << 20,
		Store:            missingStore{},
	}
}

type missingStore struct{}

func (missingStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("no such object %q", key)
}

func query(t *testing.T, s *Service, artifacts map[string]string, sql string) string {
	t.Helper()
	out, err := s.Query(context.Background(), artifacts, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return out
}

func TestQueryJSONList(t *testing.T) {
	s := newService(t, map[string]string{
		podsKey: `[{"ns":"a","pods":3},{"ns":"b","pods":5},{"ns":"c","pods":0}]`,
	})
	got := query(t, s, map[string]string{"p": podsKey}, "SELECT ns, pods FROM p WHERE pods > 0 ORDER BY pods DESC;")
	if want := "ns | pods\nb | 5\na | 3\n(2 rows)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = query(t, s, map[string]string{"p": podsKey}, "SELECT count(*) AS n, sum(pods) AS total FROM p")
	if want := "n | total\n3 | 8\n(1 row)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestQueryKubectlJSONItems(t *testing.T) {
	s := newService(t, map[string]string{"k": `{"apiVersion":"v1","kind":"List","items":[
		{"metadata":{"name":"a"},"status":{"phase":"Running"}},
		{"metadata":{"name":"b"},"status":{"phase":"Running"}},
		{"metadata":{"name":"c"},"status":{"phase":"Pending"}}]}`})
	got := query(t, s, map[string]string{tblPods: "k"},
		"SELECT status.phase AS phase, count(*) AS n FROM pods GROUP BY phase ORDER BY phase")
	if want := "phase | n\nPending | 1\nRunning | 2\n(2 rows)\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestQueryKubectlTable(t *testing.T) {
	s := newService(t, map[string]string{"t": kubectlPods})
	got := query(t, s, map[string]string{tblPods: "t"}, `SELECT "STATUS", count(*) AS n FROM pods GROUP BY 1 ORDER BY 1`)
	if want := "STATUS | n\nPending | 1\nRunning | 2\n(2 rows)\n"; got != want {
		t.Fatalf("got %q", got)
	}
	got = query(t, s, map[string]string{tblPods: "t"}, `SELECT count(*) FROM pods WHERE "NAMESPACE" = 'default'`)
	if !strings.Contains(got, "\n2\n") {
		t.Fatalf("got %q", got)
	}
}

func TestQueryYAML(t *testing.T) {
	s := newService(t, map[string]string{"y": podsYAML})
	got := query(t, s, map[string]string{tblPods: "y"},
		"SELECT metadata.name AS name, status.phase AS phase FROM pods ORDER BY name")
	if want := "name | phase\na | Running\nb | Pending\n(2 rows)\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestQueryMarkdownTable(t *testing.T) {
	s := newService(t, map[string]string{"m": "Summary\n\n| ns | pods |\n|----|------|\n| a | 3 |\n| b | 5 |\n"})
	got := query(t, s, map[string]string{"m": "m"}, "SELECT sum(pods) AS total FROM m")
	if want := "total\n8\n(1 row)\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestQueryPlainTextLines(t *testing.T) {
	s := newService(t, map[string]string{"x": "alpha\nbeta error\ngamma error\n"})
	got := query(t, s, map[string]string{"log": "x"}, "SELECT count(*) AS n FROM log WHERE line LIKE '%error%'")
	if want := "n\n2\n(1 row)\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestQueryCSVAndJoinAcrossArtifacts(t *testing.T) {
	s := newService(t, map[string]string{
		"c": "ns,owner\na,team1\nb,team2\n",
		"j": `[{"ns":"a","pods":3},{"ns":"b","pods":5}]`,
	})
	got := query(t, s, map[string]string{"owners": "c", tblPods: "j"},
		"SELECT o.owner, p.pods FROM owners o JOIN pods p USING (ns) ORDER BY o.owner")
	if want := "owner | pods\nteam1 | 3\nteam2 | 5\n(2 rows)\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestQueryOversizedResultIsCutAndSaysSo(t *testing.T) {
	s := newService(t, map[string]string{"k": "n\n" + strings.Repeat("row\n", 500)})
	s.Limits.MaxRows = 5
	got := query(t, s, map[string]string{"t": "k"}, "SELECT line_no FROM t")
	if lines := strings.Count(got, "\n"); lines != 7 {
		t.Fatalf("want header + 5 rows + notice, got %d lines:\n%s", lines, got)
	}
	if !strings.Contains(got, "showing the first 5 rows") {
		t.Fatalf("missing truncation notice: %q", got)
	}
}

func TestQueryMissingArtifact(t *testing.T) {
	s := newService(t, nil)
	_, err := s.Query(context.Background(), map[string]string{"p": "ns/crew/t/a/none-1"}, selectOne)
	if err == nil || !strings.Contains(err.Error(), `artifact "p"`) || !strings.Contains(err.Error(), "none-1") {
		t.Fatalf("want a missing artifact error naming it, got %v", err)
	}
}

func TestQueryRefusesWritesAndOtherStatements(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA})
	for _, sql := range []string{
		"DROP VIEW t",
		"CREATE TABLE x AS SELECT 1",
		"INSERT INTO t VALUES (1)",
		"COPY (SELECT 1) TO '/tmp/out.csv'",
		"ATTACH '/tmp/other.db'",
		"INSTALL httpfs",
		"SET enable_external_access=true",
		"PRAGMA database_list",
		"SELECT 1; DROP VIEW t",
		"SELECT 1; SELECT 2",
	} {
		_, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql)
		if !errors.Is(err, ErrNotReadOnly) {
			t.Errorf("%q: want ErrNotReadOnly, got %v", sql, err)
		}
	}
	if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, " ; "); err == nil {
		t.Error("an empty query must be refused")
	}
}

func TestQueryCannotCloseTheWrapper(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA})
	marker := filepath.Join(s.Dir, "pwned.csv")
	sql := "1) LIMIT 1; COPY (SELECT 1) TO '" + marker + "'; SELECT * FROM (SELECT 1"
	if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql); err == nil {
		t.Fatal("a statement hidden behind a closing parenthesis must be refused")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the injected COPY wrote a file")
	}
}

// The shell treats a line that starts with a dot as a command only at the start of
// an input; text inside a query must never run one.
func TestQueryRunsNoShellCommandFromTheSQLText(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA})
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
}

func TestQueryRefusesANulByte(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA})
	if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, "SELECT 1\x00; DROP VIEW t"); err == nil {
		t.Fatal("a NUL byte in the SQL must not pass")
	}
}

func TestQueryRefusesFilesOutsideTheNamedArtifacts(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA, "other": `[{"secret":1}]`})
	otherPath := filepath.Join(s.Dir, "other")
	for _, sql := range []string{
		"SELECT * FROM read_text('/etc/passwd')",
		"SELECT * FROM read_json_auto('" + otherPath + "')",
		"SELECT * FROM glob('/*')",
	} {
		_, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%q: want ErrRefused, got %v", sql, err)
		}
	}
}

func TestQueryRefusesTheNetwork(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA})
	for _, sql := range []string{
		"SELECT * FROM read_csv('http://127.0.0.1:9/x.csv')",
		"SELECT * FROM read_parquet('https://example.com/x.parquet')",
		"SELECT * FROM read_json('s3://bucket/key.json')",
	} {
		_, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%q: want ErrRefused, got %v", sql, err)
		}
	}
}

func TestQueryDoesNotDownloadExtensions(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA})
	_, err := s.Query(context.Background(), map[string]string{"t": "k"}, "SELECT * FROM read_markdown('/x.md')")
	if err == nil {
		t.Fatal("a function from an extension that is not built in must fail, not download")
	}
}

func TestQueryTimesOut(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA})
	s.Limits.Timeout = 400 * time.Millisecond
	start := time.Now()
	_, err := s.Query(context.Background(), map[string]string{"t": "k"},
		"SELECT count(*) FROM range(100000000000) a, range(1000) b")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the timed out query kept running for %s", elapsed)
	}
}

func TestQueryReportsSQLErrorsForTheModelToFix(t *testing.T) {
	s := newService(t, map[string]string{"k": jsonOneA})
	_, err := s.Query(context.Background(), map[string]string{"t": "k"}, "SELECT nope FROM t")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want a binder error naming the column, got %v", err)
	}
	_, err = s.Query(context.Background(), map[string]string{"t": "k"}, "SELEC 1")
	if err == nil || !strings.Contains(err.Error(), "invalid SQL") {
		t.Fatalf("want an invalid SQL error, got %v", err)
	}
}

func TestQueryLeavesNoWorkFilesBehind(t *testing.T) {
	s := newService(t, map[string]string{"y": podsYAML})
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
}

func TestQueryQuotesAwkwardNames(t *testing.T) {
	s := newService(t, map[string]string{"it's": jsonOneA})
	got := query(t, s, map[string]string{"t": "it's"}, "SELECT a FROM t")
	if want := "a\n1\n(1 row)\n"; got != want {
		t.Fatalf("got %q", got)
	}
}
