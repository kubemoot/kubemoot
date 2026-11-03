package workspace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A JSON result is flattened into plain rows before a query reads it, so a query needs only
// plain SQL (GROUP BY, MAX, AVG, ORDER BY) and never a JSON function:
//
//   - A list of objects is one row per object. A list of anything else keeps its old reading.
//   - An object that holds exactly one non-empty list of objects (a wrapper such as
//     {"data":[...]}, {"result":[...]}) is one row per element of that list. Every other field
//     of the wrapper is repeated on each row; when a row already has a column of that name,
//     the wrapper field is named root_<name>. An object with two or more such lists stays one
//     row, and a note names the lists.
//   - A nested object becomes columns named parent_child, up to maxFlattenDepth levels deep:
//     underscores keep every name a valid unquoted SQL identifier, which a dot would not.
//     Characters outside letters, digits and _ become _, and a name that would repeat gets
//     _2, _3 and so on.
//   - A list left inside a row (a list of numbers, a list of objects below the wrapper), an
//     empty object and an object deeper than the limit are kept as compact JSON text.
//   - Every row carries every column (NULL where an object lacked it). A column that holds
//     numbers in some rows and text or booleans in others is text in all of them, so both
//     engines read the column the same way.
//
// Not flattened, so they read as before: a Kubernetes List (an object with an "items" list), a
// list of Kubernetes objects (a cut List) and a single Kubernetes object, all of which keep
// metadata.name working as a struct in DuckDB and metadata->>'name' in SQLite; and a list of
// objects that only holds plain values (it already is a table).
const (
	flattenSep = "_"
	// maxFlattenDepth is how many levels of nested objects become columns.
	maxFlattenDepth = 4
	// flattenGrowth and flattenSlack bound the flattened file to flattenGrowth times the input
	// plus flattenSlack bytes. Repeated wrapper fields are what can make it larger; past the
	// bound the result is read unflattened and a note says so.
	flattenGrowth = 4
	flattenSlack  = 1 << 20
	// cellBytes is what a cell takes besides its column name and value: quotes, colon, comma and
	// the four letters of null.
	cellBytes = 12
	// writeBoundFactor loosens the bound on the written file, which is only a safety net: the
	// estimates above decide, so the runtime can make the same decision.
	writeBoundFactor = 3
	// wrapperPrefix names a wrapper field whose name a row column already uses.
	wrapperPrefix = "root"
	flatFileExt   = ".flat.json"
)

// flatNotes are the lines for the model about a flattened table, shown after the rows.
const (
	noteFlattened = "rows are flattened: a nested object is columns joined with _ (memory_total), " +
		"a list left inside a row is JSON text"
	noteTooLarge = "the result is read as it came because flattening would pass the size bound"
	noteTooWide  = "the result is read as it came because flattening would pass the column limit"
	noteUnread   = "the result is read as it came because it could not be flattened"
)

// flatTable is the flattened rows of one JSON document.
type flatTable struct {
	rows  []map[string]any
	notes []string
	// changed is false when the document reads as it is: it already was a plain table, or
	// flattening would pass a bound (a note then says so).
	changed bool
}

// flattener turns decoded JSON into rows.
type flattener struct {
	changed bool
}

// rowBuilder collects the cells of one row. Names compare without regard to case, as SQL does.
type rowBuilder struct {
	cells map[string]any
	lower map[string]bool
}

func newRow() *rowBuilder {
	return &rowBuilder{cells: map[string]any{}, lower: map[string]bool{}}
}

// put stores v under name, or under name_2, name_3 when the name is taken.
func (r *rowBuilder) put(name string, v any) {
	col := name
	for n := 2; r.lower[strings.ToLower(col)]; n++ {
		col = fmt.Sprintf("%s%s%d", name, flattenSep, n)
	}
	r.lower[strings.ToLower(col)] = true
	r.cells[col] = v
}

func (r *rowBuilder) has(name string) bool { return r.lower[strings.ToLower(name)] }

// sizeBound is the most bytes the flattened rows may take for a document of n bytes.
func sizeBound(n int) int64 { return int64(n)*flattenGrowth + flattenSlack }

