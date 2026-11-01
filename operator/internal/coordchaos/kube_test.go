/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tContainerID = "containerd://0123456789abcdef0123"

const podsJSON = `{"items":[
 {"metadata":{"name":"hello-coordinator-aaa","uid":"u1"},
  "spec":{"nodeName":"n1","containers":[{"name":"agent","env":[{"name":"KUBEMOOT_COORDINATOR_EVENT_LOG","value":"true"}]},{"name":"sidecar","env":[{"name":"X","value":"y"}]}]},
  "status":{"conditions":[{"type":"Ready","status":"True"}],
   "containerStatuses":[{"name":"agent","containerID":"containerd://0123456789abcdef0123","state":{"running":{}}}]}},
 {"metadata":{"name":"hello-coordinator-bbb","uid":"u2"},
  "spec":{"nodeName":"n2","containers":[{"name":"agent"}]},
  "status":{"conditions":[{"type":"Ready","status":"False"}],
   "containerStatuses":[{"name":"agent","state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}},
 {"metadata":{"name":"hello-coordinator-old","uid":"u3","deletionTimestamp":"2026-11-01T00:00:00Z"},"spec":{},"status":{}}]}`

func TestParsePods(t *testing.T) {
	pods, err := ParsePods([]byte(podsJSON))
	if err != nil || len(pods) != 2 {
		t.Fatalf("got %d pods, %v", len(pods), err)
	}
	checkPodA(t, pods[0])
	checkPodB(t, pods[1])
	if _, err := ParsePods([]byte("not json")); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func checkPodA(t *testing.T, a Pod) {
	t.Helper()
	if a.Name != "hello-coordinator-aaa" || a.Node != "n1" || !a.Ready || a.Env[envEventLog] != flagTrue || a.Env["X"] != "" {
		t.Errorf("pod a: %+v", a)
	}
}

func checkPodB(t *testing.T, b Pod) {
	t.Helper()
	if b.Ready || b.WaitingReason != "CrashLoopBackOff" {
		t.Errorf("pod b: %+v", b)
	}
}

func TestContainerHexID(t *testing.T) {
	if id, err := ContainerHexID(tContainerID); err != nil || id != "0123456789abcdef0123" {
		t.Fatalf("got %q %v", id, err)
	}
	for _, bad := range []string{"", "containerd://", "containerd://xyz; rm -rf /", "containerd://ABCDEF012345678"} {
		if _, err := ContainerHexID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

type recorded struct {
	calls [][]string
	out   map[string]string
}

func (r *recorded) exec(_ context.Context, _ []byte, args []string) ([]byte, error) {
	r.calls = append(r.calls, args)
	for k, v := range r.out {
		if strings.Contains(strings.Join(args, " "), k) {
			return []byte(v), nil
		}
	}
	return nil, nil
}

func newKube(r *recorded) *KubeCluster {
	k := Kubectl{Context: tContext, Kubeconfig: "/tmp/kc", Exec: r.exec}
	return &KubeCluster{K: k, Namespace: tHello, Agent: "hello-coordinator", Port: 8080,
		Signaler: &NodeSignaler{K: k, Namespace: "kubemoot-chaos", Image: "img"}}
}

func TestKubectlAlwaysNamesTheContext(t *testing.T) {
	r := &recorded{}
	c := newKube(r)
	if err := c.Kill(context.Background(), Pod{Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.calls[0], " ")
	want := "--kubeconfig /tmp/kc --context kind-chaos delete pod p1 -n hello --grace-period=0 --force --wait=false"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestSetReplicasReplicasAndPods(t *testing.T) {
	r := &recorded{out: map[string]string{"jsonpath": "2", "get pods": podsJSON}}
	c := newKube(r)
	if err := c.SetReplicas(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.calls[0], " "); !strings.Contains(got, `patch agent hello-coordinator -n hello --type merge -p {"spec":{"deployment":{"replicas":2}}}`) {
		t.Fatalf("got %q", got)
	}
	if n, err := c.Replicas(context.Background()); err != nil || n != 2 {
		t.Fatalf("replicas %d %v", n, err)
	}
	if pods, err := c.CoordinatorPods(context.Background()); err != nil || len(pods) != 2 {
		t.Fatalf("pods %d %v", len(pods), err)
	}
	if got := strings.Join(r.calls[len(r.calls)-1], " "); !strings.Contains(got, "-l kubemoot.ai/agent=hello-coordinator") {
		t.Fatalf("selector missing: %q", got)
	}
}

func TestReplicasDefaultsToOne(t *testing.T) {
	if n, err := newKube(&recorded{}).Replicas(context.Background()); err != nil || n != 1 {
		t.Fatalf("got %d %v", n, err)
	}
}

func TestConfiguredFlagsAndCounters(t *testing.T) {
	r := &recorded{out: map[string]string{
		"kubemootconfig": `{"spec":{"coordinator":{"eventLog":true,"resume":true,"lease":false}}}`,
		"--raw":          sampleMetrics,
	}}
	c := newKube(r)
	f, err := c.ConfiguredFlags(context.Background(), "default")
	if err != nil || f != (Flags{EventLog: true, Resume: true}) {
		t.Fatalf("got %+v %v", f, err)
	}
}

func TestHTTPGetReadsMetricsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/q/metrics" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, sampleMetrics)
	}))
	defer srv.Close()
	body, err := httpGet(context.Background(), srv.URL+"/q/metrics")
	if err != nil || ParseCounters(body)["fenced"] != 3 {
		t.Fatalf("got %v %v", ParseCounters(body), err)
	}
	if _, err := httpGet(context.Background(), srv.URL+"/missing"); err == nil {
		t.Fatal("a non-200 answer must be an error")
	}
}

func TestSignalCreatesOnePodPerNodeAndSignalsByContainerID(t *testing.T) {
	r := &recorded{}
	c := newKube(r)
	pod := Pod{Name: "p1", Node: "worker.1", ContainerID: tContainerID}
	for _, sig := range []string{sigStop, sigCont} {
		if err := c.Signal(context.Background(), pod, sig); err != nil {
			t.Fatal(err)
		}
	}
	applies, execs := 0, 0
	for _, call := range r.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, " apply -f -") {
			applies++
		}
		if strings.Contains(joined, " exec -n kubemoot-chaos chaos-signal-worker-1 -- sh -c ") {
			execs++
			if !strings.Contains(joined, "0123456789abcdef0123") {
				t.Errorf("container id missing: %s", joined)
			}
		}
	}
	if applies != 1 || execs != 2 {
		t.Fatalf("applies=%d execs=%d", applies, execs)
	}
}

