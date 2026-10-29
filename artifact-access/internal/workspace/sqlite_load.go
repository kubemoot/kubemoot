package workspace

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// maxColumns keeps a table well under SQLite's own column limit.
	maxColumns = 500
	// insertBatch is how many rows one INSERT statement carries, and maxVariables is
	// the most values a statement may bind.
	insertBatch  = 200
	maxVariables = 30000
)

var (
	intCell  = regexp.MustCompile(`^-?(?:0|[1-9]\d*)$`)
	realCell = regexp.MustCompile(`^-?(?:(?:0|[1-9]\d*)(?:\.\d+)?|\.\d+)(?:[eE][-+]?\d+)?$`)
)

// loadRelation reads the relation's file into a table of the relation's name. Every
// file is read twice, once to find the columns and once to insert, so a large
// artifact is never held in memory whole.
func loadRelation(ctx context.Context, conn *sql.Conn, r Relation) error {
	var src rowSource
	switch r.Kind {
	case KindCSV:
		src = csvSource{path: r.Path}
	case KindJSONItems:
		src = &jsonSource{path: r.Path, items: true}
	default:
		src = &jsonSource{path: r.Path}
	}
	cols, err := src.columns()
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return errors.New("the artifact has no columns")
	}
	if len(cols) > maxColumns {
		return fmt.Errorf("the artifact has %d columns; the limit is %d", len(cols), maxColumns)
	}
	return insertAll(ctx, conn, r.Name, cols, src)
}

// rowSource yields a table's columns, then its rows in column order.
type rowSource interface {
	columns() ([]string, error)
	rows(cols []string, emit func([]any) error) error
}

func insertAll(ctx context.Context, conn *sql.Conn, table string, cols []string, src rowSource) error {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = ident(c)
	}
	create := fmt.Sprintf("CREATE TABLE %s (%s)", ident(table), strings.Join(names, ", "))
	if _, err := conn.ExecContext(ctx, create); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	b := &batcher{ctx: ctx, tx: tx, table: ident(table), width: len(cols), per: min(insertBatch, maxVariables/len(cols))}
	if err := src.rows(cols, b.add); err != nil {
		return err
	}
	if err := b.flush(); err != nil {
		return err
	}
	return tx.Commit()
}

// batcher inserts rows several at a time, which is much faster than one statement per row.
type batcher struct {
	ctx   context.Context
	tx    *sql.Tx
	table string
	width int
	per   int
	args  []any
	full  *sql.Stmt
}

func (b *batcher) add(row []any) error {
	b.args = append(b.args, row...)
	if len(b.args) < b.per*b.width {
		return nil
	}
	if b.full == nil {
		stmt, err := b.tx.PrepareContext(b.ctx, b.insertSQL(b.per))
		if err != nil {
			return err
		}
		b.full = stmt
	}
	_, err := b.full.ExecContext(b.ctx, b.args...)
	b.args = b.args[:0]
	return err
}

// flush inserts the rows left over after the last full batch.
func (b *batcher) flush() error {
	if b.full != nil {
		_ = b.full.Close()
	}
	if len(b.args) == 0 {
		return nil
	}
	_, err := b.tx.ExecContext(b.ctx, b.insertSQL(len(b.args)/b.width), b.args...)
	return err
}

func (b *batcher) insertSQL(rows int) string {
	group := "(" + strings.TrimSuffix(strings.Repeat("?,", b.width), ",") + ")"
	return "INSERT INTO " + b.table + " VALUES " + strings.TrimSuffix(strings.Repeat(group+",", rows), ",")
}

// columnSet collects column names in the order first seen. SQLite treats names
// without regard to case, so a name that differs only by case gets a suffix.
type columnSet struct {
	names []string
	byKey map[string]string
	lower map[string]bool
}

func newColumnSet() *columnSet {
	return &columnSet{byKey: map[string]string{}, lower: map[string]bool{}}
}

// add maps a key to its column, creating the column when the key is new.
func (c *columnSet) add(key string) {
	if _, ok := c.byKey[key]; ok {
		return
	}
	c.byKey[key] = c.addPositional(key)
}

// addPositional appends a column for a header that must keep its place: a repeated or
// empty name is made unique instead of merged, so the column count equals the width.
func (c *columnSet) addPositional(name string) string {
	base := name
	if base == "" {
		base = "col"
	}
	col := base
	for n := 2; c.lower[strings.ToLower(col)]; n++ {
		col = fmt.Sprintf("%s_%d", base, n)
	}
	c.lower[strings.ToLower(col)] = true
	c.names = append(c.names, col)
	return col
}

// jsonSource reads a JSON list of objects, a single object, or (items) the "items"
// list of a kubectl style object.
type jsonSource struct {
	path   string
	items  bool
	keyCol map[string]string // object key to its column, set by columns
}

