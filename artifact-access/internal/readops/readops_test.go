package readops

import (
	"fmt"
	"strings"
	"testing"
)

const sample = "alpha\nbravo\ncharlie\ndelta\necho\n"
const big = 1 << 20

func TestHead(t *testing.T) {
	got, err := Head(strings.NewReader(sample), 2, big)
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nbravo\n[lines 0-1 of 5]\n" {
		t.Fatalf("got %q", got)
	}
	if got, _ := Head(strings.NewReader(sample), 0, big); got != "" {
		t.Fatalf("n=0 must be empty, got %q", got)
	}
	if got, _ := Head(strings.NewReader(sample), 100, big); got != sample+"[lines 0-4 of 5]\n" {
		t.Fatalf("n>len got %q", got)
	}
}

func TestTail(t *testing.T) {
	got, err := Tail(strings.NewReader(sample), 2, big)
	if err != nil {
		t.Fatal(err)
	}
	if got != "delta\necho\n[lines 3-4 of 5]\n" {
		t.Fatalf("got %q", got)
	}
	if got, _ := Tail(strings.NewReader(sample), 100, big); got != sample+"[lines 0-4 of 5]\n" {
		t.Fatalf("n>len got %q", got)
	}
	if got, _ := Tail(strings.NewReader(sample), 0, big); got != "" {
		t.Fatalf("n=0 must be empty, got %q", got)
	}
}

func TestGrep(t *testing.T) {
	got, err := Grep(strings.NewReader(sample), "a", 0, big)
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha\nbravo\ncharlie\ndelta\n" {
		t.Fatalf("got %q", got)
	}
	if got, _ := Grep(strings.NewReader(sample), "a", 1, big); got != "alpha\n" {
		t.Fatalf("max=1 got %q", got)
	}
	if _, err := Grep(strings.NewReader(sample), "(", 0, big); err == nil {
		t.Fatal("a bad pattern must error")
	}
}

func TestCount(t *testing.T) {
	n, err := Count(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("got %d", n)
	}
	if n, _ := Count(strings.NewReader("")); n != 0 {
		t.Fatalf("empty got %d", n)
	}
}

func TestCountData(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int64
	}{
		{"header + rows + trailing blank",
			"APIVERSION   KIND        NAME\nv1  Namespace  arc-runners\nv1  Namespace  cert-manager\nv1  Namespace  default\n\n", 3},
		{"section marker + header + rows (the real tooler shape)",
			"[resources_list]\nAPIVERSION   KIND        NAME\nv1  Namespace  arc-runners\nv1  Namespace  cert-manager\nv1  Namespace  default\n", 3},
		{"two tool sections, data rows across both",
			"[resources_list]\nAPIVERSION KIND NAME\nv1 Namespace a\nv1 Namespace b\n[resources_list]\nAPIVERSION KIND NAME\napps/v1 Deployment d\n", 3},
		{"plain lowercase list, no header", "arc-runners\ncert-manager\ndefault\n", 3},
		{"headerless data rows are not mistaken for a header", "v1 Namespace arc-runners\nv1 Namespace default\n", 2},
		{"blank and whitespace-only lines skipped", "a\n\n   \nb\n", 2},
		{"single all-caps token is not a header (needs >= 2 columns)", "TOTAL\n", 1},
		{"empty input", "", 0},
	}
	for _, tc := range cases {
		n, err := CountData(strings.NewReader(tc.input))
		if err != nil {
			t.Fatalf("%s: unexpected err %v", tc.name, err)
		}
		if n != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, n, tc.want)
		}
	}
}

func TestRows(t *testing.T) {
	got, err := Rows(strings.NewReader(sample), 1, 2, big)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bravo\ncharlie\n[lines 1-2 of 5]\n" {
		t.Fatalf("got %q", got)
	}
	if got, _ := Rows(strings.NewReader(sample), 3, 0, big); got != "delta\necho\n[lines 3-4 of 5]\n" {
		t.Fatalf("limit=0 got %q", got)
	}
	if got, _ := Rows(strings.NewReader(sample), 99, 5, big); got != "[no lines from line 99; the artifact has 5 lines]\n" {
		t.Fatalf("start>len got %q", got)
	}
	if got, _ := Rows(strings.NewReader(sample), -3, 1, big); got != "alpha\n[lines 0-0 of 5]\n" {
		t.Fatalf("negative start got %q", got)
	}
}

func TestSelectCSV(t *testing.T) {
	csvIn := "name,ns,age\npod-a,default,3\npod-b,kube-system,7\n"
	got, err := SelectCSV(strings.NewReader(csvIn), []string{"ns", "name"}, 0, big)
	if err != nil {
		t.Fatal(err)
	}
	want := "ns,name\ndefault,pod-a\nkube-system,pod-b\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err := SelectCSV(strings.NewReader(csvIn), []string{"nope"}, 0, big); err == nil {
		t.Fatal("missing column must error")
	}
	if _, err := SelectCSV(strings.NewReader(csvIn), nil, 0, big); err == nil {
		t.Fatal("no columns must error")
	}
}

