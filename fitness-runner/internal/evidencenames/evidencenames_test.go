/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package evidencenames

import (
	"reflect"
	"strings"
	"testing"
)

const webFront = "web-front"

const nodeTable = `NAME                         STATUS   ROLES           AGE   VERSION   INTERNAL-IP
homelab-k8s-1-cp             Ready    control-plane   40d   v1.35.0   192.168.50.11
homelab-k8s-1-cp-2           Ready    <none>          40d   v1.35.0   192.168.50.12
homelab-k8s-1-gpu-worker     Ready    gpu-worker      40d   v1.35.0   192.168.50.21`

const diskQuestion = "The recent pod restarts were caused by the node running out of disk, confirm that and tell me which node."

const node3Answer = "The recent pod restarts were confirmed to be caused by **node3** running out of disk space.\n\n" +
	"1. **Disk Pressure Confirmation**:\n" +
	"   - The `k8s-nodes/001-resources_list` output explicitly shows **node3** has `DiskPressure: True`.\n" +
	"   - indicates **ephemeral storage exhaustion** on node3; check with `ephemeral-storage` capacity.\n" +
	"   - run `kubectl describe node node3` to see `ephemeral-storage` usage.\n" +
	"```\nkubectl describe node <node-name>\n```\n"

const node12345Answer = "The restarts were triggered by **node-12345** running out of disk space.\n" +
	"- Events from `k8s-nodes/003-events_list` list `NodeCondition:DiskPressure` for **node-12345**.\n" +
	"- No other nodes showed disk pressure; see 4gi of 16Gi, v1.35.0, 192.168.50.11, 2026-11-15."

const nodeListAnswer = "The Kubernetes cluster has **13 nodes**.\n\n**Node list (sorted by name):**\n" +
	"- node-01  \n- node-02  \n- node-13  \n\n**Source**: derived from `kubectl get nodes`."

func evidenceOf(texts ...string) *Evidence {
	e := NewEvidence()
	for _, t := range texts {
		e.Add(t)
	}
	return e
}

