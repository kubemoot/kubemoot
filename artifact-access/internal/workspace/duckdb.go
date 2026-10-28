package workspace

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// maxEngineOutput caps what one query may write, so a few very wide rows cannot
	// fill memory before the row limit applies.
	maxEngineOutput = 8 << 20
	maxErrorText    = 600
	engineThreads   = 2
	// validateSQL parses the query with DuckDB's own parser: it reports an error for
	// anything but a SELECT and the number of statements it saw.
	validateSQL = "SELECT coalesce((j->>'error')::BOOLEAN, true), coalesce(j->>'error_message', ''), " +
		"coalesce(json_array_length(j->'statements'), 0) FROM (SELECT json_serialize_sql(%s)::JSON AS j)"
)

// DuckDB runs queries with the duckdb command-line program, one process per query.
//
// The process is the isolation boundary. It holds an in-memory database, no
// network, and may read only the files of the named relations: external access is
// switched off, the files are the only allowed paths, extensions cannot load, and
// the configuration is locked before the query runs. The program is linked against
// libc, so the artifact-access image that offers this engine carries it.
type DuckDB struct {
	// Bin is the duckdb executable (a name on PATH or an absolute path).
	Bin string
}

// Hint describes how nested JSON is read in this engine's SQL.
func (DuckDB) Hint() string {
	return "The SQL is DuckDB's. A nested object in JSON or YAML is a struct: read a field with col.key."
}

// Query validates sql as one SELECT statement, then runs it over rels.
func (d DuckDB) Query(ctx context.Context, rels []Relation, sql string, lim Limits) (Result, error) {
	if lim.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lim.Timeout)
		defer cancel()
	}
	sql = strings.TrimRight(strings.TrimSpace(sql), "; \t\r\n")
	if sql == "" {
		return Result{}, fmt.Errorf("%w: the query is empty", ErrNotReadOnly)
	}
	if err := d.validate(ctx, sql); err != nil {
		return Result{}, err
	}
	out, err := d.run(ctx, queryArgs(rels, sql, lim))
	if err != nil {
		return Result{}, err
	}
	return parseCSV(out, lim.MaxRows)
}

// validate returns nil only when sql is exactly one SELECT statement.
func (d DuckDB) validate(ctx context.Context, sql string) error {
	args := []string{"-csv", "-noheader", "-c", fmt.Sprintf(validateSQL, quote(sql))}
	out, err := d.run(ctx, args)
	if err != nil {
		return err
	}
	rec, err := csv.NewReader(bytes.NewReader(out)).Read()
	if err != nil || len(rec) != 3 {
		return fmt.Errorf("check query: unexpected engine output %q", firstLine(string(out)))
	}
	switch {
	case rec[0] == "true" && strings.HasPrefix(rec[1], "Only SELECT"):
		return ErrNotReadOnly
	case rec[0] == "true":
		return fmt.Errorf("invalid SQL: %s", rec[1])
	case rec[2] != "1":
		return fmt.Errorf("%w: found %s statements", ErrNotReadOnly, rec[2])
	}
	return nil
}

// queryArgs builds the command line: lock the process down, define one view per
// relation, lock the configuration, then run the query cut to MaxRows+1 rows so a
// longer result can be reported as truncated. The query is wrapped in a
// sub-select on its own lines; validate has already shown it is one SELECT, so it
// cannot close the wrapper or add a statement.
func queryArgs(rels []Relation, sql string, lim Limits) []string {
	paths := make([]string, 0, len(rels))
	for _, r := range rels {
		paths = append(paths, quote(r.Path))
	}
	stmts := make([]string, 0, len(rels)+8)
	stmts = append(stmts,
		fmt.Sprintf("SET threads=%d", engineThreads),
		fmt.Sprintf("SET memory_limit=%s", quote(lim.MemoryLimit)),
		"SET max_temp_directory_size='512MB'",
		fmt.Sprintf("SET allowed_paths=[%s]", strings.Join(paths, ",")),
		"SET enable_external_access=false",
		"SET autoload_known_extensions=false",
		"SET autoinstall_known_extensions=false")
	for _, r := range rels {
		stmts = append(stmts, viewSQL(r))
	}
	stmts = append(stmts,
		"SET lock_configuration=true",
		fmt.Sprintf("SELECT * FROM (\n%s\n) LIMIT %d", sql, lim.MaxRows+1))
	args := append(make([]string, 0, 3+2*len(stmts)), "-bail", "-csv", "-header")
	for _, s := range stmts {
		args = append(args, "-c", s)
	}
	return args
}

