package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A tooler agent's contribution is the raw output of its tool calls, written by the
// agent runtime as one section per call: a "[toolname]" line, the result, then a
// blank line. A result longer than the runtime's cap is cut and ends with
// truncatedMarker. The artifact is that text, so a query reads it section by section.
const truncatedMarker = " ...[truncated]"

// sectionHeader is a line that is only a tool name in brackets.
var sectionHeader = regexp.MustCompile(`^\[([A-Za-z][A-Za-z0-9_.-]{0,63})\]$`)

// section is the output of one tool call.
type section struct {
	tool string
	body string
	// cut is true when the runtime's size cap ended the result.
	cut bool
	// partial is true when the cut fell inside a line, so the last row is incomplete.
	partial bool
}

// splitSections returns the sections of a tooler artifact. It reports false for any
// other artifact: the text must start with a header line, and each later header must
// follow a blank line.
func splitSections(data []byte) ([]section, bool) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var secs []section
	var body []string
	prevBlank := true
	for i, line := range lines {
		m := sectionHeader.FindStringSubmatch(line)
		switch {
		case m != nil && prevBlank:
			secs = closeSection(secs, body)
			secs = append(secs, section{tool: m[1]})
			body = body[:0]
		case len(secs) == 0 && strings.TrimSpace(line) != "":
			return nil, false
		case len(secs) > 0:
			body = append(body, lines[i])
		}
		prevBlank = strings.TrimSpace(line) == ""
	}
	secs = closeSection(secs, body)
	return secs, len(secs) > 0
}

// closeSection stores the collected lines as the body of the last section.
func closeSection(secs []section, body []string) []section {
	if len(secs) == 0 {
		return secs
	}
	last := &secs[len(secs)-1]
	text := strings.TrimRight(strings.Join(body, "\n"), " \t\r\n")
	if rest, ok := strings.CutSuffix(text, truncatedMarker); ok {
		last.cut = true
		last.partial = !strings.HasSuffix(rest, "\n")
		text = rest
	}
	last.body = strings.TrimRight(text, " \t\r\n")
	return secs
}

// cleanBody returns the bytes of a section ready to detect. A result cut inside a
// line ends in a partial row: a JSON list or items list keeps its complete elements,
// anything else loses the last line.
func cleanBody(s section) string {
	if !s.cut {
		return s.body
	}
	if t := strings.TrimSpace(s.body); strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{") {
		if out, ok := salvageJSON(t); ok {
			return out
		}
	}
	if !s.partial {
		return s.body
	}
	return dropPartialLine(s.body)
}

// dropPartialLine removes the last line of a body; a body of one line becomes empty.
func dropPartialLine(body string) string {
	i := strings.LastIndex(body, "\n")
	if i < 0 {
		return ""
	}
	return strings.TrimRight(body[:i], " \t\r\n")
}

// salvageJSON returns the complete elements of a cut JSON list, or of the "items"
// list of a cut object, as a JSON array.
func salvageJSON(text string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	tok, err := dec.Token()
	if err != nil {
		return "", false
	}
	switch tok {
	case json.Delim('['):
	case json.Delim('{'):
		if !seekItems(dec) {
			return "", false
		}
	default:
		return "", false
	}
	var elems []json.RawMessage
	for dec.More() {
		var e json.RawMessage
		if dec.Decode(&e) != nil {
			break
		}
		elems = append(elems, e)
	}
	if len(elems) == 0 {
		return "", false
	}
	out, err := json.Marshal(elems)
	return string(out), err == nil
}

// seekItems reads an open object up to the start of its "items" list.
func seekItems(dec *json.Decoder) bool {
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return false
		}
		if key == itemsKey {
			tok, err := dec.Token()
			return err == nil && tok == json.Delim('[')
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return false
		}
	}
	return false
}

// errAllText says every section is prose. Such an artifact is read as one text, so
// bracketed headings of a config file or a log stay in the lines.
var errAllText = errors.New("the tool sections are all text")

