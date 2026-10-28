package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		data string
		want Format
	}{
		{"json list", jsonTwoA, FormatJSON},
		{"json object", `{"items":[]}`, FormatJSON},
		{"kubectl table", kubectlPods, FormatKubectlTable},
		{"kubectl table with spaced titles", "NAME   NOMINATED NODE   AGE\nx      <none>           1d\n", FormatKubectlTable},
		{"yaml mapping", podsYAML, FormatYAML},
		{"yaml document stream", "---\na: 1\n---\na: 2\n", FormatYAML},
		{"yaml list", "- a: 1\n- a: 2\n", FormatYAML},
		{"markdown table", "| ns | pods |\n|----|------|\n| a | 3 |\n", FormatMarkdownTable},
		{"markdown table after prose", "Summary\n\n| ns | pods |\n|----|------|\n| a | 3 |\n", FormatMarkdownTable},
		{"markdown table with alignment", "| ns | pods |\n| :-- | --: |\n| a | 3 |\n", FormatMarkdownTable},
		{"csv", "ns,pods\na,3\nb,5\n", FormatCSV},
		{"plain prose", "The cluster is healthy.\nNothing to report.\n", FormatText},
		{"broken json is text", "[{\"a\":", FormatText},
		{"empty", "", FormatText},
		{"blank lines only", "\n\n  \n", FormatText},
		{"single column is text", "alpha\nbeta\n", FormatText},
		{"unparseable yaml is text", "key: [unclosed\n", FormatText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect([]byte(tc.data)); got != tc.want {
				t.Fatalf("Detect = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestParseKubectlTable(t *testing.T) {
	rows := parseKubectlTable(kubectlPods)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}
	if got := strings.Join(rows[0], "|"); got != "NAMESPACE|NAME|READY|STATUS|RESTARTS|AGE" {
		t.Fatalf("header = %s", got)
	}
	if got := strings.Join(rows[3], "|"); got != "kube-system|dns-abc-123|1/1|Running|3|40d" {
		t.Fatalf("row = %s", got)
	}
}

func TestParseKubectlTableKeepsEmptyAndSpacedCells(t *testing.T) {
	text := "NAME    NOMINATED NODE   AGE\nx       <none>           1d\ny                        2d\n"
	rows := parseKubectlTable(text)
	if got := strings.Join(rows[2], "|"); got != "y||2d" {
		t.Fatalf("empty cell row = %q", got)
	}
	if got := strings.Join(rows[0], "|"); got != "NAME|NOMINATED NODE|AGE" {
		t.Fatalf("header = %q", got)
	}
}

func TestParseKubectlTableShortLine(t *testing.T) {
	rows := parseKubectlTable("NAME   STATUS\nx\n")
	if got := strings.Join(rows[1], "|"); got != "x|" {
		t.Fatalf("short line = %q", got)
	}
}

func TestParseMarkdownTable(t *testing.T) {
	text := "Intro text\n\n| ns | pods |\n|---|---|\n| a | 3 |\n| b | 5 |\n\n| other | table |\n|---|---|\n| x | y |\n"
	rows := parseMarkdownTable(text)
	if len(rows) != 3 || strings.Join(rows[0], "|") != "ns|pods" || strings.Join(rows[2], "|") != "b|5" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestYAMLToJSON(t *testing.T) {
	doc, err := yamlToJSON([]byte("---\na: 1\n---\na: 2\n"))
	if err != nil || string(doc) != jsonTwoA {
		t.Fatalf("multi doc: %s %v", doc, err)
	}
	doc, err = yamlToJSON([]byte("a: 1\n"))
	if err != nil || string(doc) != `{"a":1}` {
		t.Fatalf("single doc: %s %v", doc, err)
	}
	for _, bad := range []string{"just a sentence", "a: [unclosed", ""} {
		if _, err := yamlToJSON([]byte(bad)); err == nil {
			t.Fatalf("yamlToJSON(%q) must fail", bad)
		}
	}
}

func TestHasItemsList(t *testing.T) {
	cases := map[string]bool{
		`{"apiVersion":"v1","items":[{"a":1}],"kind":"List"}`: true,
		`{"items":[]}`:           true,
		`{"items":{"a":1}}`:      false,
		`{"other":[1,2]}`:        false,
		`[{"items":[1]}]`:        false,
		`{"items":[1`:            false,
		`not json`:               false,
		`{"a":{"items":[1]}}`:    false,
		`{"b":"x","items":null}`: false,
	}
	for in, want := range cases {
		if got := hasItemsList([]byte(in)); got != want {
			t.Errorf("hasItemsList(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestPrepareWritesConvertedCopies(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name     string
		data     string
		wantFmt  Format
		wantKind Kind
		wantFile string
	}{
		{"kubectl", kubectlPods, FormatKubectlTable, KindCSV, "kubectl.csv"},
		{"yaml", podsYAML, FormatYAML, KindJSONItems, "yaml.json"},
		{"md", "| a | b |\n|---|---|\n| 1 | 2 |\n", FormatMarkdownTable, KindCSV, "md.csv"},
		{"text", "one\ntwo\n", FormatText, KindCSV, "text.csv"},
	}
	for _, tc := range cases {
		rel, format, err := Prepare(tc.name, artifactK, []byte(tc.data), dir)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if format != tc.wantFmt || rel.Kind != tc.wantKind || rel.Path != filepath.Join(dir, tc.wantFile) {
			t.Fatalf("%s: got %s %s %s", tc.name, format, rel.Kind, rel.Path)
		}
		if _, err := os.Stat(rel.Path); err != nil {
			t.Fatalf("%s: converted file missing: %v", tc.name, err)
		}
	}
}

func TestPrepareReadsJSONAndCSVInPlace(t *testing.T) {
	rel, _, err := Prepare("a", artifactK, []byte(`{"items":[{"x":1}]}`), t.TempDir())
	if err != nil || rel.Path != artifactK || rel.Kind != KindJSONItems {
		t.Fatalf("json: %+v %v", rel, err)
	}
	rel, _, err = Prepare("a", artifactK, []byte(csvAB), t.TempDir())
	if err != nil || rel.Path != artifactK || rel.Kind != KindCSV {
		t.Fatalf("csv: %+v %v", rel, err)
	}
}

func TestPrepareFailsWhenWorkDirIsMissing(t *testing.T) {
	if _, _, err := Prepare("t", "/x", []byte("one\ntwo\n"), filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("writing into a missing directory must fail")
	}
}
