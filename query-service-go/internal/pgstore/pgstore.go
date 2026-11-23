// Package pgstore searches the pgvector tables the indexer writes, one pool per
// distinct database connection.
package pgstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

const (
	undefinedTable = "42P01"
	maxConns       = 4
	connectTimeout = 10 * time.Second
	// RetireGrace is how long a pool no source lists any more stays available to requests
	// that started under the previous snapshot. It is longer than the 30 second limit the
	// API puts on a query, so such a request always finds its pool.
	RetireGrace = 45 * time.Second
)

// ErrNoTable means the source's table does not exist yet (indexing has not finished).
var ErrNoTable = errors.New("table does not exist")

// Row is one search hit.
type Row struct {
	Content  string
	Metadata map[string]any
	Score    float64
}

// Store holds the connection pools of the current registry snapshot, and the pools of the
// snapshot before it for a grace period.
type Store struct {
	mu      sync.Mutex
	pools   map[registry.Conn]*pgxpool.Pool
	retired map[registry.Conn]retiredPool
	now     func() time.Time
}

// retiredPool is a pool no source of the current snapshot uses. A request that started
// under the previous snapshot may still look it up until the grace period ends.
type retiredPool struct {
	pool  *pgxpool.Pool
	until time.Time
}

// New returns an empty Store. Pools open when a snapshot lists them and connect on first
// use, so the service starts while the database is down.
func New() *Store {
	return &Store{
		pools:   map[registry.Conn]*pgxpool.Pool{},
		retired: map[registry.Conn]retiredPool{},
		now:     time.Now,
	}
}

// Sync makes the pools match a snapshot before the snapshot is published: it opens pools
// for new connections (or takes back a retired one) and retires the ones no source uses
// any more. If a pool cannot be opened nothing changes and the error is returned.
func (s *Store) Sync(next *registry.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[registry.Conn]bool{}
	for _, e := range next.Entries {
		want[e.Conn] = true
	}
	opened, err := s.openAllLocked(want)
	if err != nil {
		for _, conn := range opened {
			s.pools[conn].Close()
			delete(s.pools, conn)
		}
		return err
	}
	for conn, pool := range s.pools {
		if !want[conn] {
			delete(s.pools, conn)
			s.retired[conn] = retiredPool{pool: pool, until: s.now().Add(RetireGrace)}
		}
	}
	s.sweepLocked()
	return nil
}

// openAllLocked makes sure every wanted connection has a pool. It returns the connections
// it opened new, so a failure can undo them.
func (s *Store) openAllLocked(want map[registry.Conn]bool) ([]registry.Conn, error) {
	var opened []registry.Conn
	for conn := range want {
		fresh, err := s.openLocked(conn)
		if err != nil {
			return opened, err
		}
		if fresh {
			opened = append(opened, conn)
		}
	}
	return opened, nil
}

// openLocked gives conn an active pool: the one it has, a retired one taken back, or a new
// one (reported as fresh).
func (s *Store) openLocked(conn registry.Conn) (fresh bool, err error) {
	if _, ok := s.pools[conn]; ok {
		return false, nil
	}
	if r, ok := s.retired[conn]; ok {
		delete(s.retired, conn)
		s.pools[conn] = r.pool
		return false, nil
	}
	cfg, err := pgxpool.ParseConfig(conn.URL())
	if err != nil {
		return false, fmt.Errorf("connection %s: %w", conn, err)
	}
	cfg.MaxConns = maxConns
	cfg.ConnConfig.ConnectTimeout = connectTimeout
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return false, fmt.Errorf("connection %s: %w", conn, err)
	}
	s.pools[conn] = pool
	return true, nil
}

// sweepLocked closes retired pools whose grace period is over. Closing waits for borrowed
// connections to come back, so it runs off the lock.
func (s *Store) sweepLocked() {
	for conn, r := range s.retired {
		if !s.now().Before(r.until) {
			delete(s.retired, conn)
			go r.pool.Close()
		}
	}
}