// allText reports whether no section is a table or a document.
func allText(items []prepared) bool {
	for _, p := range items {
		if p.format != FormatText {
			return false
		}
	}
	return true
}

// prepared is one section made into a relation.
type prepared struct {
	tool   string
	format Format
	rel    Relation
	sig    string
	cut    bool
}

// prepareSections makes the relations for a tooler artifact. Each section is
// detected and prepared on its own. Sections whose relations have the same shape
// (the same format and columns, as when a tool is called twice) are the rows of one
// table. The first shape takes the name the caller chose; each other shape takes
// name_2, name_3 and so on, and a note says what each table holds.
func prepareSections(name string, secs []section, workDir string, taken map[string]bool) ([]Relation, []string, error) {
	local := maps.Clone(taken)
	var items []prepared
	for i, s := range secs {
		p, ok, err := prepareSection(fmt.Sprintf("%s__section%d", name, i+1), s, workDir)
		if err != nil {
			return nil, nil, fmt.Errorf("section [%s]: %w", s.tool, err)
		}
		if ok {
			items = append(items, p)
		}
	}
	if len(items) == 0 {
		return nil, nil, errors.New("the tool sections are all empty")
	}
	if allText(items) {
		return nil, nil, errAllText
	}
	groups := groupByShape(items)
	rels := make([]Relation, 0, len(groups))
	var notes []string
	for i, g := range groups {
		table := tableForGroup(name, i, local)
		rel, err := mergeGroup(table, g, workDir)
		if err != nil {
			return nil, nil, err
		}
		rels = append(rels, rel)
		notes = append(notes, groupNotes(table, g, len(groups) > 1)...)
	}
	maps.Copy(taken, local)
	return rels, notes, nil
}

// prepareSection writes one section to a file and prepares it. An empty section is skipped.
func prepareSection(tmp string, s section, workDir string) (prepared, bool, error) {
	body := cleanBody(s)
	if strings.TrimSpace(body) == "" {
		return prepared{}, false, nil
	}
	src := filepath.Join(workDir, tmp+".txt")
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		return prepared{}, false, fmt.Errorf("write section: %w", err)
	}
	rel, format, err := Prepare(tmp, src, []byte(body), workDir)
	if err != nil {
		return prepared{}, false, err
	}
	sig, err := shapeOf(rel)
	if err != nil {
		return prepared{}, false, err
	}
	sig = string(format) + ":" + sig
	return prepared{tool: s.tool, format: format, rel: rel, sig: sig, cut: s.cut}, true, nil
}

// groupByShape collects sections with the same shape, in order of first appearance.
func groupByShape(items []prepared) [][]prepared {
	var groups [][]prepared
	index := map[string]int{}
	for _, p := range items {
		i, ok := index[p.sig]
		if !ok {
			i = len(groups)
			index[p.sig] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], p)
	}
	return groups
}

// tableForGroup names the table for the group at position i: the caller's name for
// the first, then name_2, name_3, skipping a name the caller already uses.
func tableForGroup(name string, i int, taken map[string]bool) string {
	if i == 0 {
		return name
	}
	for n := i + 1; ; n++ {
		candidate := fmt.Sprintf("%s_%d", name, n)
		if !taken[strings.ToLower(candidate)] {
			taken[strings.ToLower(candidate)] = true
			return candidate
		}
	}
}

// groupNotes tells the model what a table holds when it is not obvious: the tables
// of an artifact with several shapes, and a table that lost a cut last row.
func groupNotes(table string, g []prepared, several bool) []string {
	var notes []string
	if several || len(g) > 1 {
		tools := make([]string, len(g))
		for i, p := range g {
			tools[i] = p.tool
		}
		notes = append(notes, fmt.Sprintf("table %s holds the output of %s", table, strings.Join(tools, ", ")))
	}
	for _, p := range g {
		if p.cut {
			notes = append(notes, fmt.Sprintf("the output of %s was cut at the tool result size limit; "+
				"table %s may be missing rows", p.tool, table))
		}
	}
	return notes
}
