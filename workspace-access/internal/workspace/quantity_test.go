package workspace

import (
	"strings"
	"testing"
)

func TestParseQuantity(t *testing.T) {
	cases := []struct {
		cell, want, unit string
		ok               bool
	}{
		{mebi65, "68157440", unitBytes, true},
		{"1Gi", "1073741824", unitBytes, true},
		{"2Ki", "2048", unitBytes, true},
		{"1Ti", "1099511627776", unitBytes, true},
		{"1Ei", "1152921504606846976", unitBytes, true},
		{"5k", "5000", unitBytes, true},
		{"5M", "5000000", unitBytes, true},
		{"1G", "1000000000", unitBytes, true},
		{"1E", "1000000000000000000", unitBytes, true},
		{"1.5Gi", "1610612736", unitBytes, true},
		{"250m", "0.25", unitCores, true},
		{"1500m", "1.5", unitCores, true},
		{"12e3", "12000", "", true},
		{"1E3", "1000", "", true},
		{"7", "7", "", true},
		{"-3", "-3", "", true},
		{"0.5", "0.5", "", true},
		{"", "", "", false},
		{"Mi", "", "", false},
		{"65 Mi", "", "", false},
		{"65mi", "", "", false},
		{"<unknown>", "", "", false},
		{"3d", "", "", false},
		{"running", "", "", false},
	}
	for _, c := range cases {
		v, unit, ok := parseQuantity(c.cell)
		if ok != c.ok {
			t.Errorf("%q: ok = %v, want %v", c.cell, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if formatQuantity(v) != c.want || unit != c.unit {
			t.Errorf("%q = %s %s, want %s %s", c.cell, formatQuantity(v), unit, c.want, c.unit)
		}
	}
}

func TestQuantifyLeavesOtherColumnsAlone(t *testing.T) {
	rows := [][]string{
		{nameCol, "cpu_cores", "memory_bytes", "mixed", "count", "age"},
		{"a", "250m", "65Mi", "5Mi", "3", "5m"},
		{"b", "1", "1Gi", "n/a", "10", "7m"},
		{"c", "", "", "2", "", ""},
	}
	out, units := quantify(rows)
	if units["cpu_cores"] != unitCores || units["memory_bytes"] != unitBytes || len(units) != 2 {
		t.Fatalf("units = %v", units)
	}
	if out[1][1] != "0.25" || out[2][2] != "1073741824" || out[3][1] != "" {
		t.Fatalf("rows = %v", out)
	}
	if !sameCells(out[1][3:], []string{"5Mi", "3", "5m"}) {
		t.Fatalf("mixed, plain and age columns must keep their text: %v", out)
	}
	if rows[1][2] != "65Mi" {
		t.Fatal("the input rows must not change")
	}
}

const (
	tableKey = "k/t"
	nameCol  = "name"
	mebi65   = "65Mi"
)

const quantityTable = "NAME    CPU(cores)   MEMORY(bytes)\nsmall   1            9Mi\nbig     250m         1Gi\nmid     2            900Mi\n"

func TestPodsTopSortsByMemoryNumerically(t *testing.T) {
	data := podsTop(t)
	s := newService(t, engineSQLite, map[string]string{"k/001-pods_top": string(data)})
	got := query(t, s, map[string]string{"p": "k/001-pods_top"},
		"SELECT pod, memory_bytes FROM p ORDER BY memory_bytes DESC LIMIT 1")
	rows := parseKubectlTable(string(data))
	best, bestBytes := "", float64(-1)
	for _, r := range rows[1:] {
		v, _, ok := parseQuantity(r[4])
		if !ok {
			continue
		}
		if f, _ := v.Float64(); f > bestBytes {
			best, bestBytes = r[1], f
		}
	}
	if !strings.Contains(got, best) {
		t.Fatalf("largest pod is %s, query gave %q", best, got)
	}
}

func TestQuantityColumnsCompareAndSum(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{tableKey: quantityTable})
	got := query(t, s, map[string]string{"t": tableKey}, "SELECT "+nameCol+" FROM t ORDER BY memory_bytes DESC")
	if !strings.HasPrefix(got, nameCol+"\nbig\nmid\nsmall\n") {
		t.Fatalf("memory order = %q", got)
	}
	got = query(t, s, map[string]string{"t": tableKey}, "SELECT "+nameCol+" FROM t ORDER BY cpu_cores DESC")
	if !strings.HasPrefix(got, nameCol+"\nmid\nsmall\nbig\n") {
		t.Fatalf("cpu order (250m below 1) = %q", got)
	}
	got = query(t, s, map[string]string{"t": tableKey}, "SELECT SUM(memory_bytes) AS s FROM t")
	if !strings.Contains(got, "2026897408") {
		t.Fatalf("sum = %q", got)
	}
}

func TestMixedColumnStaysText(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{tableKey: "NAME   MEMORY(bytes)\na      9Mi\nb      lots\n"})
	got := describe(t, s, map[string]string{"t": tableKey})
	if strings.Contains(got, "quantity, ") || !strings.Contains(got, "memory_bytes: text") {
		t.Fatalf("describe = %s", got)
	}
}

func TestPlainIntegerColumnIsUnchanged(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{tableKey: "NAME   RESTARTS\na      3\nb      10\n"})
	got := describe(t, s, map[string]string{"t": tableKey})
	if strings.Contains(got, "quantity, ") || !strings.Contains(got, "restarts: integer") {
		t.Fatalf("describe = %s", got)
	}
}

func TestDescribeSaysQuantityAndUnit(t *testing.T) {
	s := newService(t, engineSQLite, map[string]string{tableKey: quantityTable})
	got := describe(t, s, map[string]string{"t": tableKey})
	for _, want := range []string{"- memory_bytes: quantity, bytes", "- cpu_cores: quantity, cores"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func sameCells(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }
