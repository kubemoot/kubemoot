package workspace

import (
	"fmt"
	"strings"
	"testing"
)

// Real-shaped results of the Proxmox tooler, trimmed. The sample objects are what the Proxmox API
// returns for a VM's RRD data (a "data" list of one object per time slot); list_vms and
// get_cluster_resources return a plain list of objects.
const (
	rrdSamplesFmt = `{"data":[` +
		`{"time":1,"cpu":%v,"mem":%d,"maxmem":8589934592,"netin":10.5,"netout":3.25,"diskread":0,"diskwrite":4096},` +
		`{"time":2,"cpu":%v,"mem":%d,"maxmem":8589934592,"netin":11.5,"netout":2.25,"diskread":512,"diskwrite":0},` +
		`{"time":3,"cpu":%v,"mem":%d,"maxmem":8589934592,"netin":9.5,"netout":4.25,"diskread":0,"diskwrite":1024}]}`

	listVMs = `[{"vmid":101,"name":"cp","node":"rig0","status":"running","cpu":0.05,"maxcpu":4,"mem":3,"maxmem":8,"tags":"k8s","template":0,"type":"qemu","uptime":99},` +
		`{"vmid":102,"name":"w1","node":"rig1","status":"stopped","cpu":0,"maxcpu":4,"mem":0,"maxmem":16,"tags":"","template":0,"type":"qemu","uptime":0}]`

	clusterResources = `[{"id":"qemu/101","type":"qemu","node":"rig0","status":"running","name":"cp","cpu":0.05,"maxcpu":4,"mem":3,"maxmem":8},` +
		`{"id":"node/rig0","type":"node","node":"rig0","status":"online","cpu":0.1,"maxcpu":16,"mem":30,"maxmem":64}]`

	nodeStatus = `{"uptime":1234,"loadavg":["0.5","0.4","0.3"],"memory":{"total":64,"used":30,"free":34},` +
		`"cpuinfo":{"cpus":16,"model":"Ryzen","sockets":1},"rootfs":{"total":100,"used":25}}`

	rrdKeyGlob = rrdPrefix + "*-get_vm_rrd_data"
	tblRRD     = "rrd"
)

// nineRRD is nine get_vm_rrd_data calls for VMs 101 to 109. VM n peaks at cpu n/10 and its mem
// peaks at n * 1000, so every per-VM maximum is different.
func nineRRD() map[string]object {
	objs := map[string]object{}
	for n := 1; n <= 9; n++ {
		body := fmt.Sprintf(rrdSamplesFmt, float64(n)/20, n*100, float64(n)/10, n*1000, float64(n)/40, n*10)
		objs[rrdKey(n)] = call("get_vm_rrd_data", body, headerFormat, "json",
			headerArgs, fmt.Sprintf(`{"node":"rig0","vmid":%d,"timeframe":"hour"}`, 100+n))
	}
	return objs
}

// bothEngines runs fn on SQLite and on DuckDB (skipped where the program is absent) over the objects.
func bothEngines(t *testing.T, objs map[string]object, fn func(t *testing.T, s *Service)) {
	t.Helper()
	for _, name := range []string{engineSQLite, engineDuckDB} {
		t.Run(name, func(t *testing.T) {
			s := callService(t, objs)
			if name == engineDuckDB {
				s.Engine = DuckDB{Bin: duckBin(t)}
			}
			fn(t, s)
		})
	}
}

func TestNineStackedRRDCallsGiveTheMaximumPerVM(t *testing.T) {
	bothEngines(t, nineRRD(), func(t *testing.T, s *Service) {
		got := query(t, s, map[string]string{tblRRD: rrdKeyGlob},
			"SELECT _arg_vmid AS vmid, MAX(cpu) AS peak_cpu, MAX(mem) AS peak_mem, AVG(netin) AS avg_netin, COUNT(*) AS n "+
				"FROM rrd GROUP BY _arg_vmid ORDER BY _arg_vmid")
		want := "vmid | peak_cpu | peak_mem | avg_netin | n\n" +
			"101 | 0.1 | 1000 | 10.5 | 3\n102 | 0.2 | 2000 | 10.5 | 3\n103 | 0.3 | 3000 | 10.5 | 3\n" +
			"104 | 0.4 | 4000 | 10.5 | 3\n105 | 0.5 | 5000 | 10.5 | 3\n106 | 0.6 | 6000 | 10.5 | 3\n" +
			"107 | 0.7 | 7000 | 10.5 | 3\n108 | 0.8 | 8000 | 10.5 | 3\n109 | 0.9 | 9000 | 10.5 | 3\n(9 rows)\n"
		if !strings.HasPrefix(got, want) {
			t.Fatalf("got %q\nwant %q", got, want)
		}
		if !strings.Contains(got, "note: table rrd: rows are flattened") {
			t.Fatalf("the table needs its note once, got %q", got)
		}
		if n := strings.Count(got, "rows are flattened"); n != 1 {
			t.Fatalf("the note appeared %d times", n)
		}
	})
}

