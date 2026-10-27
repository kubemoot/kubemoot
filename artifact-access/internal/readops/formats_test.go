package readops

import (
	"strings"
	"testing"
)

const kubectlTable = "[resources_list]\n" +
	"NAMESPACE     APIVERSION   KIND        NAME        STATUS      AGE\n" +
	"default       v1           Namespace   default     Active      10d\n" +
	"kube-system   v1           Pod         coredns-1   Not Ready   2d\n" +
	"\n" +
	"[resources_list]\n" +
	"NAMESPACE     APIVERSION   KIND        NAME        STATUS      AGE\n" +
	"default       v1           Pod         web-1       Running     1h\n"

const yamlList = "apiVersion: v1\nkind: List\nitems:\n- metadata:\n    name: a\n  status:\n    phase: Running\n" +
	"- metadata:\n    name: b\n  status:\n    phase: Pending\n"

const (
	colName   = "name"
	wantEmpty = "empty"
)

const yamlMulti = "---\nmetadata:\n  name: one\n---\n---\nmetadata:\n  name: two\n"

type selectCase struct {
	name    string
	in      string
	cols    []string
	delim   rune
	want    string
	wantErr string
}

func runSelectCases(t *testing.T, cases []selectCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Select(strings.NewReader(tc.in), tc.cols, tc.delim, big)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v (output %q)", tc.wantErr, err, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestSelectTables(t *testing.T) {
	runSelectCases(t, []selectCase{
		{name: "kubectl table, case-insensitive, repeated header and marker skipped", in: kubectlTable,
			cols: []string{"Name", "Status"}, want: "NAME,STATUS\ndefault,Active\ncoredns-1,Not Ready\nweb-1,Running\n"},
		{name: "leading indent and wide gaps", in: "  NAME      ROLE\n  node-1    control plane\n  node-2    worker\n",
			cols: []string{"role", colName}, want: "ROLE,NAME\ncontrol plane,node-1\nworker,node-2\n"},
		{name: "single-space uppercase header", in: "NAME STATUS\nweb Running\napi Not Ready\n",
			cols: []string{"status"}, want: "STATUS\nRunning\nNot Ready\n"},
		{name: "tsv keeps empty cells", in: "NAME\tROLE\tAGE\nn1\t\t3d\nn2\tworker\t4d\n",
			cols: []string{"age", "role"}, want: "AGE,ROLE\n3d,\n4d,worker\n"},
		{name: "blank cell does not shift columns", in: "NAME     STATUS    LABELS    AGE\nweb      Running   app=web   1d\njob                          2d\n",
			cols: []string{"age", "name"}, want: "AGE,NAME\n1d,web\n2d,job\n"},
	})
}

func TestSelectCSVShapes(t *testing.T) {
	runSelectCases(t, []selectCase{
		{name: "comma header", in: "name,ns\na,b\n", cols: []string{"NS"}, want: "ns\nb\n"},
		{name: "single-column csv", in: "name\na\nb\n", cols: []string{colName}, want: "name\na\nb\n"},
		{name: "quoted commas", in: "name,note\na,\"x, y\"\n", cols: []string{"note"}, want: "note\n\"x, y\"\n"},
		{name: "quoted double spaces", in: "\"a  b\",c\n\"x  y\",z\n", cols: []string{"a  b"}, want: "a  b\nx  y\n"},
		{name: "space-padded fields", in: "name , ns\na , b\n", cols: []string{"ns"}, want: "ns\nb\n"},
		{name: "explicit semicolon", in: "a;b\n1;2\n", cols: []string{"b"}, delim: ';', want: "b\n2\n"},
		{name: "explicit tab keeps empty cells", in: "a\tb\n\t2\n", cols: []string{"a", "b"}, delim: '\t', want: "a\tb\n\t2\n"},
	})
}

