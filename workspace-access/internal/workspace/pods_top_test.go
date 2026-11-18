package workspace

import (
	"os"
	"strings"
	"testing"
)

// podsTop is kubernetes-mcp-server pods_top output: kubectl top columns whose
// titles carry a lower-case unit in parentheses, CPU(cores) and MEMORY(bytes).
func podsTop(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/pods_top.txt")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDetectReadsPodsTopAsKubectlTable(t *testing.T) {
	if got := Detect(podsTop(t)); got != FormatKubectlTable {
		t.Fatalf("Detect = %q, want %q", got, FormatKubectlTable)
	}
}

func TestPreparePodsTopHasFieldColumns(t *testing.T) {
	rel, _, err := Prepare("pods_top", "", podsTop(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	csv, err := os.ReadFile(rel.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(csv), "namespace,pod,name,cpu_cores,memory_bytes,swap_bytes\n") {
		t.Fatalf("header = %q", strings.SplitN(string(csv), "\n", 2)[0])
	}
	rows := parseKubectlTable(string(podsTop(t)))
	want := []string{"NAMESPACE", "POD", "NAME", "CPU(cores)", "MEMORY(bytes)", "SWAP(bytes)"}
	for i, w := range want {
		if rows[0][i] != w {
			t.Fatalf("column %d = %q, want %q", i, rows[0][i], w)
		}
	}
	if !strings.HasSuffix(rows[1][4], "Mi") {
		t.Fatalf("memory cell = %q", rows[1][4])
	}
}

func TestTitleWithLowerCaseWordsIsNotATable(t *testing.T) {
	for _, text := range []string{"Namespace   Pod\nx   y\n", "The pods (running) are fine\n"} {
		if got := Detect([]byte(text)); got != FormatText {
			t.Errorf("Detect(%q) = %q, want text", text, got)
		}
	}
}