func TestFlattenedRowsKeepTheCallColumnsOnEveryRow(t *testing.T) {
	bothEngines(t, nineRRD(), func(t *testing.T, s *Service) {
		got := query(t, s, map[string]string{tblRRD: rrdKeyGlob},
			"SELECT count(*) AS n, count(DISTINCT _call) AS calls, count(_args) AS args, count(_arg_vmid) AS vmids FROM rrd")
		if !strings.HasPrefix(got, "n | calls | args | vmids\n27 | 9 | 27 | 27\n(1 row)\n") {
			t.Fatalf("got %q", got)
		}
	})
}

func TestOneWrappedCallIsFlattenedToo(t *testing.T) {
	objs := map[string]object{rrdKey(1): nineRRD()[rrdKey(1)]}
	bothEngines(t, objs, func(t *testing.T, s *Service) {
		got := query(t, s, map[string]string{"r": rrdKey(1)}, "SELECT MAX(cpu) AS c, SUM(diskwrite) AS w FROM r")
		if !strings.HasPrefix(got, "c | w\n0.1 | 5120\n(1 row)\n") {
			t.Fatalf("got %q", got)
		}
	})
}

func TestNodeStatusNestedObjectsNeedNoJSONFunctions(t *testing.T) {
	objs := map[string]object{"ns/crew/th1/proxmox-pve/001-get_node_status": call("get_node_status", nodeStatus, headerFormat, "json")}
	bothEngines(t, objs, func(t *testing.T, s *Service) {
		got := query(t, s, map[string]string{"st": "ns/crew/th1/proxmox-pve/001-get_node_status"},
			"SELECT memory_used * 100.0 / memory_total AS pct, cpuinfo_cpus AS cpus, rootfs_used AS root, uptime FROM st")
		if !strings.HasPrefix(got, "pct | cpus | root | uptime\n46.875 | 16 | 25 | 1234\n(1 row)\n") {
			t.Fatalf("got %q", got)
		}
	})
}

func TestPlainListsStayAsTheyWere(t *testing.T) {
	objs := map[string]object{
		"ns/crew/th1/proxmox-qm/001-list_vms":               call("list_vms", listVMs, headerFormat, "json"),
		"ns/crew/th1/proxmox-pve/002-get_cluster_resources": call("get_cluster_resources", clusterResources, headerFormat, "json"),
	}
	bothEngines(t, objs, func(t *testing.T, s *Service) {
		got := query(t, s, map[string]string{
			"vms": "ns/crew/th1/proxmox-qm/001-list_vms",
			"res": "ns/crew/th1/proxmox-pve/002-get_cluster_resources",
		}, "SELECT v.name, r.id, r.maxmem FROM vms v JOIN res r ON r.id = 'qemu/' || v.vmid ORDER BY v.vmid")
		if got != "name | id | maxmem\ncp | qemu/101 | 8\n(1 row)\n" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestKubernetesListsKeepTheirNestedFields(t *testing.T) {
	objs := map[string]object{
		callPods1: call(toolPods, podsA, headerFormat, "json"),
		callPods2: call(toolPods, podsB, headerFormat, "json"),
	}
	for _, d := range dialects {
		t.Run(d.engine, func(t *testing.T) {
			s := callService(t, objs)
			if d.engine == engineDuckDB {
				s.Engine = DuckDB{Bin: duckBin(t)}
			}
			got := query(t, s, map[string]string{tblPods: callPrefix + "*-pods_list"},
				"SELECT "+d.objName+" AS name FROM pods ORDER BY 1")
			if !strings.HasPrefix(got, "name\na\nb\nc\n(3 rows)\n") {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestAnEmptyWrapperAndTextStayReadable(t *testing.T) {
	objs := map[string]object{
		"ns/crew/th1/proxmox-qm/001-get_vm_rrd_data": call("get_vm_rrd_data", `{"data":[],"count":0}`, headerFormat, "json"),
		"ns/crew/th1/proxmox-qm/002-get_vm_config":   call("get_vm_config", "name: web\ncores: 4\n"),
	}
	bothEngines(t, objs, func(t *testing.T, s *Service) {
		got := query(t, s, map[string]string{"r": "ns/crew/th1/proxmox-qm/001-get_vm_rrd_data"}, "SELECT count FROM r")
		if !strings.HasPrefix(got, "count\n0\n(1 row)\n") {
			t.Fatalf("got %q", got)
		}
		got = query(t, s, map[string]string{"c": "ns/crew/th1/proxmox-qm/002-get_vm_config"}, "SELECT cores FROM c")
		if !strings.HasPrefix(got, "cores\n4\n") {
			t.Fatalf("got %q", got)
		}
	})
}

func TestMixedKindsInOneColumnAgreeInBothEngines(t *testing.T) {
	objs := map[string]object{"ns/crew/th1/a/001-t": call("t", `{"data":[{"v":1,"n":{"a":1}},{"v":"x","n":{"a":2}}]}`, headerFormat, "json")}
	bothEngines(t, objs, func(t *testing.T, s *Service) {
		got := query(t, s, map[string]string{"t": "ns/crew/th1/a/001-t"}, "SELECT v, SUM(n_a) AS s FROM t GROUP BY v ORDER BY v")
		if !strings.HasPrefix(got, "v | s\n1 | 1\nx | 2\n(2 rows)\n") {
			t.Fatalf("got %q", got)
		}
	})
}
