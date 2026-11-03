package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// flattenCase is one line of testdata/flatten_cases.json. The agent runtime's Java test reads the
// same file, so the row count and column names the runtime puts in the call catalog stay the
// ones this package builds.
type flattenCase struct {
	Name    string   `json:"name"`
	Input   string   `json:"input"`
	Rows    int      `json:"rows"`
	Columns []string `json:"columns"`
}

func loadFlattenCases(t *testing.T) []flattenCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/flatten_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []flattenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// tableOf prepares a JSON document as the query service does and returns its row count and
// sorted column names.
func tableOf(t *testing.T, doc string) (int, []string) {
	t.Helper()
	dir := t.TempDir()
	src := dir + "/src.json"
	if err := os.WriteFile(src, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, _, err := PrepareAs(FormatJSON, "t", src, []byte(doc), dir)
	if err != nil {
		t.Fatal(err)
	}
	return readTable(t, rel)
}

func readTable(t *testing.T, rel Relation) (int, []string) {
	t.Helper()
	if rel.Kind == KindCSV {
		cols, err := csvSource{path: rel.Path}.columns()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		_ = csvSource{path: rel.Path}.each(func([]string) error { n++; return nil })
		sort.Strings(cols)
		return n - 1, cols
	}
	src := &jsonSource{path: rel.Path, items: rel.Kind == KindJSONItems}
	rows := 0
	cols := map[string]bool{}
	err := src.each(func(row map[string]any) error {
		rows++
		for k := range row {
			cols[k] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(cols))
	for c := range cols {
		names = append(names, c)
	}
	sort.Strings(names)
	return rows, names
}

func TestFlattenCases(t *testing.T) {
	for _, tc := range loadFlattenCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			rows, cols := tableOf(t, tc.Input)
			if rows != tc.Rows || strings.Join(cols, ",") != strings.Join(tc.Columns, ",") {
				t.Fatalf("got %d rows %v, want %d rows %v", rows, cols, tc.Rows, tc.Columns)
			}
		})
	}
}