func TestSelectErrors(t *testing.T) {
	var cols []string
	for i := 0; i < 40; i++ {
		cols = append(cols, "c"+strings.Repeat("x", i))
	}
	runSelectCases(t, []selectCase{
		{name: "missing column lists header", in: kubectlTable, cols: []string{"nope"},
			wantErr: `"NAMESPACE", "APIVERSION", "KIND", "NAME", "STATUS", "AGE"`},
		{name: "long header truncated", in: strings.Join(cols, "  ") + "\n", cols: []string{"zzz"}, wantErr: "40 columns"},
		{name: "no input", in: "", cols: []string{"a"}, wantErr: wantEmpty},
		{name: "blank lines only", in: "\n\n", cols: []string{"a"}, wantErr: wantEmpty},
		{name: "marker only", in: "[resources_list]\n", cols: []string{"a"}, wantErr: wantEmpty},
		{name: "no columns", in: kubectlTable, wantErr: "no columns"},
	})
}

func TestSelectTableBounded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("A  B\n")
	for i := 0; i < 1000; i++ {
		sb.WriteString("x  y\n")
	}
	got, err := Select(strings.NewReader(sb.String()), []string{"a"}, 0, 50)
	if err != nil || len(got) > 60 {
		t.Fatalf("len %d err %v", len(got), err)
	}
}

func TestCountAndSelectAgreeOnHeaders(t *testing.T) {
	in := "[resources_list]\nNAME STATUS\nweb Running\napi Running\n"
	n, err := CountData(strings.NewReader(in))
	if err != nil || n != 2 {
		t.Fatalf("count %d err %v", n, err)
	}
	got, err := Select(strings.NewReader(in), []string{colName}, 0, big)
	if err != nil || got != "NAME\nweb\napi\n" {
		t.Fatalf("select %q err %v", got, err)
	}
}

type jqCase struct {
	name    string
	in      string
	prog    string
	want    string
	wantErr string
}

