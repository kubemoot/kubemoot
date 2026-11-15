package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Describe tells what each named artifact is as a table: its columns, the type the query engine
// gives each, how many values are null, and a few example values, so a query is written against
// real names. The types and examples are read from the data, by the same engine and the same
// tables a query reads, so they are what a query sees. Nothing is invented: a meaning is given
// only for the columns Kubemoot adds itself (see knownColumn), and no unit is known.
const (
	// sampleRows is how many rows the examples are taken from.
	sampleRows = 50
	// maxExamples is the most example values shown for a column.
	maxExamples = 3
	// maxExampleChars is where one example value is cut.
	maxExampleChars = 40
	// maxDescribedColumns is the most columns one table is described by; the rest are counted.
	maxDescribedColumns = 200
	// maxTypesPerColumn bounds the type rows one column adds to the type query.
	maxTypesPerColumn = 6
	nullType          = "null"
	inferredNote      = "note: the types are the query engine's and the examples come from the first 50 rows; " +
		"both are read from the data. No unit or meaning is recorded for these results except where a column says one."
)

// columnInfo is what is known about one column of a table.
type columnInfo struct {
	name     string
	types    []string
	nulls    int64
	examples []string
}

// tableInfo is what Describe reports for one table.
type tableInfo struct {
	name    string
	rows    int64
	columns []columnInfo
	// omitted counts the columns past maxDescribedColumns.
	omitted int
}

// Describe returns the description of every table the named artifacts become, in name order.
func (s *Service) Describe(ctx context.Context, artifacts map[string]string) (string, error) {
	if err := checkTables(artifacts); err != nil {
		return "", err
	}
	return s.withRelations(ctx, artifacts, func(rels []Relation, notes []string) (string, error) {
		var b strings.Builder
		for _, r := range rels {
			t, err := s.describeRelation(ctx, r)
			if err != nil {
				return "", fmt.Errorf("table %q: %w", r.Name, err)
			}
			b.WriteString(t.render())
		}
		b.WriteString(renderNotes(notes))
		b.WriteString(inferredNote + "\n")
		return b.String(), nil
	})
}

func (s *Service) describeRelation(ctx context.Context, r Relation) (tableInfo, error) {
	lim := s.Limits
	lim.MaxRows = sampleRows
	first := "SELECT * FROM " + ident(r.Name) + " LIMIT " + strconv.Itoa(sampleRows)
	sample, err := s.Engine.Query(ctx, []Relation{r}, first, lim)
	if err != nil {
		return tableInfo{}, err
	}
	info := tableInfo{name: r.Name}
	names := sample.Columns
	if len(names) > maxDescribedColumns {
		info.omitted = len(names) - maxDescribedColumns
		names = names[:maxDescribedColumns]
	}
	if len(names) == 0 {
		return info, nil
	}
	lim.MaxRows = len(names) * maxTypesPerColumn
	typed, err := s.Engine.Query(ctx, []Relation{r}, typeQuery(r.Name, names), lim)
	if err != nil {
		return tableInfo{}, err
	}
	info.columns = columnsOf(names, typed.Rows, sample.Rows)
	info.rows = rowCount(typed.Rows)
	return info, nil
}

// typeQuery is one SELECT that gives, per column, each type its values have and how many.
func typeQuery(table string, columns []string) string {
	arms := make([]string, len(columns))
	for i, c := range columns {
		arms[i] = fmt.Sprintf("SELECT %s AS col, typeof(%s) AS ty, count(*) AS n FROM %s GROUP BY 2",
			literal(c), ident(c), ident(table))
	}
	return strings.Join(arms, "\nUNION ALL\n")
}

func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// typeCount is how many values of a column have one type.
type typeCount struct {
	name  string
	count int64
}