// Close closes every pool.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn, pool := range s.pools {
		pool.Close()
		delete(s.pools, conn)
	}
	for conn, r := range s.retired {
		r.pool.Close()
		delete(s.retired, conn)
	}
}

// pool finds the pool for a connection: an active one, or one retired within the grace
// period, so a request that started under the previous snapshot still finishes.
func (s *Store) pool(conn registry.Conn) (*pgxpool.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if pool, ok := s.pools[conn]; ok {
		return pool, nil
	}
	if r, ok := s.retired[conn]; ok {
		return r.pool, nil
	}
	return nil, fmt.Errorf("no connection pool for %s", conn)
}

// Ping runs SELECT 1 on the source's database.
func (s *Store) Ping(ctx context.Context, e *registry.Entry) error {
	pool, err := s.pool(e.Conn)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, "SELECT 1")
	return err
}

// Count is the number of rows in the source's table.
func (s *Store) Count(ctx context.Context, e *registry.Entry) (int64, error) {
	pool, err := s.pool(e.Conn)
	if err != nil {
		return 0, err
	}
	var n int64
	err = pool.QueryRow(ctx, CountSQL(e.Table())).Scan(&n)
	return n, classify(err)
}

// Search returns the topK nearest rows by cosine distance, best first. A non-nil
// threshold keeps only rows whose score is at least that.
func (s *Store) Search(ctx context.Context, e *registry.Entry, vec []float64, topK int,
	threshold *float64) ([]Row, error) {
	pool, err := s.pool(e.Conn)
	if err != nil {
		return nil, err
	}
	sqlText, args := SearchSQL(e.Table(), vec, topK, threshold)
	rows, err := pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, classify(err)
	}
	out, err := scanRows(rows)
	return out, classify(err)
}

func scanRows(rows pgx.Rows) ([]Row, error) {
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var (
			r    Row
			meta []byte
		)
		if err := rows.Scan(&r.Content, &meta, &r.Score); err != nil {
			return nil, err
		}
		m, err := decodeMetadata(meta)
		if err != nil {
			return nil, err
		}
		r.Metadata = m
		out = append(out, r)
	}
	return out, rows.Err()
}

// decodeMetadata reads a row's metadata column; NULL and an empty object read as {}.
func decodeMetadata(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("metadata is not a JSON object: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// classify turns a missing-table error into ErrNoTable and leaves the rest alone.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == undefinedTable {
		return fmt.Errorf("%w: %s", ErrNoTable, pgErr.Message)
	}
	return err
}

// quote quotes a table name as an SQL identifier, so a collection with a hyphen or a
// quote character cannot change the statement.
func quote(table string) string { return pgx.Identifier{table}.Sanitize() }

// CountSQL is the statement behind Count.
func CountSQL(table string) string { return "SELECT COUNT(*) FROM " + quote(table) }

// SearchSQL builds the similarity statement and its arguments. The vector goes in as a
// text literal cast to vector; the threshold, when set, is applied in the WHERE clause.
func SearchSQL(table string, vec []float64, topK int, threshold *float64) (string, []any) {
	const score = "1 - (embedding <=> $1::vector)"
	var b strings.Builder
	b.WriteString("SELECT content, metadata, " + score + " AS score FROM " + quote(table))
	args := []any{VectorLiteral(vec)}
	if threshold != nil {
		b.WriteString(" WHERE " + score + " >= $2")
		args = append(args, *threshold)
	}
	b.WriteString(" ORDER BY embedding <=> $1::vector LIMIT $" + strconv.Itoa(len(args)+1))
	args = append(args, int64(topK))
	return b.String(), args
}

// VectorLiteral formats a vector as a PostgreSQL vector literal, [x,y,z].
func VectorLiteral(vec []float64) string {
	parts := make([]string, len(vec))
	for i, x := range vec {
		parts[i] = strconv.FormatFloat(x, 'g', -1, 64)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
