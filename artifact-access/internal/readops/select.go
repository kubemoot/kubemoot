package readops

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kubemoot/kubemoot/artifact-access/internal/colname"
	"github.com/kubemoot/kubemoot/artifact-access/internal/workspace"
)

var errEmptyArtifact = errors.New("artifact is empty; it has no header row")

// SelectCSV projects the named columns from a table artifact, preserving requested column
// order, and writes them as CSV with SQL-safe headers. The table is a kubectl table, a Markdown
// table or CSV, found and parsed by the code workspace_query uses (workspace.ReadTable). Columns
// are matched by their SQL-safe name, so the printed title and any case also match. A
// delimiter other than comma says the artifact is delimited text. Output is bounded by maxBytes
// (the row loop stops once the budget is reached).
func SelectCSV(r io.Reader, columns []string, delimiter rune, maxBytes int) (string, error) {
	if len(columns) == 0 {
		return "", fmt.Errorf("no columns requested")
	}
	data, err := io.ReadAll(io.LimitReader(r, workspace.DefaultMaxArtifactBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > workspace.DefaultMaxArtifactBytes {
		return "", fmt.Errorf("artifact exceeds %d bytes; use artifact_grep or artifact_rows",
			workspace.DefaultMaxArtifactBytes)
	}
	format, rows, err := workspace.ReadTable(data, delimiter)
	if err != nil {
		return "", tableError(format, data, err)
	}
	if len(rows) == 0 {
		return "", errEmptyArtifact
	}
	request := normalizedRequest(columns)
	idx, err := columnIndexes(rows[0], request)
	if err != nil {
		return "", err
	}
	return projectRows(rows, request, idx, delimiter, maxBytes)
}

// tableError says why an artifact cannot be selected from and which tool fits it.
func tableError(format workspace.Format, data []byte, err error) error {
	if !errors.Is(err, workspace.ErrNotTable) {
		return fmt.Errorf("read table: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errEmptyArtifact
	}
	use := "artifact_grep or artifact_rows"
	if format == workspace.FormatJSON {
		use = "artifact_jq"
	}
	return fmt.Errorf("artifact is %s, not a table (kubectl table, Markdown table or CSV); use %s",
		format, use)
}

// projectRows writes the header and then the chosen columns of the body rows as CSV.
func projectRows(rows [][]string, header []string, idx []int, delimiter rune, maxBytes int) (string, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	if delimiter != 0 {
		w.Comma = delimiter
	}
	if err := writeRow(w, header); err != nil {
		return "", err
	}
	for _, rec := range rows[1:] {
		if maxBytes > 0 && sb.Len() >= maxBytes {
			break
		}
		if err := writeRow(w, project(rec, idx)); err != nil {
			return "", err
		}
	}
	return sb.String(), nil
}

// normalizedRequest gives each requested column its SQL-safe name; unlike a header, a request
// is not renumbered or de-duplicated, because the same column may be asked for twice.
func normalizedRequest(columns []string) []string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = colname.Name(c)
	}
	return out
}

// writeRow writes one CSV record and flushes so the caller can observe the backing
// builder's length (csv.Writer buffers internally otherwise).
func writeRow(w *csv.Writer, rec []string) error {
	if err := w.Write(rec); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

func project(rec []string, idx []int) []string {
	out := make([]string, len(idx))
	for j, i := range idx {
		if i < len(rec) {
			out[j] = rec[i]
		}
	}
	return out
}

// columnIndexes finds each requested (SQL-safe) name in the header. A miss lists the columns
// the table has, as workspace_query's no such column error does.
func columnIndexes(header, columns []string) ([]int, error) {
	pos := make(map[string]int, len(header))
	for i, h := range header {
		pos[h] = i
	}
	idx := make([]int, len(columns))
	for j, c := range columns {
		i, ok := pos[c]
		if !ok {
			return nil, fmt.Errorf("column %q not found; the columns are: %s", c, strings.Join(header, ", "))
		}
		idx[j] = i
	}
	return idx, nil
}
