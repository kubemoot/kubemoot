package pgstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

// testDatabaseEnv names a PostgreSQL URL with the pgvector extension available, for
// example postgres://postgres@127.0.0.1:5432/vectors. Without it these tests skip: the
// repository's CI has no pgvector, so they run on a developer machine or a pipeline that
// provides one.
const testDatabaseEnv = "KUBEMOOT_TEST_DATABASE_URL"

type fixture struct {
	store *Store
	entry *registry.Entry
	pool  *pgxpool.Pool
}

func newFixture(t *testing.T, collection string) *fixture {
	t.Helper()
	raw := os.Getenv(testDatabaseEnv)
	if raw == "" {
		t.Skipf("%s is not set", testDatabaseEnv)
	}
	conn := connFromURL(t, raw)
	pool, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		t.Fatal(err)
	}
	e := &registry.Entry{Conn: conn, Source: registry.Source{
		Name: "t", VectorStore: registry.VectorStore{Type: "pgvector", Collection: collection},
	}}
	s := New()
	t.Cleanup(s.Close)
	if err := s.Sync(&registry.Snapshot{Entries: map[string]*registry.Entry{"t": e}}); err != nil {
		t.Fatal(err)
	}
	return &fixture{store: s, entry: e, pool: pool}
}

func connFromURL(t *testing.T, raw string) registry.Conn {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pass, _ := u.User.Password()
	return registry.Conn{Host: u.Hostname(), Port: port, DBName: u.Path[1:], User: u.User.Username(), Password: pass}
}

// createTable makes the table the Java indexer (Spring AI) writes: id, content,
// metadata json, embedding vector, with a cosine HNSW index.
func (f *fixture) createTable(t *testing.T, rows [][3]string) {
	t.Helper()
	ctx := context.Background()
	table := quote(f.entry.Table())
	stmts := []string{
		"DROP TABLE IF EXISTS " + table,
		"CREATE TABLE " + table + " (id uuid DEFAULT gen_random_uuid() PRIMARY KEY, content text, metadata json, embedding vector(3))",
		"CREATE INDEX ON " + table + " USING hnsw (embedding vector_cosine_ops)",
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, "DROP TABLE IF EXISTS "+table) })
	for _, r := range rows {
		var meta any
		if r[1] != "" {
			meta = r[1]
		}
		if _, err := f.pool.Exec(ctx, "INSERT INTO "+table+" (content, metadata, embedding) VALUES ($1, $2::json, $3::vector)", r[0], meta, r[2]); err != nil {
			t.Fatal(err)
		}
	}
}

func uniqueName() string { return fmt.Sprintf("qs-test-%d", time.Now().UnixNano()) }

func TestSearchRanksByCosineAndScoresAsOneMinusDistance(t *testing.T) {
	f := newFixture(t, uniqueName())
	f.createTable(t, [][3]string{
		{"east", `{"k":"v","n":1}`, vecEast},
		{"north", "", vecNorth},
		{"northeast", `{}`, "[1,1,0]"},
	})
	rows, err := f.store.Search(context.Background(), f.entry, []float64{1, 0, 0}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Content != "east" || rows[1].Content != "northeast" || rows[2].Content != "north" {
		t.Fatalf("order %+v", rows)
	}
	checkScores(t, rows)
	checkMetadata(t, rows)
}

func TestSearchAppliesTopKAndThreshold(t *testing.T) {
	f := newFixture(t, uniqueName())
	f.createTable(t, [][3]string{{"a", "", vecEast}, {"b", "", "[1,1,0]"}, {"c", "", vecNorth}})
	ctx := context.Background()
	rows, err := f.store.Search(ctx, f.entry, []float64{1, 0, 0}, 1, nil)
	if err != nil || len(rows) != 1 || rows[0].Content != "a" {
		t.Fatalf("top 1: %+v, %v", rows, err)
	}
	th := 0.5
	rows, err = f.store.Search(ctx, f.entry, []float64{1, 0, 0}, 10, &th)
	if err != nil || len(rows) != 2 {
		t.Fatalf("threshold 0.5 keeps a and b: %+v, %v", rows, err)
	}
	th = 1.0
	rows, err = f.store.Search(ctx, f.entry, []float64{0.6, 0.8, 0}, 10, &th)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("nothing reaches 1.0: %#v, %v", rows, err)
	}
}

