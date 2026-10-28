package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEngine records what the service hands the engine and returns a canned result.
type fakeEngine struct {
	rels []Relation
	sql  string
	res  Result
	err  error
}

func (f *fakeEngine) Query(_ context.Context, rels []Relation, sql string, _ Limits) (Result, error) {
	f.rels, f.sql = rels, sql
	return f.res, f.err
}

type mapStore map[string]string

func (m mapStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s, ok := m[key]
	if !ok {
		return nil, errors.New("no such object")
	}
	return io.NopCloser(strings.NewReader(s)), nil
}

func fakeService(t *testing.T, eng Engine, objects mapStore) *Service {
	t.Helper()
	return &Service{
		Store: objects, Dir: t.TempDir(), Engine: eng,
		Limits:           Limits{Timeout: time.Second, MaxRows: 10, MemoryLimit: mem64},
		MaxArtifactBytes: 1024,
	}
}

func TestServiceMaterializesAnArtifactThatIsNotAFileYet(t *testing.T) {
	eng := &fakeEngine{res: Result{Columns: []string{"n"}, Rows: [][]string{{"2"}}}}
	s := fakeService(t, eng, mapStore{"ns/c/t/a/pods-1": jsonTwoA})
	out, err := s.Query(context.Background(), map[string]string{tblPods: "ns/c/t/a/pods-1"}, "SELECT count(*) AS n FROM pods")
	if err != nil {
		t.Fatal(err)
	}
	if out != "n\n2\n(1 row)\n" {
		t.Fatalf("out = %q", out)
	}
	want := filepath.Join(s.Dir, "ns/c/t/a/pods-1")
	if len(eng.rels) != 1 || eng.rels[0].Path != want || eng.rels[0].Kind != KindJSON || eng.rels[0].Name != tblPods {
		t.Fatalf("relations = %+v, want path %s", eng.rels, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("artifact was not materialized under the artifact directory: %v", err)
	}
}

func TestServiceUsesAFileThatIsAlreadyMaterialized(t *testing.T) {
	eng := &fakeEngine{}
	s := fakeService(t, eng, mapStore{})
	path := filepath.Join(s.Dir, "k")
	if err := os.WriteFile(path, []byte(csvAB), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, selectOne); err != nil {
		t.Fatal(err)
	}
	if eng.rels[0].Path != path || eng.rels[0].Kind != KindCSV {
		t.Fatalf("relations = %+v", eng.rels)
	}
}

func TestServiceNamesTheMissingArtifact(t *testing.T) {
	s := fakeService(t, &fakeEngine{}, mapStore{})
	_, err := s.Query(context.Background(), map[string]string{tblPods: "ns/c/t/a/none-9"}, selectOne)
	if err == nil || !strings.Contains(err.Error(), `"pods"`) || !strings.Contains(err.Error(), "none-9") {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceRefusesBadRequests(t *testing.T) {
	s := fakeService(t, &fakeEngine{}, mapStore{"k": "x"})
	tooMany := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		tooMany[n] = "k"
	}
	cases := []struct {
		name      string
		artifacts map[string]string
		sql       string
		want      string
	}{
		{"no sql", map[string]string{"t": "k"}, "  ", "sql is required"},
		{"no artifacts", nil, selectOne, "artifacts is required"},
		{"too many", tooMany, selectOne, "at most 8"},
		{"digit first", map[string]string{"1t": "k"}, selectOne, mustBeLetters},
		{"space in name", map[string]string{"my table": "k"}, selectOne, mustBeLetters},
		{"quote in name", map[string]string{`a"b`: "k"}, selectOne, mustBeLetters},
		{"traversal key", map[string]string{"t": "../../etc/passwd"}, selectOne, badKey},
		{"absolute key", map[string]string{"t": "/etc/passwd"}, selectOne, badKey},
		{"empty key", map[string]string{"t": ""}, selectOne, badKey},
		{"another query's work files", map[string]string{"t": ".query-123/x.csv"}, selectOne, badKey},
		{"names that differ only by case", map[string]string{"Pods": "k", tblPods: "k"}, selectOne, "same name"},
	}
	for _, tc := range cases {
		_, err := s.Query(context.Background(), tc.artifacts, tc.sql)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
}

func TestServiceRefusesAnArtifactOverTheSizeLimit(t *testing.T) {
	s := fakeService(t, &fakeEngine{}, mapStore{"big": strings.Repeat("x", 2048)})
	_, err := s.Query(context.Background(), map[string]string{"t": "big"}, selectOne)
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestServicePassesEngineErrorsThrough(t *testing.T) {
	for _, want := range []error{ErrTimeout, ErrNotReadOnly, ErrRefused} {
		s := fakeService(t, &fakeEngine{err: want}, mapStore{"k": csvAB})
		if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, selectOne); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	}
}

func TestServiceRemovesItsWorkDirectory(t *testing.T) {
	s := fakeService(t, &fakeEngine{}, mapStore{"y": "a: 1\nb: 2\n"})
	if _, err := s.Query(context.Background(), map[string]string{"t": "y"}, selectOne); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), workDirPrefix) {
			t.Fatalf("left %s behind", e.Name())
		}
	}
}

