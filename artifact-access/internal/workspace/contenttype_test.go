package workspace

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/kubemoot/kubemoot/artifact-access/internal/store"
)

// typeFixture is the cases shared with agent-runtime's ResultTypeTest.
type typeFixture struct {
	Cases []struct {
		Name  string `json:"name"`
		Input string `json:"input"`
		Type  string `json:"type"`
	} `json:"cases"`
}

const csvType = "text/csv"

func TestDetectContentTypeSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/result-types.json")
	if err != nil {
		t.Fatal(err)
	}
	var f typeFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("the fixture has no cases")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := DetectContentType([]byte(c.Input)); got != c.Type {
				t.Errorf("DetectContentType(%q) = %q, want %q", c.Input, got, c.Type)
			}
		})
	}
}

func TestContentTypeRoundTripsEveryFormat(t *testing.T) {
	for f := range contentTypes {
		got, ok := FormatOf(ContentType(f))
		if !ok || got != f {
			t.Errorf("FormatOf(ContentType(%q)) = %q, %v", f, got, ok)
		}
	}
}

func TestContentTypeOfAnUnknownFormatIsText(t *testing.T) {
	if got := ContentType("sql"); got != "text/plain" {
		t.Errorf("got %q", got)
	}
}

func TestFormatOf(t *testing.T) {
	cases := []struct {
		in   string
		want Format
		ok   bool
	}{
		{"application/json", FormatJSON, true},
		{"Application/JSON; charset=utf-8", FormatJSON, true},
		{"application/vnd.kubernetes+json", FormatJSON, true},
		{"application/x-yaml", FormatYAML, true},
		{"application/vnd.foo+yaml", FormatYAML, true},
		{"text/plain; profile=kubectl-table", FormatKubectlTable, true},
		{"text/plain; profile=other", FormatText, true},
		{"text/plain; charset=utf-8", FormatText, true},
		{csvType, FormatCSV, true},
		{"text/markdown", FormatMarkdownTable, true},
		{"", "", false},
		{"not a media type", "", false},
		{"image/png", "", false},
		{"text/html", "", false},
		{"application/vnd.foo+xml", "", false},
		{"application/octet-stream", "", false},
	}
	for _, c := range cases {
		got, ok := FormatOf(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("FormatOf(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestHintOfReadsTheHeaderOnly(t *testing.T) {
	with := store.Meta{Headers: map[string]string{HeaderContentType: csvType}}
	if got := HintOf(with); got != FormatCSV {
		t.Errorf("got %q", got)
	}
	cut := store.Meta{Headers: map[string]string{HeaderContentType: csvType, headerTruncated: flagSet}}
	if got := HintOf(cut); got != "" {
		t.Errorf("a cut result has no hint, got %q", got)
	}
	for _, m := range []store.Meta{{}, {Headers: map[string]string{"kubemoot.format": "json"}}, // the header older objects carry; not read,
		{Headers: map[string]string{HeaderContentType: "image/png"}}} {
		if got := HintOf(m); got != "" {
			t.Errorf("HintOf(%v) = %q, want none", m.Headers, got)
		}
	}
}

func TestPrepareAsBelievesTheRecordedType(t *testing.T) {
	dir := t.TempDir()
	// Detect would call this CSV; the header says it is plain text, so it is read as lines.
	data := []byte("a,b\n1,2\n")
	rel, format, err := PrepareAs(FormatText, "t", "", data, dir)
	if err != nil || format != FormatText {
		t.Fatalf("format %q, err %v", format, err)
	}
	if rel.Kind != KindCSV {
		t.Fatalf("kind %v", rel.Kind)
	}
	if b, _ := os.ReadFile(rel.Path); string(b) != "line_no,line\n1,\"a,b\"\n2,\"1,2\"\n" {
		t.Fatalf("read as %q", b)
	}
}

func TestPrepareAsSniffsWhenThereIsNoType(t *testing.T) {
	_, format, err := PrepareAs("", "t", "", []byte("a,b\n1,2\n"), t.TempDir())
	if err != nil || format != FormatCSV {
		t.Fatalf("format %q, err %v", format, err)
	}
}

func TestPrepareAsDropsATypeTheBytesDoNotHold(t *testing.T) {
	cases := map[string]struct {
		hint Format
		body string
		want Format
	}{
		"JSON that is not JSON":    {FormatJSON, "plain words\nmore words\n", FormatText},
		"Markdown without a table": {FormatMarkdownTable, "plain words\n", FormatText},
		"YAML that does not parse": {FormatYAML, "a: b: c\n", FormatText},
		"a table type over JSON":   {FormatKubectlTable, `[{"a":1}]`, FormatKubectlTable},
		"JSON over a CSV body":     {FormatJSON, "a,b\n1,2\n", FormatCSV},
		"an empty body, text hint": {FormatText, "", FormatText},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, format, err := PrepareAs(c.hint, "t", "", []byte(c.body), t.TempDir())
			if err != nil || format != c.want {
				t.Fatalf("format %q (want %q), err %v", format, c.want, err)
			}
		})
	}
}

func TestReadTableAsBelievesTheRecordedType(t *testing.T) {
	data := []byte("NAME   READY\nfoo    1/1\n")
	format, rows, err := ReadTableAs(FormatKubectlTable, data, ',')
	if err != nil || format != FormatKubectlTable || len(rows) != 2 {
		t.Fatalf("format %q rows %v err %v", format, rows, err)
	}
	format, _, err = ReadTableAs(FormatText, data, ',')
	if format != FormatText || err != ErrNotTable {
		t.Fatalf("a text type is not a table: %q, %v", format, err)
	}
	format, _, err = ReadTableAs("", data, ',')
	if format != FormatKubectlTable || err != nil {
		t.Fatalf("no type sniffs: %q, %v", format, err)
	}
	if format, _, _ = ReadTableAs(FormatText, []byte("a;b\n1;2\n"), ';'); format != FormatCSV {
		t.Fatalf("a delimiter says CSV whatever the type: %q", format)
	}
}
