// Package colname gives table columns names a SQL query can use without quoting.
//
// A kubectl table keeps its titles (NODE SELECTOR, UP-TO-DATE); a model writes them as
// node_selector or NODE_SELECTOR and the query fails. Every place that shows or reads a
// column name of a tabular result uses Normalize, so the description in the discussion, the
// SQL table, the artifact tools and the error messages all agree.
//
// The same rule is implemented in agent-runtime (ai.kubemoot.agent.chat.ColumnNames); both
// test suites run the cases of test/fixtures/column-names.json.
package colname

import (
	"strconv"
	"strings"
)

// Normalize returns the SQL-safe name of every header, in order: ASCII lowercase letters,
// digits and underscore. Names that collide get _2, _3.
func Normalize(headers []string) []string {
	out := make([]string, len(headers))
	taken := make(map[string]bool, len(headers))
	for i, h := range headers {
		base := fallback(Name(h), i)
		name := base
		for n := 2; taken[name]; n++ {
			name = base + "_" + strconv.Itoa(n)
		}
		taken[name] = true
		out[i] = name
	}
	return out
}

// Name is one header cut down to lowercase ASCII letters, digits and single underscores. It
// can be empty, or start with a digit; Normalize gives those a fallback.
func Name(header string) string {
	var b strings.Builder
	sep := false
	for _, r := range header {
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A'
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
		default:
			sep = b.Len() > 0
			continue
		}
		if sep {
			b.WriteByte('_')
			sep = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fallback names a column whose cleaned name is empty (col<position>, position from 1) or
// starts with a digit (prefix c_).
func fallback(name string, index int) string {
	if name == "" {
		return "col" + strconv.Itoa(index+1)
	}
	if name[0] >= '0' && name[0] <= '9' {
		return "c_" + name
	}
	return name
}