func TestInvented(t *testing.T) {
	cases := []struct {
		name     string
		answer   string
		evidence []string
		ignore   []string
		want     []string
	}{
		{"node3 from a real run", node3Answer, []string{nodeTable, "k8s-nodes/001-resources_list"},
			[]string{diskQuestion, "k8s-nodes"}, []string{"node3"}},
		{"node-12345 from a real run", node12345Answer, []string{nodeTable},
			[]string{diskQuestion, "k8s-nodes"}, []string{"node-12345"}},
		{"node-01 list in a live question", nodeListAnswer, []string{nodeTable},
			[]string{"How many nodes are there?"}, []string{"node-01", "node-02", "node-13"}},
		{"names present in evidence", "- homelab-k8s-1-cp (control-plane) - Ready\n- **homelab-k8s-1-gpu-worker**",
			[]string{nodeTable}, nil, nil},
		{"name present only in the question", "The node **node-7** is mentioned.", []string{nodeTable},
			[]string{"Is node-7 healthy?"}, nil},
		{"ns/name present in evidence", "kube-system/coredns-5d78c9869d-abcde is running\n- `kube-system/coredns-5d78c9869d-abcde`",
			[]string{"NAMESPACE NAME\nkube-system coredns-5d78c9869d-abcde"}, nil, nil},
		{"ns/name with a missing pod", "- `kube-system/coredns-5d78c9869d-zzzzz`",
			[]string{"NAMESPACE NAME\nkube-system coredns-5d78c9869d-abcde"}, nil, []string{"coredns-5d78c9869d-zzzzz"}},
		{"media type is not a pair", "Content is `text/x-yaml` and `application/json`.", []string{"x"}, nil, nil},
		{"generic words and versions", "The **node** is `ready`. Version `v1.35.0`, `1.2.3`, `10.0.0.1`, `4gi`, `100m`, `13`.",
			[]string{"x"}, nil, nil},
		{"well-known names", "- kube-system\n- control-plane\n- `ephemeral-storage`", []string{"x"}, nil, nil},
		{"kind word followed by a name", "pod web-7 restarted, namespace staging-1 is quiet",
			[]string{"web-8"}, nil, []string{"web-7", "staging-1"}},
		{"kind word followed by a plain word", "node is ready, pods in trouble, service up", []string{"x"}, nil, nil},
		{"table first cell", "| Node | Status |\n|---|---|\n| worker-9 | Ready |\n| homelab-k8s-1-cp | Ready |",
			[]string{nodeTable}, nil, []string{"worker-9"}},
		{"bare names in a fenced block", "```\nworker-9\nhomelab-k8s-1-cp\nkubectl get nodes -o wide\n```",
			[]string{nodeTable}, nil, []string{"worker-9"}},
		{"placeholders are not names", "run `kubectl describe node <node-name>` or kubectl get pod $POD-1", []string{"x"}, nil, nil},
		{"unicode and markdown", "- **nöde-1** et `日本-2`; _worker-4_ — done", []string{"x"}, nil, nil},
		{"upper case is not a name", "- **Node-01** and `DiskPressure`", []string{"x"}, nil, nil},
		{"dotted hostnames are not claimed", "- `docs.example.com` and node-9.example.com", []string{"x"}, nil, nil},
		{"evidence hostname labels count", "- **ip-10-0-0-1**", []string{"ip-10-0-0-1.ec2.internal"}, nil, nil},
		{"english compounds are not names", "- high-memory pods\n- out-of-memory kills\npods in-flight, cluster-wide, **read/write-heavy**\n- x86-64 hosts and **x86-64**",
			[]string{"x"}, nil, nil},
		{"hyphen only in a weak position", "- **web-front** and node worker-pool", []string{"x"}, nil, nil},
		{"hyphen only on a line with a pod state", "- **cert-manager**: Unschedulable\n- **nginx-ingress**: ImagePullBackOff (image failure)\n- **redis**: Evicted",
			[]string{"cert-manager"}, nil, []string{"nginx-ingress"}},
		{"state word in prose names the token after a kind word", "pod web-front is CrashLoopBackOff", []string{"x"}, nil, []string{webFront}},
		{"state word in prose is English", "- **memory-hungry** pods are Pending\n- **long-term**: the fix is pending review\n" +
			"- **well-known** workloads keep terminating gracefully\n" +
			"Pods are Pending in staging, and the other option is **cost-effective** overall.\n" +
			"pod web-front is fine; the migration is pending", []string{"x"}, nil, nil},
		{"state word belongs to the name before it", "- **cert-manager**: Unschedulable and **nginx-ingress** is fine\n- **web-front** is fine. Meanwhile **api-gate** ImagePullBackOff",
			[]string{"x"}, nil, []string{"cert-manager", "api-gate"}},
		{"a later bold name does not lend its state to the lead", "- **web-front** is fine and **api-gate**: ImagePullBackOff",
			[]string{"x"}, nil, []string{"api-gate"}},
		{"status positions", "| web-front | Pending |\nstatus: Evicted for **redis-cache**\n- **batch-job**: Terminating",
			[]string{"x"}, nil, []string{"web-front", "batch-job"}},
		{"kubectl spelling only", "- **cache-warm**: imagepullbackoff\n- **cache-cold**: pending", []string{"x"}, nil, nil},
		{"english compounds stay quiet on a state line", "- **high-memory** pods are Pending\n- **long-running** jobs: Evicted\n- **in-flight** requests Terminating",
			[]string{"x"}, nil, nil},
		{"hyphen only in a strong position", "| web-front | Ready |\nrun `web-front`", []string{"x"}, nil, []string{webFront}},
		{"pair of two hyphenated sides is strong", "see `app-front/app-back` now", []string{"x"}, nil, []string{"app-front", "app-back"}},
		{"known left side needs a digit on the right", "`monitoring/grafana-ui` and `monitoring/grafana-2`", []string{"monitoring"}, nil, []string{"grafana-2"}},
		{"exact whole token only", "- **node-1**", []string{"node-12345 node-10"}, nil, []string{"node-1"}},
		{"owner name of a seen pod is seen", "- **gpu-swap-monitor** ran twice\n- `gpu-swap-monitor`",
			[]string{"gpu-swap-monitor-29914110-vmnfv Completed"}, nil, nil},
		{"owner name seen through the ignore set", "- **web-front-1**", []string{"x"}, []string{"web-front-1-abc12"}, nil},
		{"owner prefix needs a hyphen after it", "- **node-1**", []string{"node-12345 node1-abc"}, nil, []string{"node-1"}},
		{"a longer name is not seen from its owner", "- **gpu-swap-monitor-29914110-vmnfv**",
			[]string{"gpu-swap-monitor"}, nil, []string{"gpu-swap-monitor-29914110-vmnfv"}},
		{"names repeat once", "**node3** and **node3** and node3", []string{"x"}, nil, []string{"node3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Invented(tc.answer, evidenceOf(tc.evidence...), evidenceOf(tc.ignore...))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Invented = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInventedNilEvidenceChecksNothing(t *testing.T) {
	if got := Invented("**node3**", nil, nil); got != nil {
		t.Errorf("no evidence must not flag, got %v", got)
	}
}

func TestEvidenceRoundTripAndTruncation(t *testing.T) {
	e := evidenceOf("node-1 Node-2 a.b_c 12345 " + strings.Repeat("x", 80))
	got := EvidenceFromTokens(e.Tokens())
	for _, tok := range []string{"node-1", "node-2", "a", "b", "c"} {
		if !got.Has(tok) {
			t.Errorf("round trip lost %q in %v", tok, e.Tokens())
		}
	}
	if got.Has("12345") || got.Has(strings.Repeat("x", 80)) {
		t.Error("digits only and over-long tokens are not kept")
	}
	big := NewEvidence()
	for i := 0; i <= MaxTokens+5; i++ {
		big.put("n" + strings.Repeat("a", i%50) + string(rune('a'+i%26)) + itoa(i))
	}
	if !big.Truncated() || len(big.Tokens()) > MaxTokens {
		t.Errorf("truncation: truncated=%v tokens=%d", big.Truncated(), len(big.Tokens()))
	}
}

func itoa(i int) string {
	const digits = "abcdefghij"
	if i == 0 {
		return "a"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{digits[i%10]}, b...)
	}
	return string(b)
}

func TestAddRawTextKeepsBackslashWords(t *testing.T) {
	e := NewEvidence()
	e.Add(`image: \nginx:1.27 and C:\temp\node-7`)
	for _, tok := range []string{"nginx", "node-7", "temp"} {
		if !e.Has(tok) {
			t.Errorf("raw text lost %q: %v", tok, e.Tokens())
		}
	}
}

func TestAddJSONDecodesEscapes(t *testing.T) {
	e := NewEvidence()
	if !e.AddJSON([]byte(`{"items":[{"metadata":{"name":"node-01\nnode-02"}}],"n":3}`)) {
		t.Fatal("valid JSON refused")
	}
	for _, tok := range []string{"node-01", "node-02", "items", "metadata", "name"} {
		if !e.Has(tok) {
			t.Errorf("JSON lost %q: %v", tok, e.Tokens())
		}
	}
	if NewEvidence().AddJSON([]byte("NAME  STATUS")) {
		t.Error("a table is not JSON")
	}
}

func TestHasOwnerOf(t *testing.T) {
	e := evidenceOf("gpu-swap-monitor-29914110-vmnfv node-12345")
	for tok, want := range map[string]bool{
		"gpu-swap-monitor": true, "GPU-Swap-Monitor": true, "gpu-swap": true,
		"node-1": false, "node": true, "gpu-swap-monitor-29914110-vmnfv": false, "other": false, "": false,
	} {
		if got := e.HasOwnerOf(tok); got != want {
			t.Errorf("HasOwnerOf(%q) = %v, want %v", tok, got, want)
		}
	}
}
