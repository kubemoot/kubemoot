package readops

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// sniffBytes is how much of the artifact Select looks at to tell a table from CSV.
const sniffBytes = 64 * 1024

// maxListedColumns bounds how many header columns an error lists, so a header that
// is really a long line of prose cannot flood the model's context.
const maxListedColumns = 20

var (
	errNoColumns = errors.New("no columns requested")
	errEmpty     = errors.New("the artifact is empty")
)

// Select projects the named columns from a tabular artifact with a header row and
// returns them as CSV. With no delimiter it detects the shape from the header: a
// header with a comma or a quote is CSV; a header with two or more columns (tabs, runs
// of two or more spaces, or uppercase tokens) is a kubectl-style table, which is what
// the Kubernetes tools store; anything else is single-column CSV. A delimiter forces
// CSV with that separator. Column names match case-insensitively. Output is bounded
// by maxBytes.
func Select(r io.Reader, columns []string, delimiter rune, maxBytes int) (string, error) {
	if len(columns) == 0 {
		return "", errNoColumns
	}
	if delimiter != 0 {
		return SelectCSV(r, columns, delimiter, maxBytes)
	}
	br := bufio.NewReaderSize(r, sniffBytes)
	head, _ := br.Peek(sniffBytes)
	line, ok := firstDataLine(head)
	if !ok {
		return "", errEmpty
	}
	if shape, isTable := detectTable(line); isTable {
		return selectTable(br, shape, columns, maxBytes)
	}
	return SelectCSV(br, columns, 0, maxBytes)
}

// detectTable parses a first line as a table header unless it looks like CSV.
func detectTable(line string) (*tableShape, bool) {
	if strings.ContainsAny(line, ",\"") {
		return nil, false
	}
	return parseTableHeader(line)
}

// firstDataLine returns the first line that is neither blank nor a tool-section
// marker such as "[resources_list]", without its trailing line break.
func firstDataLine(b []byte) (string, bool) {
	for _, l := range bytes.Split(b, []byte("\n")) {
		line := strings.TrimRight(string(l), "\r")
		if !skippable(line) {
			return line, true
		}
	}
	return "", false
}

// rowSink writes CSV records into a builder and reports when the byte budget is hit.
type rowSink struct {
	sb  strings.Builder
	w   *csv.Writer
	max int
}

func newRowSink(delimiter rune, maxBytes int) *rowSink {
	s := &rowSink{max: maxBytes}
	s.w = csv.NewWriter(&s.sb)
	if delimiter != 0 {
		s.w.Comma = delimiter
	}
	return s
}

// full reports whether the output reached its budget (maxBytes <= 0 is unbounded).
func (s *rowSink) full() bool { return s.max > 0 && s.sb.Len() >= s.max }

// write appends one record and flushes so full() sees the true length.
func (s *rowSink) write(rec []string) error {
	if err := s.w.Write(rec); err != nil {
		return err
	}
	s.w.Flush()
	return s.w.Error()
}

// selectTable projects columns from a table whose header is the first data line,
// skipping blank lines, tool-section markers, and a header repeated for a later
// section.
func selectTable(r io.Reader, shape *tableShape, columns []string, maxBytes int) (string, error) {
	idx, err := columnIndexes(shape.header, columns)
	if err != nil {
		return "", err
	}
	sink := newRowSink(0, maxBytes)
	if err := sink.write(project(shape.header, idx)); err != nil {
		return "", err
	}
	sc := newScanner(r)
	headerSeen := false
	var headerLine string
	for sc.Scan() && !sink.full() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if skippable(line) || (headerSeen && line == headerLine) {
			continue
		}
		if !headerSeen {
			headerSeen, headerLine = true, line
			continue
		}
		if err := sink.write(project(shape.row(line), idx)); err != nil {
			return "", err
		}
	}
	return sink.sb.String(), sc.Err()
}

// SelectCSV projects the named columns from CSV input that has a header row,
// preserving requested column order. A zero delimiter defaults to comma. Output is
// bounded by maxBytes (the row loop stops once the budget is reached).
func SelectCSV(r io.Reader, columns []string, delimiter rune, maxBytes int) (string, error) {
	if len(columns) == 0 {
		return "", errNoColumns
	}
	cr := csv.NewReader(r)
	if delimiter != 0 {
		cr.Comma = delimiter
	}
	cr.FieldsPerRecord = -1 // tolerate ragged rows
	cr.TrimLeadingSpace = delimiter == 0 || !unicode.IsSpace(delimiter)
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return "", errEmpty
	}
	if err != nil {
		return "", fmt.Errorf("read header: %w", err)
	}
	for i, h := range header {
		header[i] = strings.TrimSpace(h)
	}
	idx, err := columnIndexes(header, columns)
	if err != nil {
		return "", err
	}
	sink := newRowSink(delimiter, maxBytes)
	if err := sink.write(project(header, idx)); err != nil {
		return "", err
	}
	if err := copyCSVRows(cr, sink, idx); err != nil {
		return "", err
	}
	return sink.sb.String(), nil
}

// copyCSVRows writes the projected columns of each remaining row until EOF or the
// sink is full.
func copyCSVRows(cr *csv.Reader, sink *rowSink, idx []int) error {
	for !sink.full() {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := sink.write(project(rec, idx)); err != nil {
			return err
		}
	}
	return nil
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

// columnIndexes maps requested names to header positions: an exact match wins, then a
// case-insensitive one. A miss lists the header's columns so the model can retry.
func columnIndexes(header, columns []string) ([]int, error) {
	idx := make([]int, len(columns))
	for j, c := range columns {
		i := findColumn(header, c)
		if i < 0 {
			return nil, fmt.Errorf("column %q not found; the header has %s; "+
				"for a JSON or YAML artifact use artifact_jq, for plain text use artifact_grep or artifact_rows",
				c, describeHeader(header))
		}
		idx[j] = i
	}
	return idx, nil
}

func findColumn(header []string, name string) int {
	for i, h := range header {
		if h == name {
			return i
		}
	}
	for i, h := range header {
		if strings.EqualFold(h, name) {
			return i
		}
	}
	return -1
}

func describeHeader(header []string) string {
	shown := header
	more := ""
	if len(shown) > maxListedColumns {
		shown, more = shown[:maxListedColumns], ", ..."
	}
	quoted := make([]string, len(shown))
	for i, h := range shown {
		quoted[i] = fmt.Sprintf("%q", truncate(h, 40))
	}
	return fmt.Sprintf("%d columns: %s%s", len(header), strings.Join(quoted, ", "), more)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
