package workspace

import (
	"errors"
	"reflect"
	"testing"
)

func TestReadTableFormats(t *testing.T) {
	cases := []struct {
		name   string
		data   string
		comma  rune
		format Format
		want   [][]string
	}{
		{"kubectl table", "NAME   NODE SELECTOR   UP-TO-DATE\nweb    disk=ssd        2\n", ',', FormatKubectlTable,
			[][]string{{"name", "node_selector", "up_to_date"}, {"web", "disk=ssd", "2"}}},
		{"markdown table", "| Node Name | Ready |\n|---|---|\n| a | yes |\n", ',', FormatMarkdownTable,
			[][]string{{"node_name", "ready"}, {"a", "yes"}}},
		{"csv with default comma", "NS,Pod Count\na,3\n", 0, FormatCSV,
			[][]string{{"ns", "pod_count"}, {"a", "3"}}},
		{"explicit comma still detects", "NS,Pod Count\na,3\n", ',', FormatCSV,
			[][]string{{"ns", "pod_count"}, {"a", "3"}}},
		{"other delimiter skips detection", "A;B\n1;2\n", ';', FormatCSV,
			[][]string{{"a", "b"}, {"1", "2"}}},
		{"header only", "NAME   AGE\n", ',', FormatKubectlTable, [][]string{{"name", "age"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			format, rows, err := ReadTable([]byte(tc.data), tc.comma)
			if err != nil || format != tc.format || !reflect.DeepEqual(rows, tc.want) {
				t.Fatalf("got %s %q, %v; want %s %q", format, rows, err, tc.format, tc.want)
			}
		})
	}
}

func TestReadTableRefusesWhatIsNotATable(t *testing.T) {
	cases := []struct {
		name, data string
		format     Format
	}{
		{"a JSON object", `{"kind":"List"}`, FormatJSON},
		{"a YAML mapping", "a: 1\nb: 2\n", FormatYAML},
		{"prose", "The cluster is healthy.\nNothing to report.\n", FormatText},
		{"single column", "name\nfoo\n", FormatText},
		{"no bytes", "", FormatText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			format, rows, err := ReadTable([]byte(tc.data), ',')
			if !errors.Is(err, ErrNotTable) || format != tc.format || rows != nil {
				t.Fatalf("got %s %q, %v; want %s and ErrNotTable", format, rows, err, tc.format)
			}
		})
	}
}

func TestReadTableDelimitedTextOnlyReadsAsCSV(t *testing.T) {
	_, rows, err := ReadTable([]byte("a;b\n1;2;3\n4\n"), ';')
	if err != nil || len(rows) != 3 {
		t.Fatalf("ragged rows must be kept: %q, %v", rows, err)
	}
}
