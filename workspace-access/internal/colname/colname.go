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

const (
	// MaxListed is the most names a column list shows by default.
	MaxListed = 60
	// maxNameChars is the most characters of one name in a list; a longer name is cut and marked.
	maxNameChars = 60
	// maxClosest is the most close names an error offers.
	maxClosest = 3
	// minPartChars is the shortest word two names must share to be close.
	minPartChars = 3
)

// brackets turns square brackets into parentheses so a name cannot end a [CALL ...] line.
var brackets = strings.NewReplacer("[", "(", "]", ")")

// List is the column list shown on a [CALL ...] line and in a no such column error: the names
// joined by commas, at most MaxListed of them, then ",+<n>more" for the rest.
func List(names []string) string {
	return ListMax(names, MaxListed)
}

// ListMax is List with its own limit. The same rule runs in agent-runtime
// (ColumnNames.list); both test suites run test/fixtures/column-list.json.
func ListMax(names []string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	shown := names[:min(len(names), limit)]
	parts := make([]string, len(shown))
	for i, n := range shown {
		n = brackets.Replace(n)
		if r := []rune(n); len(r) > maxNameChars {
			n = string(r[:maxNameChars]) + "..."
		}
		parts[i] = n
	}
	out := strings.Join(parts, ",")
	if rest := len(names) - len(shown); rest > 0 {
		if out != "" {
			out += ","
		}
		out += "+" + strconv.Itoa(rest) + "more"
	}
	return out
}

// Closest names the columns that share a word of at least three letters with a name the query
// guessed (status.phase and the column status; involved_object_name and name), in the order of
// the table, at most three. It only advises: a guess is never turned into a column.
func Closest(guess string, columns []string) []string {
	words := wordSet(Name(guess))
	var out []string
	for _, c := range columns {
		if len(out) == maxClosest {
			break
		}
		for w := range wordSet(c) {
			if words[w] {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// wordSet is the words of a name (split at underscores) that are long enough to mean something.
func wordSet(name string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Split(name, "_") {
		if len(w) >= minPartChars {
			set[w] = true
		}
	}
	return set
}

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
