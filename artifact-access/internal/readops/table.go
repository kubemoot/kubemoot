package readops

import (
	"regexp"
	"strings"
	"unicode"
)

// This file is the one definition of a table artifact: the kubectl-style output the
// Kubernetes tools store. CountData, Select, and Jq's error messages all use it, so
// they agree on what is a marker, what is a header, and how a row splits.
//
// A table is an optional "[tool]" marker line, a header row, and data rows. The header
// is recognized by its shape: columns separated by a tab or by two or more spaces, or
// two or more all-uppercase tokens ("NAME STATUS"). Rows split the same way as the
// header. A row with fewer cells than the header (a blank cell) is cut at the header's
// column offsets when the header is space-aligned.

// tableSplit separates space-aligned columns: a run of two or more spaces, so a single
// space stays inside a cell ("Not Ready").
var tableSplit = regexp.MustCompile(` {2,}`)

type splitMode int

const (
	splitSpaces splitMode = iota // columns aligned with runs of two or more spaces
	splitTabs                    // columns separated by exactly one tab each
	splitFields                  // single-space header: whitespace-separated tokens
)

// tableShape is a parsed table header.
type tableShape struct {
	header  []string
	mode    splitMode
	offsets []int // start offset of each header column; set for splitSpaces
}

// skippable reports whether a line is blank or a tool-section marker, neither of
// which is ever data or a header.
func skippable(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || isSectionMarker(t)
}

// isSectionMarker reports whether a line is a tool-section marker: a single
// bracketed identifier like "[resources_list]". A tooler emits it before a table;
// it is never a data row. A one-line JSON array such as [true] or ["a"] is not one.
func isSectionMarker(line string) bool {
	if len(line) < 3 || line[0] != '[' || line[len(line)-1] != ']' {
		return false
	}
	return isMarkerName(line[1 : len(line)-1])
}

// isMarkerName reports whether text is a tool identifier: it starts with a letter and
// holds letters, digits, and _ . : / - only, and is not a JSON literal.
func isMarkerName(name string) bool {
	switch name {
	case "true", "false", "null":
		return false
	}
	for i, r := range name {
		letter := unicode.IsLetter(r)
		if i == 0 && !letter {
			return false
		}
		if !letter && !unicode.IsDigit(r) && !strings.ContainsRune("_.:/-", r) {
			return false
		}
	}
	return true
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

// parseTableHeader reads a header line. It reports false when the line has a single
// column, so CSV with one column and plain text are not mistaken for tables.
func parseTableHeader(raw string) (*tableShape, bool) {
	line := strings.TrimRight(raw, " \t\r")
	switch {
	case strings.Contains(line, "\t"):
		cells := splitTabs1(line)
		return &tableShape{header: cells, mode: splitTabs}, len(cells) >= 2
	case tableSplit.MatchString(strings.TrimSpace(line)):
		lead := len(line) - len(strings.TrimLeft(line, " "))
		cells := tableSplit.Split(strings.TrimSpace(line), -1)
		return &tableShape{header: cells, mode: splitSpaces, offsets: columnOffsets(line, lead)}, true
	case looksLikeHeader(line):
		return &tableShape{header: strings.Fields(line), mode: splitFields}, true
	}
	return nil, false
}

func splitTabs1(line string) []string {
	cells := strings.Split(line, "\t")
	for i, c := range cells {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}

// columnOffsets returns where each space-aligned column starts in the header line.
func columnOffsets(line string, lead int) []int {
	gaps := tableSplit.FindAllStringIndex(line[lead:], -1)
	offsets := make([]int, 1, len(gaps)+1)
	offsets[0] = lead
	for _, m := range gaps {
		offsets = append(offsets, lead+m[1])
	}
	return offsets
}

// row splits one data line into cells in the header's columns.
func (t *tableShape) row(raw string) []string {
	line := strings.TrimRight(raw, "\r")
	switch t.mode {
	case splitTabs:
		return splitTabs1(line)
	case splitFields:
		return t.fieldsRow(line)
	}
	cells := tableSplit.Split(strings.TrimSpace(line), -1)
	if len(cells) < len(t.header) {
		return t.sliceAtOffsets(line)
	}
	return cells
}

// fieldsRow splits on whitespace; extra tokens join the last column, so a value with
// spaces in the final column stays whole.
func (t *tableShape) fieldsRow(line string) []string {
	f := strings.Fields(line)
	if n := len(t.header); len(f) > n {
		f = append(f[:n-1], strings.Join(f[n-1:], " "))
	}
	return f
}

// sliceAtOffsets cuts a row at the header's column starts, so a blank cell keeps the
// cells after it in their columns.
func (t *tableShape) sliceAtOffsets(line string) []string {
	cells := make([]string, len(t.offsets))
	for i, start := range t.offsets {
		end := len(line)
		if i+1 < len(t.offsets) && t.offsets[i+1] < end {
			end = t.offsets[i+1]
		}
		if start < end {
			cells[i] = strings.TrimSpace(line[start:end])
		}
	}
	return cells
}
