package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	// maxSQLBytes bounds the text of one query.
	maxSQLBytes = 64 << 10
	// defaultHeapBytes applies when Limits.MemoryLimit cannot be read.
	defaultHeapBytes = 256 << 20
)

// SQLite runs queries inside the artifact-access process with a pure-Go build of
// SQLite (no cgo, no extra program or image). Each query gets its own in-memory
// database holding only the named relations.
//
// Read-only is enforced in layers: the text must be one SELECT (checkSelect), it
// runs as a sub-select of a wrapper query so no other statement can be spelled,
// the connection is set query_only, ATTACH is switched off by a zero limit, and
// the build has no extension loading and no file functions. Memory is bounded by a
// ceiling on any one value and the query is interrupted when its context ends.
type SQLite struct{}

// Hint describes how nested JSON is read in this engine's SQL.
func (SQLite) Hint() string {
	return "The SQL is SQLite's. A nested object or list in JSON or YAML is a text column: " +
		"read a field with col->>'key' (or col->>'$.a.b'), a list length with json_array_length(col)."
}

// Query runs sql over rels.
func (SQLite) Query(ctx context.Context, rels []Relation, query string, lim Limits) (Result, error) {
	if err := checkSelect(query); err != nil {
		return Result{}, err
	}
	query = strings.TrimRight(strings.TrimSpace(query), "; \t\r\n")
	if lim.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lim.Timeout)
		defer cancel()
	}
	db, conn, err := openMemory(ctx, lim)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = db.Close() }()

	for _, r := range rels {
		if err := loadRelation(ctx, conn, r); err != nil {
			return Result{}, sqliteError(ctx, fmt.Errorf("load %q: %w", r.Name, err))
		}
	}
	if err := lockDown(ctx, conn); err != nil {
		return Result{}, err
	}
	res, err := runSelect(ctx, conn, query, lim.MaxRows)
	if err != nil {
		return Result{}, sqliteError(ctx, err)
	}
	return res, nil
}

// openMemory opens one private in-memory database on a single connection.
func openMemory(ctx context.Context, lim Limits) (*sql.DB, *sql.Conn, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, nil, fmt.Errorf("open engine: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("open engine: %w", err)
	}
	setup := []string{
		"PRAGMA temp_store=MEMORY",
		"PRAGMA trusted_schema=OFF",
	}
	for _, s := range setup {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("configure engine: %w", err)
		}
	}
	// This SQLite build does not enforce a heap limit, so memory is bounded by the
	// artifact size cap, by a ceiling on the size of any one string, blob or row (a
	// quarter of the memory limit), by the row limit and by the timeout.
	limits := map[int]int{
		sqlite3.SQLITE_LIMIT_SQL_LENGTH: maxSQLBytes * 2,
		sqlite3.SQLITE_LIMIT_LENGTH:     int(min(heapBytes(lim.MemoryLimit)/4, 1<<30)),
	}
	for id, val := range limits {
		if _, err := sqlite.Limit(conn, id, val); err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("configure engine: %w", err)
		}
	}
	return db, conn, nil
}

// lockDown makes the connection refuse writes and ATTACH once the data is loaded.
func lockDown(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return fmt.Errorf("lock engine: %w", err)
	}
	if _, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_ATTACHED, 0); err != nil {
		return fmt.Errorf("lock engine: %w", err)
	}
	return nil
}

// runSelect runs the checked query as a sub-select cut to maxRows+1 rows, so a longer
// result is reported as truncated.
func runSelect(ctx context.Context, conn *sql.Conn, query string, maxRows int) (Result, error) {
	wrapped := fmt.Sprintf("SELECT * FROM (\n%s\n) LIMIT %d", query, maxRows+1)
	rows, err := conn.QueryContext(ctx, wrapped)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return Result{}, err
	}
	res := Result{Columns: cols}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(res.Rows) == maxRows {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Result{}, err
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			row[i] = cellText(v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	return res, nil
}

// cellText renders one value the way the result text shows it.
func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

// sqliteError maps an engine failure to the package's errors.
func sqliteError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ErrTimeout
	case strings.Contains(err.Error(), "out of memory"), strings.Contains(err.Error(), "too big"):
		return errors.New("the query used more memory than allowed; select fewer columns or rows")
	}
	msg := err.Error()
	if len(msg) > maxErrorText {
		msg = msg[:maxErrorText] + "..."
	}
	return errors.New(msg)
}

// heapBytes reads a limit such as "256MB" or "1GiB"; an unreadable value gives the default.
func heapBytes(s string) int64 {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n <= 0 {
		return defaultHeapBytes
	}
	return n * mult
}
