// Package readops provides bounded, streaming read-operations over an artifact's
// bytes - the "sip, don't slurp" primitives. Each op reads from an io.Reader and
// returns only a small slice, so a non-code agent (analyst, RAG, coordinator) can
// extract what it needs without the whole object entering its LLM context.
//
// Every accumulating op takes a maxBytes budget and STOPS building output once it is
// reached (bounded to at most one line past the budget), so a pathological artifact
// - many huge matching lines, a jq generator program - can never balloon memory.
// The line-oriented ops also stream a line at a time. Jq must parse its whole JSON
// input, so that input is separately capped.
//
// These functions are pure over an io.Reader and have no NATS dependency, so they
// are unit-tested directly; the MCP wiring that fetches the object lives in
// internal/mcpserver.
package readops

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/itchyny/gojq"
)

// maxLineBytes bounds a single line the scanner will accept (defensive against a
// pathological no-newline blob). 8 MiB is far beyond any real log/CSV line.
const maxLineBytes = 8 << 20

func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	return sc
}

// limitedBuilder accumulates newline-terminated output but stops once it reaches its
// byte budget, so a read-op never buffers more than ~one line past what the caller
// will accept. A maxBytes <= 0 means unbounded.
type limitedBuilder struct {
	b    strings.Builder
	max  int
	full bool
}

func newLimitedBuilder(maxBytes int) *limitedBuilder {
	return &limitedBuilder{max: maxBytes}
}

// line appends s + "\n" and reports whether the builder still has room. Once the
// budget is reached the builder is full and further lines are dropped.
func (l *limitedBuilder) line(s string) bool {
	if l.full {
		return false
	}
	l.b.WriteString(s)
	l.b.WriteByte('\n')
	if l.max > 0 && l.b.Len() >= l.max {
		l.full = true
	}
	return !l.full
}

func (l *limitedBuilder) String() string { return l.b.String() }

// Head returns the first n lines (newline-terminated), then a position line giving
// the artifact's total line count, so a reader knows how much remains. n <= 0
// yields "".
func Head(r io.Reader, n, maxBytes int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	return Rows(r, 0, n, maxBytes)
}

// position describes a page of lines: which lines it holds and how many the
// artifact has in all.
func position(start, emitted, total int) string {
	if emitted == 0 {
		return fmt.Sprintf("[no lines from line %d; the artifact has %d lines]\n", start, total)
	}
	return fmt.Sprintf("[lines %d-%d of %d]\n", start, start+emitted-1, total)
}

// Tail returns the last n lines. Memory is bounded to n lines via a ring buffer, and
// the rendered output is bounded by maxBytes.
func Tail(r io.Reader, n, maxBytes int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	sc := newScanner(r)
	ring := make([]string, n)
	count := 0
	for sc.Scan() {
		ring[count%n] = sc.Text()
		count++
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	size := n
	if count < n {
		size = count
	}
	out := newLimitedBuilder(maxBytes)
	for i := 0; i < size; i++ {
		if !out.line(ring[(count-size+i)%n]) {
			break
		}
	}
	return out.String(), nil
}

// Grep returns up to max lines matching the RE2 pattern, bounded by maxBytes.
// max <= 0 means unbounded by count (still bounded by maxBytes).
func Grep(r io.Reader, pattern string, max, maxBytes int) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("bad pattern: %w", err)
	}
	sc := newScanner(r)
	out := newLimitedBuilder(maxBytes)
	matched := 0
	for sc.Scan() {
		line := sc.Text()
		if !re.MatchString(line) {
			continue
		}
		if !out.line(line) {
			break
		}
		matched++
		if max > 0 && matched >= max {
			break
		}
	}
	return out.String(), sc.Err()
}

// Count returns the number of lines (rows) in the input. Its output is a single
// number, so it needs no byte budget.
func Count(r io.Reader) (int64, error) {
	sc := newScanner(r)
	var lines int64
	for sc.Scan() {
		lines++
	}
	return lines, sc.Err()
}

// CountData returns the number of DATA rows in the input: it skips blank lines,
// tool-section markers (a bracketed token like "[resources_list]" that a tooler
// emits before a table), and column-header rows (two or more tokens, every one
// all-uppercase - the kubectl / resources_list table shape). What remains is the
// entity count. A plain lowercase list with no header is counted in full. Markers
// and headers are skipped ANYWHERE, not just on the first line, because a tooler
// artifact can carry a leading "[tool]" marker before its header, and can repeat
// the marker+header per tool section. This is what "how many namespaces / pods /
// rows" means, versus a raw line count that over-reports by the marker, the header,
// and any blank line.
func CountData(r io.Reader) (int64, error) {
	sc := newScanner(r)
	var rows int64
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || isSectionMarker(line) || looksLikeHeader(line) {
			continue
		}
		rows++
	}
	return rows, sc.Err()
}

