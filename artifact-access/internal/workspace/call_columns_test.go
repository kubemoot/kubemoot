package workspace

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	rrdPrefix = "ns/crew/th1/proxmox-qm/"
	rrdTable  = "time,cpu,mem\n1,0.5,100\n2,0.7,200\n"
	argVMID   = "vmid"
	notJSON   = "not json"
)

func rrdKey(n int) string { return fmt.Sprintf("%s%03d-get_vm_rrd_data", rrdPrefix, n) }

// rrdObjects is a VM list plus one RRD result per VM; the VM id lives only in the arguments.
func rrdObjects(vms int) map[string]object {
	objs := map[string]object{}
	for i := 1; i <= vms; i++ {
		objs[rrdKey(i)] = call("get_vm_rrd_data", rrdTable,
			headerArgs, fmt.Sprintf(`{"node":"pve","vmid":%d,"timeframe":"hour"}`, 100+i))
	}
	return objs
}

func TestStackedCallsCarryTheirCallAndArguments(t *testing.T) {
	for _, eng := range []string{"sqlite", engineDuckDB} {
		t.Run(eng, func(t *testing.T) {
			s := callService(t, rrdObjects(3))
			if eng == engineDuckDB {
				s.Engine = DuckDB{Bin: duckBin(t)}
			}
			got := query(t, s, map[string]string{"rrd": rrdPrefix + "*-get_vm_rrd_data"},
				"SELECT _arg_vmid AS vmid, max(cpu) AS cpu, count(*) AS n FROM rrd GROUP BY _arg_vmid ORDER BY 1")
			want := "vmid | cpu | n\n101 | 0.7 | 2\n102 | 0.7 | 2\n103 | 0.7 | 2\n(3 rows)\n"
			if !strings.HasPrefix(got, want) {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestStackedCallColumnsAreTheKeyAndTheArgumentText(t *testing.T) {
	s := callService(t, rrdObjects(2))

	got := query(t, s, map[string]string{"rrd": rrdPrefix + "*-get_vm_rrd_data"},
		"SELECT DISTINCT _call, _args FROM rrd ORDER BY 1")

	want := "_call | _args\n" + rrdKey(1) + ` | {"node":"pve","vmid":101,"timeframe":"hour"}` + "\n"
	if !strings.HasPrefix(got, want) || !strings.Contains(got, rrdKey(2)) {
		t.Fatalf("got %q", got)
	}
}

func TestStackedJSONCallsCarryTheColumnsToo(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1: call(toolPods, podsA, headerFormat, "json", headerArgs, `{"namespace":"x"}`),
		callPods2: call(toolPods, podsB, headerFormat, "json", headerArgs, `{"namespace":"y"}`),
	})

	got := query(t, s, map[string]string{tblPods: callPrefix + "*-pods_list"},
		"SELECT _arg_namespace AS ns, count(*) AS n FROM pods GROUP BY 1 ORDER BY 1")

	if !strings.HasPrefix(got, "ns | n\nx | 2\ny | 1\n") {
		t.Fatalf("got %q", got)
	}
}

func TestAnArgumentThatDoesNotVaryGetsNoColumnOfItsOwn(t *testing.T) {
	per := []map[string]string{{"node": "pve", argVMID: "1"}, {"node": "pve", argVMID: "2"}}
	if got := argColumns(per); !reflect.DeepEqual(got, []string{argVMID}) {
		t.Fatalf("got %v", got)
	}
}

func TestAnArgumentMissingFromOneCallGetsNoColumn(t *testing.T) {
	per := []map[string]string{{argVMID: "1"}, {argVMID: "2"}, {}}
	if got := argColumns(per); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestArgumentColumnsAreBoundedAndOrdered(t *testing.T) {
	a, b := map[string]string{}, map[string]string{}
	for i := 0; i < 9; i++ {
		k := fmt.Sprintf("k%d", i)
		a[k], b[k] = "1", "2"
	}
	got := argColumns([]map[string]string{a, b})
	if len(got) != maxArgColumns || got[0] != "k0" {
		t.Fatalf("got %v", got)
	}
}

func TestScalarArgsKeepsOnlyPlainValuesAndSurvivesBadText(t *testing.T) {
	got := scalarArgs(`{"s":"a","n":7,"b":true,"o":{"x":1},"l":[1],"z":null}`)
	want := map[string]string{"s": "a", "n": "7", "b": "true"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"", notJSON, `{"vmid": 1`, `[1,2]`} {
		if scalarArgs(bad) != nil {
			t.Fatalf("%q gave columns", bad)
		}
	}
}

func TestCallColumnsNeverReplaceDataColumns(t *testing.T) {
	g := []prepared{{call: "k1", args: `{"a":"1"}`}, {call: "k2", args: `{"a":"2"}`}}
	tag := tagFor(g, []string{"_CALL", "x"})
	if !reflect.DeepEqual(tag.names, []string{argsColumn, "_arg_a"}) {
		t.Fatalf("got %v", tag.names)
	}
	if !reflect.DeepEqual(tag.values["k2"], []string{`{"a":"2"}`, "2"}) {
		t.Fatalf("got %v", tag.values["k2"])
	}
}

func TestArgumentNamesBecomeSafeColumnNames(t *testing.T) {
	g := []prepared{{call: "k1", args: `{"label-selector":"a"}`}, {call: "k2", args: `{"label-selector":"b"}`}}
	if tag := tagFor(g, nil); tag.names[2] != "_arg_label_selector" {
		t.Fatalf("got %v", tag.names)
	}
}

func TestACallWithoutArgumentsHeaderStillHasCallAndEmptyArgs(t *testing.T) {
	s := callService(t, map[string]object{
		callPods1: call(toolPods, podsA, headerFormat, "json"),
		callPods2: call(toolPods, podsB, headerFormat, "json"),
	})

	got := query(t, s, map[string]string{tblPods: callPrefix + "*-pods_list"},
		"SELECT count(DISTINCT _call) AS calls FROM pods")

	if !strings.HasPrefix(got, "calls\n2\n") {
		t.Fatalf("got %q", got)
	}
}
