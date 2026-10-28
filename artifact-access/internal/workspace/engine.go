// Package workspace answers read-only SQL over the artifacts of one discussion.
//
// The artifacts stay in the NATS object store and are materialized as files under
// the artifact directory. A query names the artifacts it needs; each is turned
// into a table the query engine can read (a JSON list, a kubectl table, YAML, a
// Markdown table, CSV, or plain text lines) and the engine runs one SELECT with a
// time limit, a row limit and no access to anything but those tables.
//
// The query engine sits behind the Engine interface. DuckDB is the engine Kubemoot
// ships; another engine that reads the same relations can replace it through the
// operator chart value artifacts.queryEngine without a change to crews or prompts.
package workspace

import (
	"context"
	"errors"
	"time"
)

// Kind says how an engine reads a Relation's file.
type Kind string

const (
	// KindJSON is a JSON document: a list of objects is one row per object.
	KindJSON Kind = "json"
	// KindJSONItems is a JSON object whose "items" list is the rows (kubectl -o json).
	KindJSONItems Kind = "json-items"
	// KindCSV is a CSV file with a header row.
	KindCSV Kind = "csv"
)

// Relation is one named table a query can read.
type Relation struct {
	Name string
	Path string
	Kind Kind
}

// Limits bound one query.
type Limits struct {
	// Timeout stops a query that runs too long.
	Timeout time.Duration
	// MaxRows is the most rows a result carries; the rest are cut off.
	MaxRows int
	// MemoryLimit is the engine's memory ceiling, in the engine's own notation
	// (for DuckDB, "256MB").
	MemoryLimit string
}

// Result is the rows a query produced, as text.
type Result struct {
	Columns []string
	Rows    [][]string
	// Truncated is true when the query produced more than Limits.MaxRows rows.
	Truncated bool
}

// Engine runs one read-only SQL query over relations.
type Engine interface {
	Query(ctx context.Context, rels []Relation, sql string, lim Limits) (Result, error)
}

var (
	// ErrNotReadOnly means the SQL is not exactly one SELECT statement.
	ErrNotReadOnly = errors.New("only one read-only SELECT query is allowed")
	// ErrTimeout means the query ran past its time limit and was stopped.
	ErrTimeout = errors.New("query timed out")
	// ErrRefused means the query tried to reach something other than the named artifacts.
	ErrRefused = errors.New("query may read only the named artifacts: no other files and no network")
)
