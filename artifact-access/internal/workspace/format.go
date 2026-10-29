package workspace

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Format is what an artifact's bytes turned out to be.
type Format string

// The formats a query can read. Anything else is read as lines of text.
const (
	FormatJSON          Format = "json"
	FormatMarkdownTable Format = "markdown-table"
	FormatKubectlTable  Format = "kubectl-table"
	FormatYAML          Format = "yaml"
	FormatCSV           Format = "csv"
	FormatText          Format = "text"
)

// itemsKey is the list a kubectl List object holds its objects in.
const itemsKey = "items"

const sniffLines = 5

var (
	// headerCell matches one kubectl column title; titles are separated by two or
	// more spaces and a title may hold single spaces ("NOMINATED NODE").
	headerCell    = regexp.MustCompile(`\S+(?: \S+)*`)
	headerTitle   = regexp.MustCompile(`^[A-Z][A-Z0-9 _/().-]*$`)
	mdSeparator   = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)
	yamlFirstLine = regexp.MustCompile(`^(---|- |[A-Za-z_][\w.-]*:(\s|$))`)
)

type detector struct {
	format Format
	match  func(data []byte, lines []string) bool
}

// detectors run in order; the first match wins. Text matches everything else.
var detectors = []detector{
	{FormatJSON, isJSON},
	{FormatMarkdownTable, isMarkdownTable},
	{FormatKubectlTable, isKubectlTable},
	{FormatYAML, isYAML},
	{FormatCSV, isCSV},
}

// Detect decides how to read an artifact from its content alone: object keys carry
// no file extension.
func Detect(data []byte) Format {
	lines := leadingLines(data, sniffLines)
	for _, d := range detectors {
		if len(lines) > 0 && d.match(data, lines) {
			return d.format
		}
	}
	return FormatText
}

// leadingLines returns up to n non-blank lines from the start of data.
func leadingLines(data []byte, n int) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimRight(line, " \t\r"); strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
		if len(out) == n {
			break
		}
	}
	return out
}

func isJSON(data []byte, _ []string) bool {
	t := bytes.TrimSpace(data)
	return len(t) > 0 && (t[0] == '{' || t[0] == '[') && json.Valid(t)
}

// isMarkdownTable looks for a pipe row followed by its separator row among the
// leading lines, so a table after a sentence or a heading is found.
func isMarkdownTable(_ []byte, lines []string) bool {
	for i := 0; i+1 < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "|") && mdSeparator.MatchString(strings.TrimSpace(lines[i+1])) {
			return true
		}
	}
	return false
}

func isKubectlTable(_ []byte, lines []string) bool {
	cells := headerCell.FindAllString(lines[0], -1)
	if len(cells) < 2 {
		return false
	}
	for _, c := range cells {
		if !headerTitle.MatchString(c) {
			return false
		}
	}
	return true
}

func isYAML(data []byte, lines []string) bool {
	if !yamlFirstLine.MatchString(lines[0]) {
		return false
	}
	_, err := yamlToJSON(data)
	return err == nil
}

func isCSV(_ []byte, lines []string) bool {
	if !strings.Contains(lines[0], ",") {
		return false
	}
	r := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	r.FieldsPerRecord = 0
	recs, err := r.ReadAll()
	return err == nil && len(recs) > 0 && len(recs[0]) >= 2
}

// preparers turn each format into a Relation, writing a converted copy into workDir
// when the engine cannot read the original.
var preparers = map[Format]func(name, src string, data []byte, workDir string) (Relation, error){
	FormatJSON:          prepareJSON,
	FormatCSV:           prepareCSV,
	FormatYAML:          prepareYAML,
	FormatMarkdownTable: prepareMarkdownTable,
	FormatKubectlTable:  prepareKubectlTable,
	FormatText:          prepareText,
}

// Prepare makes the relation a query reads for an artifact whose bytes are in data
// and whose materialized file is src. Converted copies are written under workDir.
func Prepare(name, src string, data []byte, workDir string) (Relation, Format, error) {
	format := Detect(data)
	rel, err := preparers[format](name, src, data, workDir)
	return rel, format, err
}

// PrepareArtifact makes the relations a query reads for one named artifact, with
// notes for the model on how it was read. An artifact made of a tooler's output
// sections (see splitSections) is read one section at a time; any other artifact is
// one relation. taken holds the lower-case table names the caller already uses, and
// receives the extra names this artifact takes.
func PrepareArtifact(name, src string, data []byte, workDir string,
	taken map[string]bool) ([]Relation, []string, error) {
	if secs, ok := splitSections(data); ok {
		rels, notes, err := prepareSections(name, secs, workDir, taken)
		if err == nil {
			return rels, notes, nil
		}
		if !errors.Is(err, errAllText) {
			rel, _, perr := Prepare(name, src, data, workDir)
			note := "the tool sections could not be read one by one (" + err.Error() + "); read as one text"
			return []Relation{rel}, []string{note}, perr
		}
	}
	rel, _, err := Prepare(name, src, data, workDir)
	return []Relation{rel}, nil, err
}

