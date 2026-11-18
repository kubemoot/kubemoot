package workspace

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/kubemoot/kubemoot/artifact-access/internal/colname"
)

// tableColumns is a loaded table and its column names, in the order the table has them.
type tableColumns struct {
	table   string
	columns []string
}

// missingColumn finds the name in a "no such column: <name>" error.
var missingColumn = regexp.MustCompile(`no such column: ([^\s)]+)`)

// withColumns adds the loaded tables and their columns to an error that names a column the
// tables do not have, so the next query can use a real name instead of a guess. The list has
// the shape of the columns= list on a [CALL ...] line (colname.List). When the guess shares a
// word with real columns they are named, and a table that is plain text lines says so. Any
// other error is returned as it is.
func withColumns(err error, schema []tableColumns) error {
	if err == nil || !strings.Contains(err.Error(), "no such column") || len(schema) == 0 {
		return err
	}
	parts := make([]string, 0, len(schema))
	for _, t := range schema {
		parts = append(parts, t.table+" ("+colname.List(t.columns)+")")
	}
	msg := err.Error() + "; the columns are: " + strings.Join(parts, "; ")
	if text := textTables(schema); len(text) > 0 {
		msg += "; " + strings.Join(text, ", ") + " read as plain text lines, not as a table of fields: " +
			"filter the line column with LIKE, or describe the artifact to see how it was read"
	}
	if m := missingColumn.FindStringSubmatch(err.Error()); m != nil {
		if near := closestColumns(m[1], schema); len(near) > 0 {
			msg += "; closest to " + m[1] + ": " + strings.Join(near, ", ")
		}
	}
	return errors.New(msg)
}

// textTables names the tables whose only columns are the line number and the line.
func textTables(schema []tableColumns) []string {
	var out []string
	for _, t := range schema {
		if slices.Equal(t.columns, []string{lineNoColumn, lineColumn}) {
			out = append(out, t.table)
		}
	}
	return out
}

// closestColumns names the columns, as table.column, that share a word with the guess.
func closestColumns(guess string, schema []tableColumns) []string {
	var out []string
	for _, t := range schema {
		for _, c := range colname.Closest(strings.TrimPrefix(guess, t.table+"."), t.columns) {
			out = append(out, t.table+"."+c)
		}
	}
	return out
}