// flattenDocument decodes doc and returns its flattened rows. A document that is not a list
// or an object of the kinds above comes back unchanged.
func flattenDocument(doc []byte) (flatTable, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return flatTable{}, fmt.Errorf("read JSON: %w", err)
	}
	f := &flattener{}
	limit := sizeBound(len(doc))
	switch x := root.(type) {
	case []any:
		return f.listTable(x, limit), nil
	case map[string]any:
		return f.objectTable(x, limit), nil
	}
	return flatTable{}, nil
}

// isKubernetesObject reports whether obj has the metadata of a Kubernetes object: a metadata
// object with a name, or a kind beside it. Those keep their nested fields, which models already
// read as metadata.name (DuckDB) or metadata ->> 'name' (SQLite).
func isKubernetesObject(obj map[string]any) bool {
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return false
	}
	_, named := meta["name"].(string)
	_, kind := obj["kind"].(string)
	return named || kind
}

// keepsShape reports whether an object is read as it is: a Kubernetes object, or a List with an
// "items" list.
func keepsShape(obj map[string]any) bool {
	_, items := obj[itemsKey].([]any)
	return items || isKubernetesObject(obj)
}

// startsWithKubernetesObject reports whether the first element of list is a Kubernetes object.
func startsWithKubernetesObject(list []any) bool {
	if len(list) == 0 {
		return false
	}
	first, ok := list[0].(map[string]any)
	return ok && isKubernetesObject(first)
}

// hasNestedObject reports whether obj has a field that is a non-empty object.
func hasNestedObject(obj map[string]any) bool {
	for _, v := range obj {
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			return true
		}
	}
	return false
}

// listTable is the table of a list. It builds rows only when an element has a nested object,
// since a list of plain objects already is a table.
func (f *flattener) listTable(list []any, limit int64) flatTable {
	if startsWithKubernetesObject(list) || !anyNested(list) {
		return flatTable{}
	}
	return f.finish(f.listRows(list, nil, nil), nil, limit)
}

func anyNested(list []any) bool {
	for _, e := range list {
		if obj, ok := e.(map[string]any); ok && hasNestedObject(obj) {
			return true
		}
	}
	return false
}

// objectTable is the table of an object: one row per element of its only list of objects, or one row.
func (f *flattener) objectTable(obj map[string]any, limit int64) flatTable {
	if keepsShape(obj) {
		return flatTable{}
	}
	lists := objectLists(obj)
	if len(lists) == 1 {
		return f.wrapperTable(obj, lists[0], limit)
	}
	notes := severalListsNote(lists)
	if !hasNestedObject(obj) {
		return flatTable{notes: notes}
	}
	row := newRow()
	f.object("", obj, 0, row)
	return f.finish([]*rowBuilder{row}, notes, limit)
}

// wrapperTable makes one row per element of obj[key], with the other fields of obj on each row.
func (f *flattener) wrapperTable(obj map[string]any, key string, limit int64) flatTable {
	elems, _ := obj[key].([]any)
	if startsWithKubernetesObject(elems) {
		return flatTable{}
	}
	siblings := newRow()
	var repeated int64
	for _, k := range sortedKeys(obj) {
		if k != key {
			f.value(columnName("", k), obj[k], 0, siblings)
			repeated += int64(len(jsonText(obj[k])))
		}
	}
	if repeated*int64(len(elems)-1) > limit {
		return flatTable{notes: []string{noteTooLarge}}
	}
	f.changed = true
	return f.finish(f.listRows(elems, siblings, sortedKeys(siblings.cells)), nil, limit)
}

// listRows makes one row per element; an element that is not an object is the cell "value". The
// wrapper's own fields (siblings, in the order of names) are repeated on each row.
func (f *flattener) listRows(list []any, siblings *rowBuilder, names []string) []*rowBuilder {
	rows := make([]*rowBuilder, 0, len(list))
	for _, e := range list {
		row := newRow()
		if obj, ok := e.(map[string]any); ok {
			f.object("", obj, 0, row)
		} else {
			f.value("value", e, 0, row)
		}
		addSiblings(row, siblings, names)
		row.lower = nil
		rows = append(rows, row)
	}
	return rows
}