func TestJq(t *testing.T) {
	jsonIn := `{"items":[{"n":"a"},{"n":"b"}]}`
	got, err := Jq(strings.NewReader(jsonIn), ".items[].n", big, big)
	if err != nil {
		t.Fatal(err)
	}
	if got != "\"a\"\n\"b\"\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := Jq(strings.NewReader(jsonIn), ".", 5, big); err == nil {
		t.Fatal("input over cap must error")
	}
	if _, err := Jq(strings.NewReader(jsonIn), ".[", big, big); err == nil {
		t.Fatal("bad program must error")
	}
	if _, err := Jq(strings.NewReader("not json"), ".", big, big); err == nil {
		t.Fatal("bad json must error")
	}
}

// --- output bounding ("sip, don't slurp" enforced inside the op) ---

func TestHeadBoundedByMaxBytes(t *testing.T) {
	// 1000 short lines; cap output at 50 bytes -> only a handful of lines come back.
	in := strings.Repeat("0123456789\n", 1000)
	got, err := Head(strings.NewReader(in), 1000, 50)
	if err != nil {
		t.Fatal(err)
	}
	footer := "[lines 0-3 of 1000]\n"
	if len(got) > 50+len("0123456789\n")+len(footer) {
		t.Fatalf("output not bounded: %d bytes", len(got))
	}
	if !strings.HasSuffix(got, " of 1000]\n") {
		t.Fatalf("a capped page still reports the artifact's total, got %q", got[len(got)-30:])
	}
}

func TestGrepBoundedByMaxBytes(t *testing.T) {
	in := strings.Repeat("match\n", 100000)
	got, err := Grep(strings.NewReader(in), "match", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 100+len("match\n") {
		t.Fatalf("grep output not bounded: %d bytes", len(got))
	}
}

func TestJqOutputBoundedByMaxBytes(t *testing.T) {
	// A generator that would emit ~1M results; the input is tiny but the OUTPUT must
	// be bounded so a jq program cannot balloon memory.
	got, err := Jq(strings.NewReader("null"), "range(1000000)", big, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 100+8 {
		t.Fatalf("jq output not bounded: %d bytes", len(got))
	}
}

func TestSelectCSVBoundedByMaxBytes(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("a,b\n")
	for i := 0; i < 100000; i++ {
		sb.WriteString("x,y\n")
	}
	got, err := SelectCSV(strings.NewReader(sb.String()), []string{"a"}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 100+8 {
		t.Fatalf("select output not bounded: %d bytes", len(got))
	}
}

// linesAndRange returns how many content lines a page holds and the range its
// position line claims.
func linesAndRange(t *testing.T, page string) (int, int, int) {
	t.Helper()
	i := strings.LastIndex(strings.TrimSuffix(page, "\n"), "\n")
	footer := page[i+1:]
	var a, b, total int
	if _, err := fmt.Sscanf(footer, "[lines %d-%d of %d]", &a, &b, &total); err != nil {
		t.Fatalf("no range in %q", footer)
	}
	return strings.Count(page[:i+1], "\n"), a, b
}

func TestPositionMatchesWhatACappedPageReturns(t *testing.T) {
	in := strings.Repeat("0123456789\n", 1000)
	for _, maxBytes := range []int{150, 200, 1000} {
		page, err := Rows(strings.NewReader(in), 10, 500, maxBytes)
		if err != nil {
			t.Fatal(err)
		}
		n, a, b := linesAndRange(t, page)
		if n != b-a+1 || a != 10 {
			t.Fatalf("maxBytes %d: returned %d lines but claims %d-%d", maxBytes, n, a, b)
		}
		if len(page) > maxBytes {
			t.Fatalf("maxBytes %d: page with position is %d bytes", maxBytes, len(page))
		}
	}
}

func TestPositionEdges(t *testing.T) {
	if got, _ := Rows(strings.NewReader(""), 0, 10, big); got != "[no lines from line 0; the artifact has 0 lines]\n" {
		t.Fatalf("empty artifact got %q", got)
	}
	if got, _ := Rows(strings.NewReader(sample), 5, 10, big); got != "[no lines from line 5; the artifact has 5 lines]\n" {
		t.Fatalf("start == total got %q", got)
	}
	if got, _ := Head(strings.NewReader(sample), 1, big); got != "alpha\n[lines 0-0 of 5]\n" {
		t.Fatalf("head got %q", got)
	}
}

func TestALongLinePastThePageDoesNotFailIt(t *testing.T) {
	in := "a\nb\n" + strings.Repeat("x", maxLineBytes+10) + "\nc\n"
	got, err := Rows(strings.NewReader(in), 0, 2, big)
	if err != nil {
		t.Fatalf("a long line after the page must not fail it: %v", err)
	}
	if got != "a\nb\n[lines 0-1 of at least 2]\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFirstLineAlwaysReturned(t *testing.T) {
	long := strings.Repeat("y", 500)
	got, _ := Rows(strings.NewReader(long+"\nshort\n"), 0, 5, 200)
	if !strings.HasPrefix(got, long+"\n[lines 0-0 of 2]") {
		t.Fatalf("a first line over the budget still comes back, got %q", got[:40])
	}
}
