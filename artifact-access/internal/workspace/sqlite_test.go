package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sqliteService(t *testing.T, files map[string]string) *Service {
	t.Helper()
	return newService(t, engineSQLite, files)
}

func TestCheckSelectAcceptsOneSelect(t *testing.T) {
	ok := []string{
		selectOne, "select 1;", "  SELECT 1 ;; ", "WITH a AS (SELECT 1) SELECT * FROM a", "VALUES (1)",
		"SELECT ';' AS semi", `SELECT "a;b" FROM t`, "SELECT 1 -- DROP TABLE t; \n", "SELECT /* ; DROP */ 1",
		"SELECT 'it''s; fine'", "SELECT [a;b] FROM t", "SELECT (1)", "-- lead\nSELECT 1",
	}
	for _, q := range ok {
		if _, err := checkSelect(q); err != nil {
			t.Errorf("%q must pass: %v", q, err)
		}
	}
}

func TestCheckSelectCutsAtTheTerminator(t *testing.T) {
	cases := map[string]string{
		"SELECT 1;":                              selectOne,
		"  SELECT 1 ; -- note":                   "SELECT 1",
		"SELECT 1; /* later */":                  "SELECT 1",
		"SELECT ';' AS s; \n":                    "SELECT ';' AS s",
		"SELECT 1 -- keep; this\n":               "SELECT 1 -- keep; this",
		"WITH a AS (SELECT 1) SELECT * FROM a;;": "WITH a AS (SELECT 1) SELECT * FROM a",
	}
	for in, want := range cases {
		got, err := checkSelect(in)
		if err != nil || got != want {
			t.Errorf("checkSelect(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestCheckSelectRefusesEverythingElse(t *testing.T) {
	notReadOnly := []string{
		"", " ", "-- c", insertT, "pragma x", "ATTACH 'f'", "VACUUM INTO 'f'", dropT,
		"SELECT 1; SELECT 2", "SELECT 1; -- c\n" + dropT, "SELECT 1;'x'", "SELECT 1; (SELECT 1)", "SELECT 1)",
		"SELECT (1", "SELECT 1\x00", "'x'", "EXPLAIN SELECT 1", "1) ; " + dropT + "; (",
	}
	for _, q := range notReadOnly {
		if _, err := checkSelect(q); !errors.Is(err, ErrNotReadOnly) {
			t.Errorf("%q: want ErrNotReadOnly, got %v", q, err)
		}
	}
}

func TestCheckSelectRefusesUnterminatedText(t *testing.T) {
	for _, q := range []string{"SELECT 'open", "SELECT /* open", `SELECT "open`, "SELECT [open"} {
		if _, err := checkSelect(q); err == nil || errors.Is(err, ErrNotReadOnly) {
			t.Errorf("%q: want an invalid SQL error, got %v", q, err)
		}
	}
	if _, err := checkSelect("SELECT " + strings.Repeat("1,", maxSQLBytes)); err == nil {
		t.Error("an oversized query must be refused")
	}
}

// The text check is the first layer. These show the layers under it hold on their own.
func TestSQLiteConnectionRefusesWritesAndAttach(t *testing.T) {
	ctx := context.Background()
	db, conn, err := openMemory(ctx, Limits{MemoryLimit: mem64})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := conn.ExecContext(ctx, "CREATE TABLE t (a)"); err != nil {
		t.Fatal(err)
	}
	if err := lockDown(ctx, conn); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.db")
	for _, stmt := range []string{
		insertT, "CREATE TABLE x (a)", dropT, "ATTACH '" + other + "' AS o",
		"VACUUM INTO '" + other + "'",
	} {
		if _, execErr := conn.ExecContext(ctx, stmt); execErr == nil {
			t.Errorf("%q must fail on a locked connection", stmt)
		}
	}
	if _, err := os.Stat(other); err == nil {
		t.Error("a file was created outside the database")
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil || n != 0 {
		t.Errorf("rows = %d err = %v, want 0", n, err)
	}
}

func TestSQLiteHasNoExtensionOrFileFunctions(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": jsonOneA})
	for _, sql := range []string{
		"SELECT load_extension('/lib/x.so')", "SELECT load_extension('/lib/x.so', 'init')",
		"SELECT readfile('/etc/passwd')", "SELECT writefile('/tmp/kmwq-x', 'x')", "SELECT edit('x')",
		"SELECT * FROM fsdir('/')", "SELECT zipfile('x')",
	} {
		if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, sql); err == nil {
			t.Errorf("%q must fail", sql)
		}
	}
	if _, err := os.Stat("/tmp/kmwq-x"); err == nil {
		t.Error("writefile wrote a file")
	}
}

func TestSQLiteSeesOnlyTheNamedTables(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": jsonOneA, "other": `[{"secret":1}]`})
	got := query(t, s, map[string]string{"t": "k"}, "SELECT name FROM sqlite_master ORDER BY name")
	if want := "name\nt\n(1 row)\n"; got != want {
		t.Fatalf("got %q, want only the named table", got)
	}
	if _, err := s.Query(context.Background(), map[string]string{"t": "k"}, "SELECT * FROM other"); err == nil {
		t.Fatal("an artifact that was not named must not be a table")
	}
	got = query(t, s, map[string]string{"t": "k"}, "SELECT name FROM pragma_database_list")
	if got != "name\nmain\n(1 row)\n" {
		t.Fatalf("databases = %q", got)
	}
}

func TestSQLiteValueSizeLimitStopsAHungryQuery(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": jsonOneA})
	s.Limits.MemoryLimit = "8MB"
	s.Limits.Timeout = 20 * time.Second
	_, err := s.Query(context.Background(), map[string]string{"t": "k"},
		"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 50) "+
			"SELECT length(group_concat(hex(zeroblob(1000000)))) FROM c")
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("want a memory error before the timeout, got %v", err)
	}
}

func TestSQLiteStoresNestedValuesAsJSONText(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": `[{"id":1,"meta":{"a":{"b":"deep"}},"tags":["x","y"],"ok":true,"none":null,"f":1.5}]`})
	got := query(t, s, map[string]string{"t": "k"},
		"SELECT meta->>'$.a.b' AS deep, json_array_length(tags) AS n, ok, none IS NULL AS missing, f FROM t")
	if want := "deep | n | ok | missing | f\ndeep | 2 | 1 | 1 | 1.5\n(1 row)\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestSQLiteJSONShapes(t *testing.T) {
	cases := map[string]string{
		`{"a":1,"b":"x"}`:                   "a | b\n1 | x\n(1 row)\n",
		`[1,2]`:                             "value\n1\n2\n(2 rows)\n",
		`[{"a":1},{"b":2}]`:                 "a | b\n1 | \n | 2\n(2 rows)\n",
		`[{"A":1,"a":2}]`:                   "A | a_2\n1 | 2\n(1 row)\n",
		`[{"big":12345678901234567890}]`:    "big\n1.2345678901234567e+19\n(1 row)\n",
		`{"kind":"List","items":[{"x":1}]}`: "x\n1\n(1 row)\n",
	}
	for doc, want := range cases {
		s := sqliteService(t, map[string]string{"k": doc})
		if got := query(t, s, map[string]string{"t": "k"}, "SELECT * FROM t"); got != want {
			t.Errorf("%s: got %q, want %q", doc, got, want)
		}
	}
}

// runRelation loads one file as the given kind, bypassing format detection, and runs sql.
func runRelation(t *testing.T, content string, kind Kind, sql string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	lim := Limits{Timeout: 10 * time.Second, MaxRows: 100, MemoryLimit: mem256}
	res, err := SQLite{}.Query(context.Background(), []Relation{{Name: "t", Path: path, Kind: kind}}, sql, lim)
	if err != nil {
		return "", err
	}
	return Render(res), nil
}

func TestSQLiteRefusesUnreadableJSON(t *testing.T) {
	cases := []struct {
		doc  string
		kind Kind
	}{
		{`[1,`, KindJSON}, {`"just a string"`, KindJSON}, {`[]`, KindJSON}, {``, KindJSON},
		{`{"items":5}`, KindJSONItems}, {`{"kind":"x"}`, KindJSONItems}, {`[1]`, KindJSONItems},
	}
	for _, c := range cases {
		if out, err := runRelation(t, c.doc, c.kind, "SELECT * FROM t"); err == nil {
			t.Errorf("%s as %s: want an error, got %q", c.doc, c.kind, out)
		}
	}
}

func TestSQLiteCSVTypesAndEdges(t *testing.T) {
	csv := "id,zip,price,name,\nA1,007,1.5,x,y\n2,008,,z\n3,009,2e2\n"
	got, err := runRelation(t, csv, KindCSV,
		"SELECT typeof(zip) AS z, typeof(price) AS p, sum(price) AS total, count(col5) AS c5, typeof(id) AS i FROM t LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "z | p | total | c5 | i\ntext | real | 201.5 | 1 | text\n(1 row)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got, err = runRelation(t, "n\n1\n2\n-3\n", KindCSV, "SELECT typeof(n) AS k, sum(n) AS s FROM t")
	if err != nil || got != "k | s\ninteger | 0\n(1 row)\n" {
		t.Fatalf("integers: %q %v", got, err)
	}
}

func TestSQLiteRefusesTooManyColumns(t *testing.T) {
	cols := make([]string, maxColumns+1)
	for i := range cols {
		cols[i] = "c" + strconv.Itoa(i)
	}
	if out, err := runRelation(t, strings.Join(cols, ",")+"\n1\n", KindCSV, selectOne); err == nil {
		t.Fatalf("a table over the column limit must be refused, got %q", out)
	}
	obj := map[string]int{}
	for i := 0; i <= maxColumns; i++ {
		obj["k"+strconv.Itoa(i)] = i
	}
	doc, _ := json.Marshal([]any{obj})
	if out, err := runRelation(t, string(doc), KindJSON, selectOne); err == nil {
		t.Fatalf("a JSON table over the column limit must be refused, got %q", out)
	}
}

func TestHeapBytes(t *testing.T) {
	cases := map[string]int64{
		"256MB": 256 << 20, "1GB": 1 << 30, "64MiB": 64 << 20, "512KB": 512 << 10, " 2 mb ": 2 << 20, "1000": 1000,
		"": defaultHeapBytes, "lots": defaultHeapBytes, "-5MB": defaultHeapBytes, "0": defaultHeapBytes,
	}
	for in, want := range cases {
		if got := heapBytes(in); got != want {
			t.Errorf("heapBytes(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCellText(t *testing.T) {
	cases := map[any]string{nil: "", int64(7): "7", 1.5: "1.5", 3.0: "3", true: "true", "s": "s"}
	for in, want := range cases {
		if got := cellText(in); got != want {
			t.Errorf("cellText(%v) = %q, want %q", in, got, want)
		}
	}
	if got := cellText([]byte("b")); got != "b" {
		t.Errorf("bytes = %q", got)
	}
}

func TestSQLiteErrorMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if err := sqliteError(ctx, errors.New("interrupted")); !errors.Is(err, ErrTimeout) {
		t.Errorf("deadline = %v", err)
	}
	err := sqliteError(context.Background(), errors.New("out of memory (7)"))
	if err == nil || !strings.Contains(err.Error(), "more memory") {
		t.Errorf("memory = %v", err)
	}
	long := sqliteError(context.Background(), errors.New(strings.Repeat("x", 2000)))
	if len(long.Error()) > maxErrorText+10 {
		t.Errorf("error text not capped: %d", len(long.Error()))
	}
}

func TestEnginesDescribeTheirDialect(t *testing.T) {
	if h := (&Service{Engine: SQLite{}}).Hint(); !strings.Contains(h, "SQLite") || !strings.Contains(h, "->>") {
		t.Errorf("sqlite hint = %q", h)
	}
	if h := (&Service{Engine: DuckDB{}}).Hint(); !strings.Contains(h, "DuckDB") {
		t.Errorf("duckdb hint = %q", h)
	}
	if h := (&Service{Engine: &fakeEngine{}}).Hint(); h != "" {
		t.Errorf("an engine with no hint gave %q", h)
	}
}

func TestSQLiteKeepsEveryCSVHeaderInPlace(t *testing.T) {
	cases := []struct{ csv, want string }{
		{"a,a,b\n1,2,\n3,4,5\n", "a | a_2 | b\n1 | 2 | \n3 | 4 | 5\n(2 rows)\n"},
		{",col1\n7,8\n", "col1 | col1_2\n7 | 8\n(1 row)\n"},
		{"\ufeffid,name\n1,x\n", "id | name\n1 | x\n(1 row)\n"},
		{"A,a\n1,2\n", "A | a_2\n1 | 2\n(1 row)\n"},
	}
	for _, c := range cases {
		got, err := runRelation(t, c.csv, KindCSV, "SELECT * FROM t")
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.csv, got, err, c.want)
		}
	}
}

func TestSQLiteLoadsMoreRowsThanOneBatch(t *testing.T) {
	var b strings.Builder
	b.WriteString("n,tag\n")
	rows := insertBatch*3 + 7
	for i := 1; i <= rows; i++ {
		b.WriteString(strconv.Itoa(i) + ",x\n")
	}
	got, err := runRelation(t, b.String(), KindCSV, "SELECT count(*) AS c, sum(n) AS s, min(n) AS lo, max(n) AS hi FROM t")
	want := "c | s | lo | hi\n" + strconv.Itoa(rows) + " | " + strconv.Itoa(rows*(rows+1)/2) + " | 1 | " + strconv.Itoa(rows) + "\n(1 row)\n"
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestSQLiteRunsAStatementFollowedByAComment(t *testing.T) {
	s := sqliteService(t, map[string]string{"k": jsonOneA})
	got := query(t, s, map[string]string{"t": "k"}, "SELECT a FROM t; -- that is all")
	if want := "a\n1\n(1 row)\n"; got != want {
		t.Fatalf("got %q", got)
	}
}

// One query that builds many mid-size values stays under every per-value limit, so
// the watch on the process's memory is what stops it.
func TestSQLiteMemoryWatchStopsAQueryThatGrowsTheProcess(t *testing.T) {
	if _, ok := residentBytes(); !ok {
		t.Skip("process size is not readable here")
	}
	s := sqliteService(t, map[string]string{"k": jsonOneA})
	s.Limits.MemoryLimit = "64MB"
	s.Limits.Timeout = 30 * time.Second
	start := time.Now()
	_, err := s.Query(context.Background(), map[string]string{"t": "k"},
		"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 400) "+
			"SELECT x, hex(randomblob(4000000)) AS h FROM c ORDER BY h")
	if !errors.Is(err, errMemory) && (err == nil || !strings.Contains(err.Error(), "more memory")) {
		t.Fatalf("want the memory error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the query ran for %s before it was stopped", elapsed)
	}
}

func TestSQLiteQuerySlotsAreReleasedAndBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var releases []func()
	for i := 0; i < maxConcurrentQueries; i++ {
		release, err := acquireSlot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := acquireSlot(ctx); err == nil {
		t.Fatal("a query beyond the slot count must wait, then give up with its context")
	}
	for _, release := range releases {
		release()
	}
	release, err := acquireSlot(context.Background())
	if err != nil {
		t.Fatalf("slots were not released: %v", err)
	}
	release()
}

func TestWatchMemoryDoesNothingForNoLimit(t *testing.T) {
	ctx, stop := watchMemory(context.Background(), 0)
	defer stop()
	time.Sleep(3 * memoryPoll)
	if ctx.Err() != nil {
		t.Fatalf("a zero limit must not cancel: %v", ctx.Err())
	}
}