// addSiblings repeats the wrapper's own fields on a row; one named like a column of the row is
// called root_<name>.
func addSiblings(row, siblings *rowBuilder, names []string) {
	for _, name := range names {
		col := name
		if row.has(col) {
			col = wrapperPrefix + flattenSep + name
		}
		row.put(col, siblings.cells[name])
	}
}

func severalListsNote(lists []string) []string {
	if len(lists) < 2 {
		return nil
	}
	return []string{fmt.Sprintf("the result holds %d lists of objects (%s) and is kept as one row; "+
		"each list is JSON text", len(lists), strings.Join(lists, ", "))}
}

// objectLists names the fields of obj that are a non-empty list whose elements are all objects.
func objectLists(obj map[string]any) []string {
	var keys []string
	for _, k := range sortedKeys(obj) {
		if isObjectList(obj[k]) {
			keys = append(keys, k)
		}
	}
	return keys
}

func isObjectList(v any) bool {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	for _, e := range list {
		if _, ok := e.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// object adds the fields of obj to row, below prefix.
func (f *flattener) object(prefix string, obj map[string]any, depth int, row *rowBuilder) {
	for _, k := range sortedKeys(obj) {
		f.value(columnName(prefix, k), obj[k], depth, row)
	}
}

// value adds one field: a nested object becomes columns, a list or a too deep object becomes JSON
// text, anything else is the cell.
func (f *flattener) value(name string, v any, depth int, row *rowBuilder) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) > 0 && depth < maxFlattenDepth {
			f.changed = true
			f.object(name, x, depth+1, row)
			return
		}
		row.put(name, jsonText(x))
	case []any:
		row.put(name, jsonText(x))
	default:
		row.put(name, v)
	}
}

// columnName joins a parent name and a key and keeps only letters, digits and _.
func columnName(prefix, key string) string {
	name := unsafeColumnChars.ReplaceAllString(key, flattenSep)
	switch {
	case name == "":
		name = "col"
	case name[0] >= '0' && name[0] <= '9' && prefix == "":
		name = flattenSep + name
	}
	if prefix == "" {
		return name
	}
	return prefix + flattenSep + name
}

// jsonText is v as compact JSON, without the HTML escapes json.Marshal adds.
func jsonText(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cellKind says how a column's values are stored: numbers, text or booleans.
type cellKind int

const (
	kindNone cellKind = iota
	kindNumber
	kindString
	kindBool
	kindMixed
)

func kindOf(v any) cellKind {
	switch v.(type) {
	case nil:
		return kindNone
	case json.Number:
		return kindNumber
	case bool:
		return kindBool
	}
	return kindString
}

// finish gives every row every column, unless that would pass the column or the size bound.
func (f *flattener) finish(rows []*rowBuilder, notes []string, limit int64) flatTable {
	if !f.changed || len(rows) == 0 {
		return flatTable{notes: notes}
	}
	alias, kinds := planColumns(rows)
	switch {
	case len(kinds) > maxColumns:
		return flatTable{notes: []string{noteTooWide}}
	case int64(len(rows))*rowOverhead(kinds) > limit:
		return flatTable{notes: []string{noteTooLarge}}
	}
	return flatTable{
		rows: unifyColumns(rows, alias, kinds), notes: append([]string{noteFlattened}, notes...), changed: true,
	}
}

// rowOverhead is what one flattened row takes besides its values: each column name, its quotes,
// the colon and comma, and the null of a cell the object lacked. The runtime computes the same sum.
func rowOverhead(kinds map[string]cellKind) int64 {
	var n int64
	for col := range kinds {
		n += int64(len(col)) + cellBytes
	}
	return n
}

// planColumns finds the columns of all rows and what kind of value each holds. Two spellings that
// differ only by case are different cells of a row but one name to SQL, so the later one is
// renamed name_2, name_3 as inside a row. alias maps each spelling to its column.
func planColumns(rows []*rowBuilder) (map[string]string, map[string]cellKind) {
	alias := map[string]string{}
	owner := map[string]string{} // lower-case column to the spelling that owns it
	kinds := map[string]cellKind{}
	for _, r := range rows {
		for _, name := range sortedKeys(r.cells) {
			col, seen := alias[name]
			if !seen {
				col = claimColumn(name, owner)
				alias[name] = col
			}
			kinds[col] = mergeKind(kinds[col], kindOf(r.cells[name]))
		}
		if len(kinds) > maxColumns {
			break
		}
	}
	return alias, kinds
}

// claimColumn returns the column a new spelling takes, and records it as the owner.
func claimColumn(name string, owner map[string]string) string {
	col := name
	for n := 2; owner[strings.ToLower(col)] != ""; n++ {
		col = fmt.Sprintf("%s%s%d", name, flattenSep, n)
	}
	owner[strings.ToLower(col)] = name
	return col
}

// unifyColumns gives every row every column and makes a column that mixes value kinds text.
func unifyColumns(rows []*rowBuilder, alias map[string]string, kinds map[string]cellKind) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		cells := make(map[string]any, len(kinds))
		for col := range kinds {
			cells[col] = nil
		}
		for name, v := range r.cells {
			col := alias[name]
			cells[col] = cellValue(v, kinds[col])
		}
		out[i] = cells
	}
	return out
}