func runJqCases(t *testing.T, cases []jqCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Jq(strings.NewReader(tc.in), tc.prog, big, big)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestJqFormats(t *testing.T) {
	runJqCases(t, []jqCase{
		{name: "yaml single", in: yamlList, prog: ".items[].status.phase", want: "\"Running\"\n\"Pending\"\n"},
		{name: "yaml multi-doc skips empty doc", in: yamlMulti, prog: ".metadata.name", want: "\"one\"\n\"two\"\n"},
		{name: "marker-prefixed yaml", in: "[resources_get]\n\n" + yamlList, prog: ".kind", want: "\"List\"\n"},
		{name: "marker-prefixed json", in: "[resources_get]\n{\"a\":1}\n", prog: ".a", want: "1\n"},
		{name: "json list", in: `[{"n":1},{"n":2}]`, prog: ".[].n", want: "1\n2\n"},
		{name: "json one-line array is not a marker", in: `["a"]`, prog: ".[0]", want: "\"a\"\n"},
		{name: "json stream", in: "{\"n\":1}\n{\"n\":2}\n", prog: ".n", want: "1\n2\n"},
		{name: "yaml flow style", in: "a: {b: [1, 2]}\n", prog: ".a.b", want: "[1,2]\n"},
	})
}

func TestJqKeepsScalarText(t *testing.T) {
	in := "date: 2026-10-01\nstamp: 2026-10-01T12:30:45.123456Z\nversion: \"1.10\"\nflag: true\nnothing: ~\nn: 5\n"
	runJqCases(t, []jqCase{{name: "scalars", in: in, prog: "[.date,.stamp,.version,.flag,.nothing,.n]",
		want: "[\"2026-10-01\",\"2026-10-01T12:30:45.123456Z\",\"1.10\",true,null,5]\n"}})
}

func TestJqNumbersMatchBetweenJSONAndYAML(t *testing.T) {
	want := "[18446744073709551615,9007199254740993,1.5]\n"
	runJqCases(t, []jqCase{
		{name: "json", in: `{"a":18446744073709551615,"b":9007199254740993,"c":1.5}`, prog: "[.a,.b,.c]", want: want},
		{name: "yaml", in: "a: 18446744073709551615\nb: 9007199254740993\nc: 1.5\n", prog: "[.a,.b,.c]", want: want},
	})
}

func TestJqKeys(t *testing.T) {
	runJqCases(t, []jqCase{
		{name: "non-string keys keep text", in: "1: a\n2: b\ntrue: c\n", prog: `[.["1"],.["2"],.["true"]]`, want: "[\"a\",\"b\",\"c\"]\n"},
		{name: "null key reads as null", in: "~: x\n", prog: `.["null"]`, want: "\"x\"\n"},
		{name: "int and string key collide", in: "1: a\n\"1\": b\n", prog: ".", wantErr: `two keys that read as "1"`},
		{name: "collection as key", in: "? [a, b]\n: x\n", prog: ".", wantErr: "used as a key"},
	})
}

func TestJqDocumentProblems(t *testing.T) {
	runJqCases(t, []jqCase{
		{name: "scalar among mappings", in: "a: 1\n---\nplain\n---\nb: 2\n", prog: ".", wantErr: "document 2 of 3 is a single value"},
		{name: "comment only", in: "# nothing here\n# at all\n", prog: ".", wantErr: "only comments"},
		{name: "document markers only", in: "---\n---\n", prog: ".", wantErr: "only comments or document markers"},
		{name: "no input", in: "", prog: ".", wantErr: wantEmpty},
		{name: "blank", in: " \n", prog: ".", wantErr: wantEmpty},
		{name: "marker only", in: "[resources_get]\n", prog: ".", wantErr: wantEmpty},
		{name: "truncated json object", in: `{"items":[{"a":1},{"a"`, prog: ".", wantErr: "looks like JSON but does not parse"},
		{name: "truncated json array", in: "[1,2,", prog: ".", wantErr: "looks like JSON"},
		{name: "malformed yaml", in: "a: [1, 2\nb: : :\n", prog: ".", wantErr: "not valid JSON or YAML"},
		{name: "table", in: kubectlTable, prog: ".", wantErr: "a table"},
		{name: "text", in: "proxmox vm list for node pve\nvm 100 running\n", prog: ".", wantErr: "plain text"},
		{name: "jq error on second document", in: "a: 1\n---\na: x\n", prog: ".a + 1", wantErr: "document 2 of 2"},
	})
}

func TestJqKeepsOutputBeforeLaterDocumentError(t *testing.T) {
	got, err := Jq(strings.NewReader("a: 1\n---\na: x\n"), ".a + 1", big, big)
	if err == nil || got != "2\n" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestJqYAMLAliasesUnderTheCap(t *testing.T) {
	in := "base: &b {x: 1, y: [1, 2]}\nitems:\n- *b\n- *b\n"
	runJqCases(t, []jqCase{{name: "alias", in: in, prog: ".items | map(.y[1])", want: "[2,2]\n"}})
}

func TestJqAliasBombStops(t *testing.T) {
	in := "a: &a [x, x, x, x, x, x, x, x, x, x]\n"
	prev := "a"
	for _, name := range []string{"b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q"} {
		refs := strings.Repeat("*"+prev+", ", 10)
		in += name + ": &" + name + " [" + strings.TrimSuffix(refs, ", ") + "]\n"
		prev = name
	}
	_, err := Jq(strings.NewReader(in), ".", int64(len(in))+1, big)
	if err == nil || !strings.Contains(err.Error(), "value limit") {
		t.Fatalf("want value limit error, got %v", err)
	}
}

func TestJqInputCap(t *testing.T) {
	deep := strings.Repeat("[", 50) + strings.Repeat("]", 50)
	if _, err := Jq(strings.NewReader(deep), ".", 20, big); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("nested over cap: %v", err)
	}
	if _, err := Jq(strings.NewReader("a: &x [1,2,3]\nb: *x\nc: *x\n"), ".", 10, big); err == nil {
		t.Fatal("aliased yaml over cap must error")
	}
	if got, err := Jq(strings.NewReader(deep), "length", 0, big); err != nil || got != "1\n" {
		t.Fatalf("cap <= 0 disables the cap: %q %v", got, err)
	}
}

func TestIsSectionMarker(t *testing.T) {
	for line, want := range map[string]bool{
		"[resources_list]": true, "[k8s-nodes/list]": true,
		"[true]": false, "[null]": false, `["a"]`: false, "[1,2]": false, "[]": false, "[a b]": false, "[{}]": false,
	} {
		if isSectionMarker(line) != want {
			t.Fatalf("%q: want %v", line, want)
		}
	}
}
