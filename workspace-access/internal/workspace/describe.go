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
// only for the columns Kubemoot adds itself (see knownColumn), and a unit is given only for a
// column read as a Kubernetes quantity.
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
	maxTypesPerColumn = 8
	nullType          = "null"
	textType          = "text"
	inferredNote      = "note: the types are the query engine's and the examples come from the first 50 rows; " +
		"both are read from the data. No unit or meaning is recorded for these results except where a column says one; " +
		"a column typed quantity holds numbers in the unit named, read from values such as 65Mi or 250m."
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
	// repeated counts the columns whose name an earlier column already has; they are not described again.
	repeated int
	// typesCut is true when the engine reported more types per column than the query asked for.
	typesCut bool
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
	names, repeated := distinctNames(sample.Columns)
	info.repeated = repeated
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
	info.typesCut = typed.Truncated
	info.columns = columnsOf(names, sample.Columns, typed.Rows, sample.Rows)
	markQuantities(info.columns, r.Quantities)
	info.rows = rowCount(names[0], typed.Rows)
	return info, nil
}

// distinctNames is the column names without a name that an earlier column has, and how many were
// left out. A query cannot tell two columns of one name apart, so the second is not described.
func distinctNames(columns []string) ([]string, int) {
	seen := make(map[string]bool, len(columns))
	out := make([]string, 0, len(columns))
	for _, c := range columns {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, len(columns) - len(out)
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
func columnsOf(names, sampleColumns []string, typeRows, sample [][]string) []columnInfo {
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
		examples := examplesOf(sample, indexOf(sampleColumns, name))
		out[i] = columnInfo{name: name, types: types, nulls: nulls, examples: examples}
		if jsonText(out[i].examples) && len(types) == 1 && isTextType(types[0]) {
			out[i].types = []string{"text (JSON)"}
		}
	}
	return out
}

// markQuantities says in a column's type that its numbers were read from Kubernetes quantities
// and in what unit.
func markQuantities(columns []columnInfo, units map[string]string) {
	for i, c := range columns {
		if unit, ok := units[c.name]; ok {
			columns[i].types = []string{"quantity, " + unit}
		}
	}
}

// isTextType is true for the type name an engine gives a string column: SQLite's text, DuckDB's varchar.
func isTextType(name string) bool { return name == textType || name == "varchar" }

// indexOf is the position of the first column called name.
func indexOf(columns []string, name string) int {
	for i, c := range columns {
		if c == name {
			return i
		}
	}
	return -1
}

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
		if i < 0 || i >= len(row) || row[i] == "" || seen[row[i]] {
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

// rowCount is the number of rows, from the type counts of the named column (every row has a
// value, or a null, in every column).
func rowCount(column string, typeRows [][]string) int64 {
	var n int64
	for _, r := range typeRows {
		if len(r) == 3 && r[0] == column {
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
	if t.repeated > 0 {
		fmt.Fprintf(&b, "(%d columns repeat the name of an earlier column and are not described again)\n", t.repeated)
	}
	if t.typesCut {
		b.WriteString("(some columns have more types than are shown)\n")
	}
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