func TestServiceOrdersRelationsByName(t *testing.T) {
	eng := &fakeEngine{}
	s := fakeService(t, eng, mapStore{"k1": csvAB, "k2": csvAB})
	if _, err := s.Query(context.Background(), map[string]string{"zeta": "k1", "alpha": "k2"}, selectOne); err != nil {
		t.Fatal(err)
	}
	if eng.rels[0].Name != "alpha" || eng.rels[1].Name != "zeta" {
		t.Fatalf("relations = %+v", eng.rels)
	}
}

func TestRender(t *testing.T) {
	got := Render(Result{Columns: []string{"a", "b"}, Rows: [][]string{{"1", "x\ny"}, {"2", ""}}})
	if want := "a | b\n1 | x y\n2 | \n(2 rows)\n"; got != want {
		t.Fatalf("got %q", got)
	}
	got = Render(Result{Columns: []string{"a"}, Rows: [][]string{{"1"}}, Truncated: true})
	if !strings.Contains(got, "showing the first 1 rows") || strings.Contains(got, "(1 row)") {
		t.Fatalf("got %q", got)
	}
	if got := Render(Result{Columns: []string{"a"}}); got != "a\n(0 rows)\n" {
		t.Fatalf("empty = %q", got)
	}
}

func TestQueryArgsLockDownBeforeTheQuery(t *testing.T) {
	args := queryArgs([]Relation{{Name: "t", Path: artifactK, Kind: KindJSON}}, selectOne, Limits{MaxRows: 5, MemoryLimit: mem64})
	joined := strings.Join(args, "\n")
	order := []string{
		"SET memory_limit='64MB'", "SET allowed_paths=['/artifacts/k']", "SET enable_external_access=false",
		"SET autoinstall_known_extensions=false", "CREATE VIEW \"t\"", "SET lock_configuration=true", "LIMIT 6",
	}
	last := -1
	for _, want := range order {
		i := strings.Index(joined, want)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of order in:\n%s", want, joined)
		}
		last = i
	}
	if args[0] != "-bail" {
		t.Fatalf("a failed setup statement must stop the run, args = %v", args[:3])
	}
}

func TestViewSQLForEachKind(t *testing.T) {
	cases := map[Kind]string{
		KindJSON:      `read_json_auto('/p')`,
		KindJSONItems: `unnest(items)`,
		KindCSV:       `read_csv_auto('/p', header=true)`,
	}
	for kind, want := range cases {
		if got := viewSQL(Relation{Name: "t", Path: "/p", Kind: kind}); !strings.Contains(got, want) {
			t.Errorf("%s: %s lacks %s", kind, got, want)
		}
	}
	got := viewSQL(Relation{Name: `we"ird`, Path: "/it's", Kind: KindJSON})
	if !strings.Contains(got, `"we""ird"`) || !strings.Contains(got, `'/it''s'`) {
		t.Errorf("quoting: %s", got)
	}
}

func TestParseCSV(t *testing.T) {
	res, err := parseCSV([]byte("a,b\n1,2\n3,4\n5,6\n"), 2)
	if err != nil || !res.Truncated || len(res.Rows) != 2 || res.Columns[1] != "b" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	res, err = parseCSV([]byte("a\n"), 2)
	if err != nil || res.Truncated || len(res.Rows) != 0 {
		t.Fatalf("header only: %+v %v", res, err)
	}
	if _, err := parseCSV(nil, 2); err == nil {
		t.Fatal("no output must be an error")
	}
}