func mergeKind(have, got cellKind) cellKind {
	if have == kindNone || got == kindNone || have == got {
		return max(have, got)
	}
	return kindMixed
}

// cellValue is v, or its text when its column mixes kinds.
func cellValue(v any, k cellKind) any {
	if v == nil || k != kindMixed {
		return v
	}
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return fmt.Sprint(v)
}

// flattenRelation writes the flattened rows of a JSON document under workDir and returns the
// relation that reads them. It returns ok false when the document reads as it is: not a list or
// object it flattens, already a plain table, flattening would pass the size or column bound, or
// the document could not be decoded. In that case rel holds only the notes to tell the model, if any.
func flattenRelation(name string, doc []byte, workDir string) (Relation, bool, error) {
	t, err := flattenDocument(doc)
	if err != nil {
		return Relation{Notes: []string{noteUnread}}, false, nil
	}
	if !t.changed {
		return Relation{Notes: t.notes}, false, nil
	}
	path := filepath.Join(workDir, name+flatFileExt)
	fit, err := writeFlat(path, t.rows, sizeBound(len(doc))*writeBoundFactor)
	if err != nil {
		return Relation{}, false, err
	}
	if !fit {
		_ = os.Remove(path)
		return Relation{Notes: []string{noteTooLarge}}, false, nil
	}
	return Relation{Name: name, Path: path, Kind: KindJSON, Notes: t.notes}, true, nil
}

// errOverBound stops a write that has passed its size bound.
type errOverBound struct{}

func (errOverBound) Error() string { return "over the size bound" }

// boundedWriter fails once more than limit bytes have been written.
type boundedWriter struct {
	w       io.Writer
	limit   int64
	written int64
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	b.written += int64(len(p))
	if b.written > b.limit {
		return 0, errOverBound{}
	}
	return b.w.Write(p)
}

// writeFlat saves rows as a JSON list and reports false when it would pass limit bytes.
func writeFlat(path string, rows []map[string]any, limit int64) (bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	bw := bufio.NewWriter(&boundedWriter{w: f, limit: limit})
	werr := encodeRows(bw, rows)
	if werr == nil {
		werr = bw.Flush()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if _, over := werr.(errOverBound); over {
		return false, nil
	}
	if werr != nil {
		return false, fmt.Errorf("write %s: %w", path, werr)
	}
	return true, nil
}

func encodeRows(w *bufio.Writer, rows []map[string]any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := w.WriteByte('['); err != nil {
		return err
	}
	for i, r := range rows {
		if i > 0 {
			if err := w.WriteByte(','); err != nil {
				return err
			}
		}
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return w.WriteByte(']')
}
