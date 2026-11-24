package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
)

// withNestedHint adds, to an error that names a column the tables lack, the way into a column that
// holds a nested object when the guess reads like a path inside it (status.conditions,
// involved_object_name for the column InvolvedObject). A query that guesses such a path fails again
// and again on the list of real columns alone, because the column it needs is there under a shorter
// name. The hint names that column and the SQLite form that reads the field. Any other error, and
// a guess that matches no column holding JSON, is returned as it is.
func withNestedHint(ctx context.Context, conn *sql.Conn, err error, schema []tableColumns) error {
	if err == nil || !strings.Contains(err.Error(), "no such column") {
		return err
	}
	m := missingColumn.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	for _, t := range schema {
		for _, c := range t.columns {
			path, ok := pathInside(m[1], c)
			if ok && holdsJSON(ctx, conn, t.table, c) {
				return errors.New(err.Error() + "; " + c + " of " + t.table + " holds a nested object or list as text, " +
					"so read inside it with " + quoteIdent(c) + "->>'$." + path + "' (a list: json_each(" +
					quoteIdent(c) + ", '$." + path + "') or json_array_length)")
			}
		}
	}
	return err
}

// pathInside reads guess as a path into column: the rest of the guess after the column's name,
// compared without case, underscores or dots. status.conditions.type for status is conditions.type;
// involved_object_name for InvolvedObject is name. The second result is false when the guess is
// the column itself or does not start with it (a table or alias before the column is skipped).
func pathInside(guess, column string) (string, bool) {
	want := squash(column)
	if len(want) < 3 {
		return "", false
	}
	segments := strings.FieldsFunc(guess, func(r rune) bool { return r == '.' })
	for from := range segments {
		if path, ok := pathAfter(strings.Join(segments[from:], "."), want); ok {
			return path, true
		}
	}
	return "", false
}

// pathAfter is the rest of text after the characters that spell want, ignoring case and the
// separators, with the remaining words joined by dots; false when text does not start with want
// or has nothing after it.
func pathAfter(text, want string) (string, bool) {
	matched := 0
	for i, r := range text {
		if matched == len(want) {
			if !startsWord(text[i:]) {
				return "", false
			}
			rest := strings.Trim(text[i:], "._")
			return strings.ReplaceAll(rest, "_", "."), rest != ""
		}
		if r == '_' || r == '.' {
			continue
		}
		if unicode.ToLower(r) != rune(want[matched]) {
			return "", false
		}
		matched++
	}
	return "", false
}

// startsWord is true when text begins at a word boundary: a separator or a capital letter, so
// the column name is a whole word of the guess (name is in name_x, and not in namespace).
func startsWord(text string) bool {
	r := []rune(text)[0]
	return r == '.' || r == '_' || unicode.IsUpper(r)
}

// squash lowercases a name and drops everything but letters and digits.
func squash(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// holdsJSON reports whether a value of the column starts like a JSON object or list.
func holdsJSON(ctx context.Context, conn *sql.Conn, table, column string) bool {
	q := "SELECT 1 FROM " + quoteIdent(table) + " WHERE " + quoteIdent(column) +
		" LIKE '{%' OR " + quoteIdent(column) + " LIKE '[%' LIMIT 1"
	var one int
	return conn.QueryRowContext(ctx, q).Scan(&one) == nil
}
