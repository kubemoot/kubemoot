package workspace

import (
	"fmt"
	"strings"
)

// Render writes a result as plain text a small model reads easily: a header line,
// one line per row with cells separated by " | ", then a count line. A cut-off
// result says so and says how to get the rest.
func Render(res Result) string {
	var b strings.Builder
	b.WriteString(strings.Join(res.Columns, " | "))
	b.WriteByte('\n')
	for _, row := range res.Rows {
		for i, cell := range row {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(oneLine(cell))
		}
		b.WriteByte('\n')
	}
	if res.Truncated {
		fmt.Fprintf(&b, "[showing the first %d rows; the query has more. "+
			"Add WHERE or LIMIT, or use count(*) and GROUP BY]\n", len(res.Rows))
	} else {
		unit := "rows"
		if len(res.Rows) == 1 {
			unit = "row"
		}
		fmt.Fprintf(&b, "(%d %s)\n", len(res.Rows), unit)
	}
	return b.String()
}

// oneLine keeps a cell on its row by replacing line breaks with spaces.
func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}
