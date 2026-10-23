package readops

import (
	"strings"
	"testing"
)

const kubectlTable = "[resources_list]\n" +
	"NAMESPACE   APIVERSION   KIND        NAME           STATUS      AGE\n" +
	"default     v1           Namespace   default        Active      10d\n" +
	"kube-system   v1           Pod         coredns-1      Not Ready   2d\n" +
	"\n" +
	"[resources_list]\n" +
	"NAMESPACE   APIVERSION   KIND        NAME           STATUS      AGE\n" +
	"default     v1           Pod         web-1          Running     1h\n"

const yamlList = "apiVersion: v1\nkind: List\nitems:\n- metadata:\n    name: a\n  status:\n    phase: Running\n" +
	"- metadata:\n    name: b\n  status:\n    phase: Pending\n"

const yamlMulti = "---\nmetadata:\n  name: one\n  created: 2026-10-01T00:00:00Z\n---\n---\nmetadata:\n  name: two\n"

func TestSelectKubectlTableCaseInsensitive(t *testing.T) {
	got, err := Select(strings.NewReader(kubectlTable), []string{"Name", "Status"}, 0, big)
	if err != nil {
		t.Fatal(err)
	}
	want := "NAME,STATUS\ndefault,Active\ncoredns-1,Not Ready\nweb-1,Running\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSelectTableUnusualSpacing(t *testing.T) {
	in := "NAME\t\tSTATUS      ROLE\nnode-1\tReady       control plane\n  node-2     NotReady  worker\n"
	got, err := Select(strings.NewReader(in), []string{"role", "name"}, 0, big)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ROLE,NAME\ncontrol plane,node-1\nworker,node-2\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSelectMissingColumnListsHeader(t *testing.T) {
	_, err := Select(strings.NewReader(kubectlTable), []string{"nope"}, 0, big)
	if err == nil {
		t.Fatal("must error")
	}
	for _, w := range []string{`"NAMESPACE"`, `"AGE"`, "6 columns", "artifact_jq"} {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("error %q lacks %q", err, w)
		}
	}
}

func TestSelectLongHeaderIsTruncated(t *testing.T) {
	var cols []string
	for i := 0; i < 40; i++ {
		cols = append(cols, "c"+strings.Repeat("x", i))
	}
	_, err := Select(strings.NewReader(strings.Join(cols, "  ")+"\n"), []string{"zzz"}, 0, big)
	if err == nil || !strings.Contains(err.Error(), "40 columns") || !strings.HasSuffix(strings.Split(err.Error(), ";")[1], ", ...") {
		t.Fatalf("got %v", err)
	}
}

func TestSelectCSVFallbackAndCase(t *testing.T) {
	got, err := Select(strings.NewReader("name,ns\na,b\n"), []string{"NS"}, 0, big)
	if err != nil || got != "ns\nb\n" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = Select(strings.NewReader("a;b\n1;2\n"), []string{"b"}, ';', big)
	if err != nil || got != "b\n2\n" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestSelectEmptyAndNoColumns(t *testing.T) {
	for _, in := range []string{"", "\n\n", "[resources_list]\n"} {
		if _, err := Select(strings.NewReader(in), []string{"a"}, 0, big); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("input %q: got %v", in, err)
		}
	}
	if _, err := Select(strings.NewReader(kubectlTable), nil, 0, big); err == nil {
		t.Fatal("no columns must error")
	}
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

func TestJqYAMLSingleDocument(t *testing.T) {
	got, err := Jq(strings.NewReader(yamlList), ".items[].status.phase", big, big)
	if err != nil || got != "\"Running\"\n\"Pending\"\n" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestJqYAMLMultiDocument(t *testing.T) {
	got, err := Jq(strings.NewReader(yamlMulti), ".metadata.name", big, big)
	if err != nil || got != "\"one\"\n\"two\"\n" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = Jq(strings.NewReader(yamlMulti), ".metadata.created", big, big)
	if err != nil || !strings.HasPrefix(got, "\"2026-10-01T00:00:00Z\"") {
		t.Fatalf("timestamp got %q err %v", got, err)
	}
}

func TestJqJSONListAndStream(t *testing.T) {
	got, err := Jq(strings.NewReader(`[{"n":1},{"n":2}]`), ".[].n", big, big)
	if err != nil || got != "1\n2\n" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = Jq(strings.NewReader("{\"n\":1}\n{\"n\":2}\n"), ".n", big, big)
	if err != nil || got != "1\n2\n" {
		t.Fatalf("stream got %q err %v", got, err)
	}
}

func TestJqYAMLNonStringKeys(t *testing.T) {
	got, err := Jq(strings.NewReader("1: a\n2: b\n"), `.["2"]`, big, big)
	if err != nil || got != "\"b\"\n" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestJqEmptyArtifact(t *testing.T) {
	for _, in := range []string{"", " \n", "---\n"} {
		if _, err := Jq(strings.NewReader(in), ".", big, big); err == nil {
			t.Fatalf("input %q must error", in)
		}
	}
	if _, err := Jq(strings.NewReader(""), ".", big, big); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("got %v", err)
	}
}

func TestJqRejectsTableAndText(t *testing.T) {
	_, err := Jq(strings.NewReader(kubectlTable), ".", big, big)
	if err == nil || !strings.Contains(err.Error(), "a table") || !strings.Contains(err.Error(), "artifact_select") {
		t.Fatalf("table: %v", err)
	}
	_, err = Jq(strings.NewReader("proxmox vm list for node pve\nvm 100 running\n"), ".", big, big)
	if err == nil || !strings.Contains(err.Error(), "plain text") || !strings.Contains(err.Error(), "artifact_grep") {
		t.Fatalf("text: %v", err)
	}
}

func TestNormalizeNumbers(t *testing.T) {
	got, err := Jq(strings.NewReader("big: 18446744073709551615\nsmall: 5\nf: 1.5\n"), "[.big, .small, .f]", big, big)
	if err != nil || !strings.Contains(got, "5,1.5") {
		t.Fatalf("got %q err %v", got, err)
	}
}