func prepareJSON(name, src string, data []byte, _ string) (Relation, error) {
	kind := KindJSON
	if hasItemsList(data) {
		kind = KindJSONItems
	}
	return Relation{Name: name, Path: src, Kind: kind}, nil
}

func prepareCSV(name, src string, _ []byte, _ string) (Relation, error) {
	return Relation{Name: name, Path: src, Kind: KindCSV}, nil
}

func prepareYAML(name, _ string, data []byte, workDir string) (Relation, error) {
	doc, err := yamlToJSON(data)
	if err != nil {
		return Relation{}, err
	}
	path := filepath.Join(workDir, name+".json")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		return Relation{}, fmt.Errorf("write %s: %w", name, err)
	}
	kind := KindJSON
	if hasItemsList(doc) {
		kind = KindJSONItems
	}
	return Relation{Name: name, Path: path, Kind: kind}, nil
}

func prepareMarkdownTable(name, _ string, data []byte, workDir string) (Relation, error) {
	return writeRows(name, workDir, parseMarkdownTable(string(data)))
}

func prepareKubectlTable(name, _ string, data []byte, workDir string) (Relation, error) {
	return writeRows(name, workDir, parseKubectlTable(string(data)))
}

func prepareText(name, _ string, data []byte, workDir string) (Relation, error) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	rows := make([][]string, 0, len(lines)+1)
	rows = append(rows, []string{"line_no", "line"})
	for i, line := range lines {
		rows = append(rows, []string{strconv.Itoa(i + 1), strings.TrimRight(line, "\r")})
	}
	return writeRows(name, workDir, rows)
}

// writeRows saves rows (the first is the header) as a CSV file the engine reads.
func writeRows(name, workDir string, rows [][]string) (Relation, error) {
	path := filepath.Join(workDir, name+".csv")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Relation{}, fmt.Errorf("write %s: %w", name, err)
	}
	werr := csv.NewWriter(f).WriteAll(rows)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return Relation{}, fmt.Errorf("write %s: %w", name, werr)
	}
	return Relation{Name: name, Path: path, Kind: KindCSV}, nil
}

// parseKubectlTable cuts each line at the column starts of the header, which is
// exact even when a cell is empty or holds spaces.
func parseKubectlTable(text string) [][]string {
	var rows [][]string
	var starts []int
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			continue
		}
		if starts == nil {
			idx := headerCell.FindAllStringIndex(line, -1)
			starts = make([]int, len(idx))
			for i, p := range idx {
				starts[i] = p[0]
			}
		}
		rows = append(rows, sliceAt(line, starts))
	}
	return rows
}

// sliceAt returns the cells of line between the given column start offsets.
func sliceAt(line string, starts []int) []string {
	cells := make([]string, len(starts))
	for i, s := range starts {
		end := len(line)
		if i+1 < len(starts) {
			end = min(starts[i+1], len(line))
		}
		if s < end {
			cells[i] = strings.TrimSpace(line[s:end])
		}
	}
	return cells
}

// parseMarkdownTable returns the header and body rows of the first pipe table and
// drops the separator row.
func parseMarkdownTable(text string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") {
			if len(rows) > 0 {
				break
			}
			continue
		}
		if mdSeparator.MatchString(t) {
			continue
		}
		t = strings.TrimSuffix(strings.TrimPrefix(t, "|"), "|")
		cells := strings.Split(t, "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, cells)
	}
	return rows
}

// yamlToJSON converts YAML to JSON: one document as itself, several as a list. A
// document that is only a scalar is not data to query and is refused.
func yamlToJSON(data []byte) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []any
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read YAML: %w", err)
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	if len(docs) == 0 {
		return nil, errors.New("YAML holds no document")
	}
	var out any = docs
	if len(docs) == 1 {
		out = docs[0]
	}
	switch out.(type) {
	case map[string]any, []any:
		return json.Marshal(out)
	}
	return nil, errors.New("YAML holds no mapping or list")
}

// hasItemsList reports whether data is a JSON object with a top-level "items" list,
// the shape of kubectl get -o json.
func hasItemsList(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return false
		}
		if key == itemsKey {
			return len(raw) > 0 && raw[0] == '['
		}
	}
	return false
}