func TestEngineErrorClassification(t *testing.T) {
	runErr := errors.New("exit status 1")
	for _, msg := range []string{
		`Permission Error: Cannot access file "/etc/passwd" - file system operations are disabled by configuration`,
		`Invalid Input Error: Cannot change configuration option "x" - the configuration has been locked`,
	} {
		if err := engineError(msg, runErr); !errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v", msg, err)
		}
	}
	err := engineError("Binder Error: column \"x\" not found\nLINE 2: SELECT x\n        ^\nhint: -agent renders", runErr)
	if errors.Is(err, ErrRefused) || strings.Contains(err.Error(), "hint") || strings.Contains(err.Error(), "LINE") {
		t.Errorf("binder error = %v", err)
	}
	if err := engineError("", runErr); !errors.Is(err, runErr) {
		t.Errorf("silent failure = %v", err)
	}
	long := engineError(strings.Repeat("x", 2000), runErr)
	if len(long.Error()) > maxErrorText+10 {
		t.Errorf("error text not capped: %d", len(long.Error()))
	}
}

func TestCappedBufferStopsAtItsLimit(t *testing.T) {
	c := &cappedBuffer{limit: 4}
	if _, err := c.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("de")); err == nil || !c.exceeded {
		t.Fatal("a write past the limit must fail")
	}
}

func envFrom(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFromEnvOffersNothingByDefault(t *testing.T) {
	for _, m := range []map[string]string{nil, {EnvEngine: EngineNone}} {
		if s, err := FromEnv(envFrom(m), mapStore{}, "/artifacts"); s != nil || err != nil {
			t.Fatalf("%v must offer nothing: %v %v", m, s, err)
		}
	}
}

func TestFromEnvRefusesUnknownEngineAndMissingProgram(t *testing.T) {
	for _, m := range []map[string]string{
		{EnvEngine: "sqlite"},
		{EnvEngine: EngineDuckDB, EnvDuckDBBin: "/no/such/duckdb"},
	} {
		if s, err := FromEnv(envFrom(m), mapStore{}, "/artifacts"); s != nil || err == nil {
			t.Errorf("%v: want an error, got %v %v", m, s, err)
		}
	}
}

func TestFromEnvLimits(t *testing.T) {
	bin := writeFakeBinary(t)
	with := func(m map[string]string) func(string) string {
		m[EnvEngine], m[EnvDuckDBBin] = EngineDuckDB, bin
		return envFrom(m)
	}
	s, err := FromEnv(with(map[string]string{}), mapStore{}, "/artifacts")
	if err != nil || s == nil {
		t.Fatalf("defaults: %v %v", s, err)
	}
	if s.Limits.Timeout != defaultTimeout || s.Limits.MaxRows != defaultMaxRows || s.Limits.MemoryLimit != defaultMemory {
		t.Fatalf("defaults = %+v", s.Limits)
	}
	s, err = FromEnv(with(map[string]string{EnvTimeout: "3s", EnvMaxRows: "7", EnvMemory: mem64}), mapStore{}, "/artifacts")
	if err != nil || s.Limits.Timeout != 3*time.Second || s.Limits.MaxRows != 7 || s.Limits.MemoryLimit != mem64 {
		t.Fatalf("set: %+v %v", s, err)
	}
}

func TestFromEnvRefusesBadLimits(t *testing.T) {
	bin := writeFakeBinary(t)
	for _, m := range []map[string]string{
		{EnvTimeout: "soon"}, {EnvTimeout: "-1s"}, {EnvMaxRows: "0"}, {EnvMaxRows: "many"}, {EnvMaxArtifactBytes: "-5"},
	} {
		m[EnvEngine], m[EnvDuckDBBin] = EngineDuckDB, bin
		if s, err := FromEnv(envFrom(m), mapStore{}, "/artifacts"); err == nil {
			t.Errorf("%v: want an error, got %+v", m, s)
		}
	}
}

// writeFakeBinary makes an executable file so LookPath succeeds without DuckDB.
func writeFakeBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "duckdb")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