func TestSearchAndCountOnAMissingTableAreErrNoTable(t *testing.T) {
	f := newFixture(t, uniqueName())
	if _, err := f.store.Search(context.Background(), f.entry, []float64{1, 0, 0}, 3, nil); !errors.Is(err, ErrNoTable) {
		t.Errorf("search: %v", err)
	}
	if _, err := f.store.Count(context.Background(), f.entry); !errors.Is(err, ErrNoTable) {
		t.Errorf("count: %v", err)
	}
}

func TestACollectionNameWithAQuoteIsAnIdentifierNotSQL(t *testing.T) {
	f := newFixture(t, `odd"name; DROP TABLE x`)
	f.createTable(t, [][3]string{{"only", "", vecEast}})
	rows, err := f.store.Search(context.Background(), f.entry, []float64{1, 0, 0}, 5, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%+v, %v", rows, err)
	}
}

func TestCountAndPing(t *testing.T) {
	f := newFixture(t, uniqueName())
	f.createTable(t, [][3]string{{"a", "", vecEast}, {"b", "", vecNorth}})
	if n, err := f.store.Count(context.Background(), f.entry); err != nil || n != 2 {
		t.Fatalf("count %d, %v", n, err)
	}
	if err := f.store.Ping(context.Background(), f.entry); err != nil {
		t.Fatal(err)
	}
}

func TestSyncClosesPoolsNoSourceUsesAndKeepsTheRest(t *testing.T) {
	f := newFixture(t, uniqueName())
	other := f.entry.Conn
	other.DBName = "other"
	e2 := &registry.Entry{Conn: other, Source: f.entry.Source}
	snap := &registry.Snapshot{Entries: map[string]*registry.Entry{"t": f.entry, "o": e2}}
	if err := f.store.Sync(snap); err != nil {
		t.Fatal(err)
	}
	if len(f.store.pools) != 2 {
		t.Fatalf("pools %d", len(f.store.pools))
	}
	if err := f.store.Sync(&registry.Snapshot{Entries: map[string]*registry.Entry{"t": f.entry}}); err != nil {
		t.Fatal(err)
	}
	if len(f.store.pools) != 1 {
		t.Fatalf("pools after the drop %d", len(f.store.pools))
	}
}

func TestAPoolForAnUnreachableDatabaseOpensLazily(t *testing.T) {
	s := New()
	defer s.Close()
	e := &registry.Entry{Conn: registry.Conn{Host: loopback, Port: 1, DBName: "x", User: "u"}}
	if err := s.Sync(&registry.Snapshot{Entries: map[string]*registry.Entry{"x": e}}); err != nil {
		t.Fatalf("the service must start with the database down: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Ping(ctx, e); err == nil {
		t.Fatal("ping must fail")
	}
}

func TestAnEntryWithoutAPoolIsAnError(t *testing.T) {
	s := New()
	if err := s.Ping(context.Background(), &registry.Entry{}); err == nil {
		t.Fatal("expected an error")
	}
}

// Unit vectors the fixtures use.
const (
	vecEast  = "[1,0,0]"
	vecNorth = "[0,1,0]"
)

func checkScores(t *testing.T, rows []Row) {
	t.Helper()
	if rows[0].Score < 0.999999 || rows[2].Score > 1e-6 {
		t.Errorf("scores %v %v", rows[0].Score, rows[2].Score)
	}
}

// checkMetadata expects a real object, an empty object for {}, and {} (not nil) for NULL.
func checkMetadata(t *testing.T, rows []Row) {
	t.Helper()
	if rows[0].Metadata["k"] != "v" || len(rows[1].Metadata) != 0 || rows[2].Metadata == nil {
		t.Errorf("metadata %+v", []map[string]any{rows[0].Metadata, rows[1].Metadata, rows[2].Metadata})
	}
}