func TestSignalRefusesOtherSignalsAndNonHexContainerIDs(t *testing.T) {
	c := newKube(&recorded{})
	pod := Pod{Name: "p1", Node: "n", ContainerID: tContainerID}
	if err := c.Signal(context.Background(), pod, "KILL"); err == nil {
		t.Fatal("only STOP and CONT are allowed")
	}
	if err := c.Signal(context.Background(), Pod{Node: "n", ContainerID: "x; reboot"}, sigStop); err == nil {
		t.Fatal("a non-hex container id was accepted")
	}
}

func TestSignalerManifestIsPrivilegedHostPIDOnTheNode(t *testing.T) {
	m := (&NodeSignaler{Namespace: "kubemoot-chaos", Image: "natsio/nats-box:0.14.5"}).manifest("chaos-signal-n1", "n1")
	for _, want := range []string{
		"hostPID: true", "privileged: true", "nodeName: n1", "image: natsio/nats-box:0.14.5",
		"pod-security.kubernetes.io/enforce: privileged", "activeDeadlineSeconds",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest lacks %q", want)
		}
	}
	if s := signalScript("abc123abc123", "STOP"); !strings.Contains(s, "kill -STOP $pids") || !strings.Contains(s, "exit 3") {
		t.Errorf("script: %s", s)
	}
}

func TestContinueAllDoesNothingWhenNoSignalerPodWasMade(t *testing.T) {
	r := &recorded{}
	c := newKube(r)
	c.Signaler.ContinueAll(context.Background(), []Pod{{Name: "p", Node: "n", ContainerID: tContainerID}})
	if len(r.calls) != 0 {
		t.Fatalf("made %d kubectl calls", len(r.calls))
	}
}

func TestStreamEventKind(t *testing.T) {
	cases := []struct {
		line, kind, errText string
		ok                  bool
	}{
		{`data: {"type":"synthesis","content":"Paris"}`, "synthesis", "", true},
		{`data:{"type":"error","error":"boom"}`, "error", tBoom, true},
		{`id: t:1`, "", "", false},
		{`data: not json`, "", "", false},
		{``, "", "", false},
	}
	for _, c := range cases {
		k, e, ok := StreamEventKind(c.line)
		if k != c.kind || e != c.errText || ok != c.ok {
			t.Errorf("%q: got %q %q %t", c.line, k, e, ok)
		}
	}
}

func TestStreamCountsSynthesesAndErrors(t *testing.T) {
	s := &stream{}
	s.apply(`data: {"type":"synthesis"}`)
	s.apply(`data: {"type":"phase"}`)
	s.apply(`data: {"type":"synthesis"}`)
	if s.Syntheses() != 2 || s.Err() != nil {
		t.Fatalf("count %d err %v", s.Syntheses(), s.Err())
	}
	s.apply(`data: {"type":"error","error":"gone"}`)
	if s.Err() == nil || !strings.Contains(s.Err().Error(), "gone") {
		t.Fatalf("err %v", s.Err())
	}
}

func TestAskPostsThroughTheGatewayProxy(t *testing.T) {
	r := &recorded{out: map[string]string{"create --raw": `{"conversationId":"c1","requestedAt":"2026-11-01T10:00:00Z"}`}}
	k := Kubectl{Context: tContext, Exec: r.exec, Bin: "true"}
	g := GatewayAsker{K: k, Namespace: tHello, Crew: "hello"}
	a, err := g.Ask(context.Background(), "q?")
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	if a.ConversationID() != "c1" {
		t.Fatalf("conversation %q", a.ConversationID())
	}
	if got := strings.Join(r.calls[0], " "); !strings.Contains(got, "/api/v1/namespaces/hello/services/hello-discussion:80/proxy/api/v1/discussions/hello") {
		t.Fatalf("path: %q", got)
	}
	bad := &recorded{out: map[string]string{"create --raw": `{"error":"no"}`}}
	if _, err := (GatewayAsker{K: Kubectl{Context: "c", Exec: bad.exec}, Namespace: "n", Crew: "c"}).Ask(context.Background(), "q"); err == nil {
		t.Fatal("a reply without a conversation was accepted")
	}
}