func (j *jsonSource) columns() ([]string, error) {
	seen := map[string]bool{}
	err := j.each(func(row map[string]any) error {
		for k := range row {
			seen[k] = true
		}
		if len(seen) > maxColumns {
			return fmt.Errorf("the artifact has more than %d columns", maxColumns)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Map order is random, so the columns come out in name order.
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	set := newColumnSet()
	for _, k := range keys {
		set.add(k)
	}
	j.keyCol = set.byKey
	return set.names, nil
}

func (j *jsonSource) rows(cols []string, emit func([]any) error) error {
	index := make(map[string]int, len(cols))
	for i, c := range cols {
		index[c] = i
	}
	return j.each(func(row map[string]any) error {
		out := make([]any, len(cols))
		for k, v := range row {
			out[index[j.keyCol[k]]] = sqlValue(v)
		}
		return emit(out)
	})
}

// each calls fn for every row of the file.
func (j *jsonSource) each(fn func(map[string]any) error) error {
	f, err := os.Open(j.path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(bufio.NewReader(f))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	switch {
	case tok == json.Delim('[') && !j.items:
		return eachElement(dec, fn)
	case tok == json.Delim('{') && j.items:
		return eachItem(dec, fn)
	case tok == json.Delim('{'):
		return eachObject(f, fn)
	}
	return errors.New("the JSON is not a list or an object")
}

// eachElement calls fn for each element of the array the decoder is inside.
func eachElement(dec *json.Decoder, fn func(map[string]any) error) error {
	for dec.More() {
		var v any
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("read JSON: %w", err)
		}
		row, ok := v.(map[string]any)
		if !ok {
			row = map[string]any{"value": v}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	// The closing bracket proves the list was not cut short.
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	return nil
}

// eachItem finds the "items" list of an object the decoder has just opened.
func eachItem(dec *json.Decoder, fn func(map[string]any) error) error {
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return fmt.Errorf("read JSON: %w", err)
		}
		if key != itemsKey {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return fmt.Errorf("read JSON: %w", err)
			}
			continue
		}
		if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
			return errors.New("items is not a list")
		}
		return eachElement(dec, fn)
	}
	return errors.New("no items list")
}

// eachObject reads a whole single object as one row.
func eachObject(f *os.File, fn func(map[string]any) error) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReader(f))
	dec.UseNumber()
	var row map[string]any
	if err := dec.Decode(&row); err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	return fn(row)
}

// sqlValue turns a decoded JSON value into a value SQLite stores.
func sqlValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case json.Number:
		if n, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	case string:
		return x
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// csvSource reads a CSV file with a header row. A column whose every non-empty cell is
// a number is stored as numbers, so SUM and ORDER BY work on it; the rest stays text.
type csvSource struct{ path string }

type colKind int

const (
	kindInt colKind = iota
	kindReal
	kindText
)

func (c csvSource) each(fn func(rec []string) error) error {
	f, err := os.Open(c.path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r := csv.NewReader(bufio.NewReader(f))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read CSV: %w", err)
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
}

func (c csvSource) columns() ([]string, error) {
	set := newColumnSet()
	first := true
	err := c.each(func(rec []string) error {
		if !first {
			return nil
		}
		first = false
		for i, h := range rec {
			name := strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))
			if name == "" {
				name = "col" + strconv.Itoa(i+1)
			}
			set.addPositional(name)
		}
		return nil
	})
	return set.names, err
}

// kinds finds each column's storage kind from every row after the header.
func (c csvSource) kinds(width int) ([]colKind, error) {
	kinds := make([]colKind, width)
	first := true
	err := c.each(func(rec []string) error {
		if first {
			first = false
			return nil
		}
		for i := 0; i < width && i < len(rec); i++ {
			kinds[i] = widen(kinds[i], rec[i])
		}
		return nil
	})
	return kinds, err
}

// widen returns the kind a column needs after seeing cell.
func widen(k colKind, cell string) colKind {
	switch {
	case cell == "" || k == kindText:
		return k
	case k == kindInt && intCell.MatchString(cell):
		return kindInt
	case realCell.MatchString(cell):
		return kindReal
	}
	return kindText
}

func (c csvSource) rows(cols []string, emit func([]any) error) error {
	kinds, err := c.kinds(len(cols))
	if err != nil {
		return err
	}
	first := true
	return c.each(func(rec []string) error {
		if first {
			first = false
			return nil
		}
		out := make([]any, len(cols))
		for i := range cols {
			if i < len(rec) {
				out[i] = csvValue(rec[i], kinds[i])
			}
		}
		return emit(out)
	})
}

// csvValue converts a cell to its column's kind; an empty number cell is NULL.
func csvValue(cell string, k colKind) any {
	if k == kindText {
		return cell
	}
	if cell == "" {
		return nil
	}
	if k == kindInt {
		if n, err := strconv.ParseInt(cell, 10, 64); err == nil {
			return n
		}
	}
	if f, err := strconv.ParseFloat(cell, 64); err == nil {
		return f
	}
	return cell
}