func flatRows(t *testing.T, doc string) []map[string]any {
	t.Helper()
	tbl, err := flattenDocument([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !tbl.changed {
		t.Fatalf("%s was not flattened", doc)
	}
	return tbl.rows
}

func TestFlattenValues(t *testing.T) {
	cases := []struct {
		name, doc string
		want      string
	}{
		{"wrapper rows carry the wrapper fields",
			`{"data":[{"t":1},{"t":2}],"vmid":101}`, `[{"t":1,"vmid":101},{"t":2,"vmid":101}]`},
		{"a clashing wrapper field is renamed",
			`{"data":[{"t":1}],"t":9}`, `[{"root_t":9,"t":1}]`},
		{"nested objects join with underscores, lists stay JSON text",
			`{"mem":{"total":8,"used":2},"load":[1,2]}`, `[{"load":"[1,2]","mem_total":8,"mem_used":2}]`},
		{"missing keys are null",
			`[{"a":{"x":1}},{"b":2}]`, `[{"a_x":1,"b":null},{"a_x":null,"b":2}]`},
		{"a column of numbers and text is text",
			`[{"v":1,"n":{"a":1}},{"v":"x","n":{"a":2}},{"v":2.5,"n":{"a":3}}]`,
			`[{"n_a":1,"v":"1"},{"n_a":2,"v":"x"},{"n_a":3,"v":"2.5"}]`},
		{"a column of numbers and booleans is text",
			`[{"v":true,"n":{"a":1}},{"v":3,"n":{"a":2}}]`, `[{"n_a":1,"v":"true"},{"n_a":2,"v":"3"}]`},
		{"a number keeps its digits",
			`{"data":[{"big":12345678901234567890,"f":0.1}]}`, `[{"big":12345678901234567890,"f":0.1}]`},
		{"an object below the depth limit is JSON text",
			`{"a":{"b":{"c":{"d":{"e":{"f":1}}}}}}`, `[{"a_b_c_d_e":"{\"f\":1}"}]`},
		{"an empty object is JSON text",
			`{"a":{},"b":{"c":1}}`, `[{"a":"{}","b_c":1}]`},
		{"plain elements of a mixed list are the value column",
			`[{"a":{"x":1}},7]`, `[{"a_x":1,"value":null},{"a_x":null,"value":7}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(flatRows(t, tc.doc))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestFlattenLeavesPlainTablesAndKubernetesAlone(t *testing.T) {
	for name, doc := range map[string]string{
		"flat list":            `[{"vmid":101,"name":"a"}]`,
		"list with a list":     `[{"vmid":101,"tags":["x","y"]}]`,
		"flat object":          `{"a":1,"b":"x"}`,
		"kubernetes list":      podsA,
		"kubernetes object":    `{"kind":"Pod","metadata":{"name":"a"},"status":{"phase":"Running"}}`,
		"list of k8s objects":  `[{"metadata":{"name":"a"},"spec":{"x":1}}]`,
		"empty list":           `[]`,
		"two lists of objects": `{"a":[{"x":1}],"b":[{"y":1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			tbl, err := flattenDocument([]byte(doc))
			if err != nil || tbl.changed {
				t.Fatalf("changed = %v, err = %v; the document must read as it came", tbl.changed, err)
			}
		})
	}
}

func TestFlattenTwoListsGetANote(t *testing.T) {
	tbl, err := flattenDocument([]byte(`{"a":[{"x":1}],"b":[{"y":1}]}`))
	if err != nil || len(tbl.notes) != 1 || !strings.Contains(tbl.notes[0], "a, b") {
		t.Fatalf("notes = %v, err = %v", tbl.notes, err)
	}
}

func TestFlattenRefusesNonJSON(t *testing.T) {
	for _, doc := range []string{"", notJSON, `{"a":`, `[1,`} {
		if _, err := flattenDocument([]byte(doc)); err == nil {
			t.Errorf("%q: want an error", doc)
		}
	}
	// Text that is not JSON never reaches the flattener: it is read as text.
	rows, cols := tableOf(t, "plain text\nsecond line")
	if rows != 2 || strings.Join(cols, ",") != "line,line_no" {
		t.Fatalf("got %d rows %v", rows, cols)
	}
}

func TestFlattenScalarListIsRowsOfValue(t *testing.T) {
	rows, cols := tableOf(t, `[1,2,3]`)
	if rows != 3 || strings.Join(cols, ",") != "value" {
		t.Fatalf("got %d rows %v", rows, cols)
	}
}

// bigWrapper is a wrapper of n samples with the given extra wrapper field.
func bigWrapper(n int, extra string) string {
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"t":%d,"cpu":%d.5}`, i, i%7)
	}
	b.WriteString(`]` + extra + `}`)
	return b.String()
}

func TestFlattenHugeArrayAtTheLimit(t *testing.T) {
	doc := bigWrapper(200000, "") // about 3.6 MB of input
	rows, cols := tableOf(t, doc)
	if rows != 200000 || strings.Join(cols, ",") != "cpu,t" {
		t.Fatalf("got %d rows %v", rows, cols)
	}
}

func TestFlattenKeepsTheSizeBound(t *testing.T) {
	// 5,000 rows each repeat a 2 KB wrapper field: about 10 MB out of about 0.1 MB in.
	doc := bigWrapper(5000, `,"blob":"`+strings.Repeat("x", 2000)+`"`)
	dir := t.TempDir()
	rel, ok, err := flattenRelation("t", []byte(doc), dir)
	if err != nil || ok {
		t.Fatalf("ok = %v, err = %v; the bound must stop the flattening", ok, err)
	}
	if len(rel.Notes) != 1 || rel.Notes[0] != noteTooLarge {
		t.Fatalf("notes = %v", rel.Notes)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("the partial file must be removed, found %d file(s)", len(left))
	}
}

func TestFlattenKeepsTheColumnLimit(t *testing.T) {
	leaf := map[string]int{}
	for i := 0; i < 300; i++ {
		leaf[fmt.Sprintf("k%d", i)] = i
	}
	raw, err := json.Marshal(map[string]any{"data": []any{map[string]any{"a": leaf, "b": leaf}}})
	if err != nil {
		t.Fatal(err)
	}
	rel, ok, err := flattenRelation("t", raw, t.TempDir())
	if err != nil || ok || len(rel.Notes) != 1 || rel.Notes[0] != noteTooWide {
		t.Fatalf("ok = %v, notes = %v, err = %v", ok, rel.Notes, err)
	}
}

func TestFlattenedColumnNamesAreUnquotedIdentifiers(t *testing.T) {
	for _, tc := range loadFlattenCases(t) {
		tbl, err := flattenDocument([]byte(tc.Input))
		if err != nil || !tbl.changed {
			continue
		}
		for col := range tbl.rows[0] {
			if !tableName.MatchString(col) {
				t.Errorf("%s: column %q is not a plain identifier", tc.Name, col)
			}
		}
	}
}

func TestFlattenFallsBackWhenTheDocumentCannotBeDecoded(t *testing.T) {
	rel, ok, err := flattenRelation("t", []byte(`{"data":[`), t.TempDir())
	if err != nil || ok || len(rel.Notes) != 1 || rel.Notes[0] != noteUnread {
		t.Fatalf("ok = %v, notes = %v, err = %v", ok, rel.Notes, err)
	}
}

func TestFlattenKeepsTheColumnLimitForSparseRows(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 600; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"a":{"k%d":1}}`, i)
	}
	b.WriteString("]")
	rel, ok, err := flattenRelation("t", []byte(b.String()), t.TempDir())
	if err != nil || ok || len(rel.Notes) != 1 || rel.Notes[0] != noteTooWide {
		t.Fatalf("ok = %v, notes = %v, err = %v", ok, rel.Notes, err)
	}
}

func TestFlattenRenamesNamesThatDifferOnlyByCase(t *testing.T) {
	got, err := json.Marshal(flatRows(t, `[{"A":{"x":1}},{"a":{"x":2}}]`))
	if err != nil || string(got) != `[{"A_x":1,"a_x_2":null},{"A_x":null,"a_x_2":2}]` {
		t.Fatalf("got %s, err = %v", got, err)
	}
}