// columnsOf joins the type rows (column, type, count) and the sample rows into one description
// per column, in the table's column order.
func columnsOf(names []string, typeRows, sample [][]string) []columnInfo {
	byColumn := map[string][]typeCount{}
	for _, r := range typeRows {
		if len(r) == 3 {
			n, _ := strconv.ParseInt(r[2], 10, 64)
			byColumn[r[0]] = append(byColumn[r[0]], typeCount{strings.ToLower(r[1]), n})
		}
	}
	out := make([]columnInfo, len(names))
	for i, name := range names {
		types, nulls := splitTypes(byColumn[name])
		out[i] = columnInfo{name: name, types: types, nulls: nulls, examples: examplesOf(sample, i)}
		if jsonText(out[i].examples) && len(types) == 1 && isTextType(types[0]) {
			out[i].types = []string{"text (JSON)"}
		}
	}
	return out
}

// isTextType is true for the type name an engine gives a string column: SQLite's text, DuckDB's varchar.
func isTextType(name string) bool { return name == "text" || name == "varchar" }

// splitTypes orders a column's non-null types by how many values have each, and counts its nulls.
func splitTypes(counts []typeCount) ([]string, int64) {
	var nulls int64
	var kept []typeCount
	for _, c := range counts {
		if c.name == nullType {
			nulls += c.count
		} else {
			kept = append(kept, c)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].count > kept[j].count })
	types := make([]string, len(kept))
	for i, c := range kept {
		types[i] = cutText(c.name, maxExampleChars)
	}
	if len(types) == 0 {
		types = []string{nullType}
	}
	return types, nulls
}

// examplesOf is up to maxExamples distinct non-empty values of column i from the sample rows.
func examplesOf(sample [][]string, i int) []string {
	var out []string
	seen := map[string]bool{}
	for _, row := range sample {
		if len(out) == maxExamples {
			break
		}
		if i >= len(row) || row[i] == "" || seen[row[i]] {
			continue
		}
		seen[row[i]] = true
		out = append(out, row[i])
	}
	return out
}

// jsonText reports whether every example is a JSON object or list, so the column is nested data
// held as text.
func jsonText(examples []string) bool {
	for _, e := range examples {
		if t := strings.TrimSpace(e); t == "" || (t[0] != '{' && t[0] != '[') || !json.Valid([]byte(t)) {
			return false
		}
	}
	return len(examples) > 0
}

// rowCount is the number of rows, from the first column's type counts (every row has a value, or
// a null, in every column).
func rowCount(typeRows [][]string) int64 {
	if len(typeRows) == 0 || len(typeRows[0]) != 3 {
		return 0
	}
	first := typeRows[0][0]
	var n int64
	for _, r := range typeRows {
		if len(r) == 3 && r[0] == first {
			c, _ := strconv.ParseInt(r[2], 10, 64)
			n += c
		}
	}
	return n
}

func cutText(s string, n int) string {
	one := oneLine(s)
	if r := []rune(one); len(r) > n {
		return string(r[:n]) + "..."
	}
	return one
}

// knownColumn is what a column means, for the columns Kubemoot adds to a table of stacked calls.
// Any other column has no recorded meaning.
func knownColumn(name string) string {
	switch {
	case name == callColumn:
		return "the key of the stored call the row came from"
	case name == argsColumn:
		return "the arguments of that call, as JSON text"
	case strings.HasPrefix(name, argPrefix):
		return "the value of the argument " + strings.TrimPrefix(name, argPrefix) + " in that call"
	}
	return ""
}

func (t tableInfo) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[TABLE %s rows=%d columns=%d]\n", t.name, t.rows, len(t.columns)+t.omitted)
	for _, c := range t.columns {
		b.WriteString(c.line() + "\n")
	}
	if t.omitted > 0 {
		fmt.Fprintf(&b, "(%d more columns are not described)\n", t.omitted)
	}
	return b.String()
}

func (c columnInfo) line() string {
	parts := []string{c.name + ": " + strings.Join(c.types, " or ")}
	if c.nulls > 0 {
		parts = append(parts, strconv.FormatInt(c.nulls, 10)+" null")
	}
	if m := knownColumn(c.name); m != "" {
		parts = append(parts, "meaning: "+m)
	}
	if len(c.examples) > 0 {
		quoted := make([]string, len(c.examples))
		for i, e := range c.examples {
			quoted[i] = strconv.Quote(cutText(e, maxExampleChars))
		}
		parts = append(parts, "examples: "+strings.Join(quoted, ", "))
	}
	return "- " + strings.Join(parts, "; ")
}