// viewSQL defines the table a relation's name stands for.
func viewSQL(r Relation) string {
	src := fmt.Sprintf("read_json_auto(%s)", quote(r.Path))
	switch r.Kind {
	case KindJSONItems:
		src = fmt.Sprintf("(SELECT i.* FROM (SELECT unnest(items) AS i FROM %s))", src)
	case KindCSV:
		src = fmt.Sprintf("read_csv_auto(%s, header=true)", quote(r.Path))
	}
	return fmt.Sprintf("CREATE VIEW %s AS SELECT * FROM %s", ident(r.Name), src)
}

// run executes the program with an empty environment and a private home directory,
// so it reads no init file and finds no installed extension.
func (d DuckDB) run(ctx context.Context, args []string) ([]byte, error) {
	home, err := os.MkdirTemp("", "duckdb-home-")
	if err != nil {
		return nil, fmt.Errorf("engine home: %w", err)
	}
	defer func() { _ = os.RemoveAll(home) }()

	cmd := exec.CommandContext(ctx, d.Bin, args...)
	cmd.Env = []string{"HOME=" + home}
	cmd.Dir = home
	cmd.WaitDelay = time.Second
	stdout := &cappedBuffer{limit: maxEngineOutput}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr

	runErr := cmd.Run()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, ErrTimeout
	case stdout.exceeded:
		return nil, errors.New("the result is too large; select fewer columns or rows")
	case runErr != nil:
		return nil, engineError(stderr.String(), runErr)
	}
	return stdout.Bytes(), nil
}

// engineError turns the program's complaint into an error a model can act on. An
// attempt to reach a file or address outside the named artifacts is ErrRefused.
func engineError(stderr string, runErr error) error {
	text := cleanEngineText(stderr)
	if text == "" {
		return fmt.Errorf("engine failed: %w", runErr)
	}
	if strings.Contains(text, "file system operations are disabled") ||
		strings.Contains(text, "configuration has been locked") ||
		strings.Contains(text, "not in the list of allowed") {
		return fmt.Errorf("%w (%s)", ErrRefused, firstLine(text))
	}
	return errors.New(capErrorText(text))
}

// cleanEngineText drops the parts of the program's message that mislead a model: its
// hint about agent mode and line pointers into the wrapped query.
func cleanEngineText(stderr string) string {
	var keep []string
	for _, line := range strings.Split(stderr, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "hint:") || strings.HasPrefix(t, "LINE ") || strings.Trim(t, "^") == "" {
			continue
		}
		keep = append(keep, t)
	}
	return strings.Join(keep, "\n")
}

// parseCSV reads the engine's CSV output: a header row, then up to maxRows rows.
func parseCSV(out []byte, maxRows int) (Result, error) {
	r := csv.NewReader(bytes.NewReader(out))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil {
		return Result{}, fmt.Errorf("read engine output: %w", err)
	}
	if len(recs) == 0 {
		return Result{}, errors.New("the query returned no result set")
	}
	res := Result{Columns: recs[0], Rows: recs[1:]}
	if len(res.Rows) > maxRows {
		res.Rows, res.Truncated = res.Rows[:maxRows], true
	}
	return res, nil
}

// quote renders s as a SQL string literal.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ident renders s as a SQL identifier.
func ident(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// cappedBuffer collects up to limit bytes and then fails the write, which ends the
// program with a broken pipe instead of letting it fill memory.
type cappedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.Len()+len(p) > c.limit {
		c.exceeded = true
		return 0, io.ErrShortWrite
	}
	return c.Buffer.Write(p)
}