// isSectionMarker reports whether a line is a tool-section marker: a single
// bracketed token like "[resources_list]" with no inner brackets. A tooler emits
// it before a table; it is never a data row.
func isSectionMarker(line string) bool {
	return len(line) >= 2 && line[0] == '[' && line[len(line)-1] == ']' &&
		!strings.ContainsAny(line[1:len(line)-1], "[]")
}

// looksLikeHeader reports whether a line is a column-header row: two or more
// whitespace-separated tokens, every one all-uppercase (e.g.
// "APIVERSION KIND NAME STATUS AGE LABELS"). Data rows carry lowercase names and
// TitleCase kinds (e.g. "v1 Namespace arc-runners"), so they do not match.
func looksLikeHeader(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return false
	}
	for _, f := range fields {
		if !isUpperToken(f) {
			return false
		}
	}
	return true
}

// isUpperToken reports whether a token has at least one letter and every letter
// is uppercase. "APIVERSION" and "AGE" qualify; "v1" and "Namespace" do not.
func isUpperToken(s string) bool {
	hasLetter := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			hasLetter = true
			if !unicode.IsUpper(r) {
				return false
			}
		}
	}
	return hasLetter
}

// Rows returns the lines in [start, start+limit) (0-based), bounded by maxBytes,
// then a position line such as "[lines 0-99 of 412]". limit <= 0 means to EOF
// (still bounded by maxBytes). The whole artifact is scanned to count its lines.
func Rows(r io.Reader, start, limit, maxBytes int) (string, error) {
	if start < 0 {
		start = 0
	}
	sc := newScanner(r)
	out := newLimitedBuilder(maxBytes)
	idx, emitted, full := 0, 0, false
	for sc.Scan() {
		inPage := idx >= start && !full && (limit <= 0 || emitted < limit)
		if inPage {
			if out.line(sc.Text()) {
				emitted++
			} else {
				full = true
			}
		}
		idx++
	}
	return out.String() + position(start, emitted, idx), sc.Err()
}

// SelectCSV projects the named columns from CSV input that has a header row,
// preserving requested column order. A zero delimiter defaults to comma. Output is
// bounded by maxBytes (the row loop stops once the budget is reached).
func SelectCSV(r io.Reader, columns []string, delimiter rune, maxBytes int) (string, error) {
	if len(columns) == 0 {
		return "", fmt.Errorf("no columns requested")
	}
	cr := newCSVReader(r, delimiter)
	header, err := cr.Read()
	if err != nil {
		return "", fmt.Errorf("read header: %w", err)
	}
	idx, err := columnIndexes(header, columns)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	if delimiter != 0 {
		w.Comma = delimiter
	}
	if err := writeRow(w, columns); err != nil {
		return "", err
	}
	if err := copyProjectedRows(cr, w, &sb, idx, maxBytes); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func newCSVReader(r io.Reader, delimiter rune) *csv.Reader {
	cr := csv.NewReader(r)
	if delimiter != 0 {
		cr.Comma = delimiter
	}
	cr.FieldsPerRecord = -1 // tolerate ragged rows
	return cr
}

// copyProjectedRows writes the projected columns of each remaining row until EOF or
// the output reaches maxBytes (maxBytes <= 0 means unbounded).
func copyProjectedRows(cr *csv.Reader, w *csv.Writer, sb *strings.Builder, idx []int, maxBytes int) error {
	for maxBytes <= 0 || sb.Len() < maxBytes {
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := writeRow(w, project(rec, idx)); err != nil {
			return err
		}
	}
	return nil
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

func columnIndexes(header, columns []string) ([]int, error) {
	pos := make(map[string]int, len(header))
	for i, h := range header {
		pos[h] = i
	}
	idx := make([]int, len(columns))
	for j, c := range columns {
		i, ok := pos[c]
		if !ok {
			return nil, fmt.Errorf("column %q not found in header", c)
		}
		idx[j] = i
	}
	return idx, nil
}

// Jq runs the gojq program over JSON input and returns one JSON result per line.
// jq must parse the whole input value, so the input is capped at maxInputBytes;
// the result stream is separately bounded by maxBytes (a generator program cannot
// balloon memory). Beyond either cap the caller should use the compute sandbox.
func Jq(r io.Reader, program string, maxInputBytes int64, maxBytes int) (string, error) {
	q, err := gojq.Parse(program)
	if err != nil {
		return "", fmt.Errorf("parse jq: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxInputBytes {
		return "", fmt.Errorf("input exceeds jq cap of %d bytes; use a compute sandbox", maxInputBytes)
	}
	var input any
	if err := json.Unmarshal(data, &input); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}
	out := newLimitedBuilder(maxBytes)
	iter := q.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			return "", fmt.Errorf("jq: %w", err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		if !out.line(string(b)) {
			break
		}
	}
	return out.String(), nil
}
