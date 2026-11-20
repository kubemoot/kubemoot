/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	toolsPrefix  = "/tools/"
	smokeNS      = "smoke-crew"
	smokeGetTool = "get_nodes"
	smokeContent = "content"
	smokeNotJSON = "<html>"
	smokeOther   = "elsewhere-server"
	smokeBadTool = "bad-tool"
	smokeIsError = "isError"
	smokeProxmox = "proxmox"
	smokeToolsK  = "tools"
	smokeOtherT  = "other-tool"
	smokeKubNS   = "kubemoot-ns"
	smokeCrewRef = "smoke-crew-ref"
)

// fakeToolGateway serves GET /tools and POST /tools/{tool}/call the way the Kubemoot
// gateway does. answers maps a tool to its reply; a tool in slow sleeps first.
type fakeToolGateway struct {
	mu      sync.Mutex
	tools   []gatewayTool
	answers map[string]map[string]any
	slow    map[string]time.Duration
	called  []string
	listErr bool
}

func (g *fakeToolGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == toolsPrefix[:len(toolsPrefix)-1] {
		if g.listErr {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": len(g.tools), smokeToolsK: g.tools})
		return
	}
	tool := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, toolsPrefix), "/call")
	g.mu.Lock()
	g.called = append(g.called, tool)
	g.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if d := g.slow[tool]; d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return
		}
	}
	reply, ok := g.answers[tool]
	if !ok {
		reply = map[string]any{smokeIsError: false, smokeContent: "ok " + string(body)}
	}
	_ = json.NewEncoder(w).Encode(reply)
}

func smokeServer(name string, inputs ...kubemootv1alpha1.ToolSmokeInput) kubemootv1alpha1.MCPServer {
	return kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: smokeNS},
		Spec:       kubemootv1alpha1.MCPServerSpec{SmokeInputs: inputs},
	}
}

func smokeInput(tool string, args ...string) kubemootv1alpha1.ToolSmokeInput {
	in := kubemootv1alpha1.ToolSmokeInput{Name: tool}
	for i := 0; i+1 < len(args); i += 2 {
		if in.Arguments == nil {
			in.Arguments = map[string]string{}
		}
		in.Arguments[args[i]] = args[i+1]
	}
	return in
}

func rowFor(t *testing.T, rows []toolSmokeRow, server, tool string) toolSmokeRow {
	t.Helper()
	for _, r := range rows {
		if r.Server == server && r.Tool == tool {
			return r
		}
	}
	t.Fatalf("no row for %s/%s in %+v", server, tool, rows)
	return toolSmokeRow{}
}

func TestRunToolSmokePassesAndSkipsUndeclaredTools(t *testing.T) {
	gw := &fakeToolGateway{tools: []gatewayTool{
		{Name: smokeGetTool, ServerName: smokeProxmox}, {Name: "shutdown_vm", ServerName: smokeProxmox},
		{Name: smokeOtherT, ServerName: smokeOther},
	}}
	ts := httptest.NewServer(gw)
	defer ts.Close()

	rows := runToolSmoke(context.Background(), ts.Client(), ts.URL,
		[]kubemootv1alpha1.MCPServer{smokeServer(smokeProxmox, smokeInput(smokeGetTool, "node", "pve"))})

	if len(rows) != 2 {
		t.Fatalf("want 2 rows (other servers ignored), got %+v", rows)
	}
	called := rowFor(t, rows, smokeProxmox, smokeGetTool)
	if !called.Passed || !called.Invoked || called.Error != "" {
		t.Errorf("declared tool should be invoked and pass: %+v", called)
	}
	listedOnly := rowFor(t, rows, smokeProxmox, "shutdown_vm")
	if !listedOnly.Passed || listedOnly.Invoked {
		t.Errorf("tool without input should pass as listed only: %+v", listedOnly)
	}
	if len(gw.called) != 1 || gw.called[0] != smokeGetTool {
		t.Errorf("only the declared tool may be called, got %v", gw.called)
	}
}

func TestRunToolSmokeSendsDeclaredArguments(t *testing.T) {
	var got map[string]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{smokeToolsK: []gatewayTool{{Name: "t", ServerName: "s"}}})
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"isError":false,"content":"x"}`))
	}))
	defer srv.Close()
	rows := runToolSmoke(context.Background(), srv.Client(), srv.URL,
		[]kubemootv1alpha1.MCPServer{smokeServer("s", smokeInput("t", "namespace", "kubemoot-ns", "limit", "3"))})
	if !rows[0].Passed {
		t.Fatalf("want pass, got %+v", rows)
	}
	if got["arguments"]["namespace"] != smokeKubNS || got["arguments"]["limit"] != "3" {
		t.Errorf("arguments not forwarded: %v", got)
	}
}

func TestRunToolSmokeToolErrorFails(t *testing.T) {
	gw := &fakeToolGateway{
		tools:   []gatewayTool{{Name: smokeGetTool, ServerName: smokeProxmox}},
		answers: map[string]map[string]any{smokeGetTool: {smokeIsError: true, smokeContent: "proxmox-secret missing"}},
	}
	ts := httptest.NewServer(gw)
	defer ts.Close()
	rows := runToolSmoke(context.Background(), ts.Client(), ts.URL,
		[]kubemootv1alpha1.MCPServer{smokeServer(smokeProxmox, smokeInput(smokeGetTool))})
	r := rowFor(t, rows, smokeProxmox, smokeGetTool)
	if r.Passed || !strings.Contains(r.Error, "proxmox-secret missing") {
		t.Errorf("an isError reply must fail with its content: %+v", r)
	}
}

func TestRunToolSmokeUnlistedDeclaredToolFails(t *testing.T) {
	gw := &fakeToolGateway{tools: []gatewayTool{{Name: "real", ServerName: "s"}}}
	ts := httptest.NewServer(gw)
	defer ts.Close()
	rows := runToolSmoke(context.Background(), ts.Client(), ts.URL,
		[]kubemootv1alpha1.MCPServer{smokeServer("s", smokeInput("ghost"))})
	ghost := rowFor(t, rows, "s", "ghost")
	if ghost.Passed || ghost.Invoked || !strings.Contains(ghost.Error, "does not list") {
		t.Errorf("a declared tool the gateway does not list must fail uncalled: %+v", ghost)
	}
	if len(gw.called) != 0 {
		t.Errorf("an unlisted tool must not be called, got %v", gw.called)
	}
}

func TestRunToolSmokeTimeoutFails(t *testing.T) {
	gw := &fakeToolGateway{
		tools: []gatewayTool{{Name: "slow", ServerName: "s"}},
		slow:  map[string]time.Duration{"slow": 2 * time.Second},
	}
	ts := httptest.NewServer(gw)
	defer ts.Close()
	in := smokeInput("slow")
	in.Timeout = &metav1.Duration{Duration: 50 * time.Millisecond}
	start := time.Now()
	rows := runToolSmoke(context.Background(), ts.Client(), ts.URL, []kubemootv1alpha1.MCPServer{smokeServer("s", in)})
	r := rowFor(t, rows, "s", "slow")
	if r.Passed || !strings.Contains(r.Error, "no answer within") {
		t.Errorf("a slow tool must fail with a timeout: %+v", r)
	}
	if time.Since(start) > time.Second {
		t.Errorf("the smoke must stop at the tool's timeout, took %s", time.Since(start))
	}
}

func TestRunToolSmokeServerWithNoToolsFails(t *testing.T) {
	ts := httptest.NewServer(&fakeToolGateway{})
	defer ts.Close()
	srv := smokeServer(smokeProxmox)
	srv.Status.Phase = "Error"
	srv.Status.Message = "secret proxmox-secret not found"
	rows := runToolSmoke(context.Background(), ts.Client(), ts.URL, []kubemootv1alpha1.MCPServer{srv})
	if len(rows) != 1 || rows[0].Passed || rows[0].Tool != "tools/list" ||
		!strings.Contains(rows[0].Error, "proxmox-secret") {
		t.Errorf("a server with no listed tools must fail naming its status: %+v", rows)
	}
}

func TestRunToolSmokeListingFailureFailsEveryServer(t *testing.T) {
	ts := httptest.NewServer(&fakeToolGateway{listErr: true})
	defer ts.Close()
	rows := runToolSmoke(context.Background(), ts.Client(), ts.URL,
		[]kubemootv1alpha1.MCPServer{smokeServer("b"), smokeServer("a")})
	if len(rows) != 2 || rows[0].Server != "a" || rows[0].Passed || rows[1].Passed {
		t.Errorf("both servers must fail, sorted: %+v", rows)
	}
}

func TestToolCallErrorCases(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		body    string
		wantErr bool
	}{
		{"ok", 200, `{"isError":false,"content":"x"}`, false},
		{"empty content is still not an error", 200, `{"isError":false,"content":""}`, false},
		{"isError", 200, `{"isError":true,"content":"boom"}`, true},
		{"http error", 500, `{}`, true},
		{"html reply", 200, smokeNotJSON, true},
	}
	for _, c := range cases {
		err := toolCallError(c.code, http.StatusText(c.code), []byte(c.body))
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestBuildToolSmokeStatusFailuresFirstAndCapped(t *testing.T) {
	var rows []toolSmokeRow
	for i := 0; i < kubemootv1alpha1.MaxToolSmokeResults+20; i++ {
		rows = append(rows, toolSmokeRow{Server: "s", Tool: "ok", Passed: true})
	}
	rows = append(rows, toolSmokeRow{Server: "s", Tool: smokeBadTool, Error: "x"})
	st := buildToolSmokeStatus(rows)
	if st.Passed || st.Failures != 1 || len(st.Results) != kubemootv1alpha1.MaxToolSmokeResults {
		t.Fatalf("unexpected status: passed=%t failures=%d rows=%d", st.Passed, st.Failures, len(st.Results))
	}
	if st.Results[0].Tool != smokeBadTool {
		t.Errorf("failures must come first, got %+v", st.Results[0])
	}
	if !buildToolSmokeStatus(nil).Passed {
		t.Errorf("no rows is a pass")
	}
}

// gateReconciler builds a reconciler over a fake client holding objs, whose gateway
// calls go to handler regardless of host.
func gateReconciler(t *testing.T, handler http.Handler, objs ...runtime.Object) *CrewFitnessSuiteReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitnessSuite{}).Build()
	return &CrewFitnessSuiteReconciler{Client: c, Scheme: scheme, smokeClient: &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			return rec.Result(), nil
		})}}
}

func gateSuite() *kubemootv1alpha1.CrewFitnessSuite {
	return &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "suite", Namespace: smokeNS},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: smokeCrewRef},
	}
}

func gateGateway() *kubemootv1alpha1.MCPGateway {
	return &kubemootv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: smokeNS}}
}

func TestToolSmokeGateFailureEndsSuiteBeforeAnyModel(t *testing.T) {
	gw := &fakeToolGateway{
		tools:   []gatewayTool{{Name: smokeGetTool, ServerName: smokeProxmox}},
		answers: map[string]map[string]any{smokeGetTool: {smokeIsError: true, smokeContent: "no secret"}},
	}
	srv := smokeServer(smokeProxmox, smokeInput(smokeGetTool))
	suite := gateSuite()
	r := gateReconciler(t, gw, &srv, gateGateway(), suite)

	_, stop, err := r.toolSmokeGate(context.Background(), suite)
	if err != nil || !stop {
		t.Fatalf("want stop, got stop=%t err=%v", stop, err)
	}
	if suite.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseToolSmokeFailed || suite.Status.CompletedAt == nil {
		t.Errorf("phase/completedAt not set: %+v", suite.Status)
	}
	assertSmokeFailedCondition(t, suite)
	if suite.Status.ToolSmoke == nil || suite.Status.ToolSmoke.Failures != 1 || suite.Status.IterationsTotal != 0 {
		t.Errorf("table missing or iterations scheduled: %+v", suite.Status)
	}
}

func assertSmokeFailedCondition(t *testing.T, suite *kubemootv1alpha1.CrewFitnessSuite) {
	t.Helper()
	cond := meta.FindStatusCondition(suite.Status.Conditions, kubemootv1alpha1.ConditionToolSmoke)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != kubemootv1alpha1.ToolSmokeReasonFailed ||
		!strings.Contains(cond.Message, "proxmox/get_nodes") {
		t.Errorf("condition must name the failure: %+v", cond)
	}
}

func TestToolSmokeGatePassLetsSuiteContinue(t *testing.T) {
	gw := &fakeToolGateway{tools: []gatewayTool{{Name: "t", ServerName: "s"}}}
	srv := smokeServer("s", smokeInput("t"))
	suite := gateSuite()
	r := gateReconciler(t, gw, &srv, gateGateway(), suite)
	_, stop, err := r.toolSmokeGate(context.Background(), suite)
	if err != nil || stop {
		t.Fatalf("a passing smoke must not stop the suite: stop=%t err=%v", stop, err)
	}
	cond := meta.FindStatusCondition(suite.Status.Conditions, kubemootv1alpha1.ConditionToolSmoke)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kubemootv1alpha1.ToolSmokeReasonPassed {
		t.Errorf("condition: %+v", cond)
	}
	if suite.Status.ToolSmoke == nil || !suite.Status.ToolSmoke.Passed {
		t.Errorf("table: %+v", suite.Status.ToolSmoke)
	}
}

func TestToolSmokeGateSkip(t *testing.T) {
	failing := &fakeToolGateway{listErr: true}
	srv := smokeServer("s")
	skipped := gateSuite()
	skipped.Spec.SkipToolSmoke = true
	r := gateReconciler(t, failing, &srv, gateGateway(), skipped)
	if _, stop, err := r.toolSmokeGate(context.Background(), skipped); stop || err != nil {
		t.Fatalf("skip must not stop: stop=%t err=%v", stop, err)
	}
	if c := meta.FindStatusCondition(skipped.Status.Conditions, kubemootv1alpha1.ConditionToolSmoke); c == nil ||
		c.Reason != kubemootv1alpha1.ToolSmokeReasonSkipped || skipped.Status.ToolSmoke != nil {
		t.Errorf("skip must record the reason and no table: %+v", c)
	}
	if len(failing.called) != 0 {
		t.Errorf("a skipped smoke must not call the gateway")
	}

}

func TestToolSmokeGateNoServers(t *testing.T) {
	failing := &fakeToolGateway{listErr: true}
	empty := gateSuite()
	r := gateReconciler(t, failing, empty)
	if _, stop, err := r.toolSmokeGate(context.Background(), empty); stop || err != nil {
		t.Fatalf("a namespace with no MCPServer must pass: stop=%t err=%v", stop, err)
	}
	if c := meta.FindStatusCondition(empty.Status.Conditions, kubemootv1alpha1.ConditionToolSmoke); c == nil ||
		c.Reason != kubemootv1alpha1.ToolSmokeReasonNoServers {
		t.Errorf("condition: %+v", c)
	}
}

func TestToolSmokeGateNoGatewayFails(t *testing.T) {
	srv := smokeServer("s")
	suite := gateSuite()
	r := gateReconciler(t, &fakeToolGateway{}, &srv, suite)
	_, stop, err := r.toolSmokeGate(context.Background(), suite)
	if err != nil || !stop || suite.Status.ToolSmoke == nil ||
		!strings.Contains(suite.Status.ToolSmoke.Results[0].Error, "no MCPGateway") {
		t.Errorf("servers with no gateway must fail: stop=%t err=%v %+v", stop, err, suite.Status.ToolSmoke)
	}
}

func TestDispatchPhaseToolSmokeFailedIsFinal(t *testing.T) {
	suite := gateSuite()
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseToolSmokeFailed
	r := gateReconciler(t, &fakeToolGateway{}, suite)
	res, err := r.dispatchPhase(context.Background(), suite)
	if err != nil || !res.IsZero() {
		t.Errorf("a smoke-failed suite must neither requeue nor error: %+v %v", res, err)
	}
}

func TestStartSuiteToolSmokeFailureStartsNothing(t *testing.T) {
	gw := &fakeToolGateway{
		tools:   []gatewayTool{{Name: smokeGetTool, ServerName: "s"}},
		answers: map[string]map[string]any{smokeGetTool: {smokeIsError: true, smokeContent: "down"}},
	}
	srv := smokeServer("s", smokeInput(smokeGetTool))
	suite := gateSuite()
	suite.Spec.Iterations = 1
	suite.Spec.Scripts = []kubemootv1alpha1.SuiteScript{{TestRef: "a", TestContent: "x"}}
	r := gateReconciler(t, gw, &srv, gateGateway(), suite)

	if _, err := r.startSuite(context.Background(), suite); err != nil {
		t.Fatal(err)
	}
	st := suite.Status
	if st.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseToolSmokeFailed || st.RunID != "" || st.StartedAt != nil || st.IterationsTotal != 0 {
		t.Errorf("a failed smoke must leave no run: %+v", st)
	}
	if !isSuiteTerminal(st.Phase) || !judgeSkipped(suite) {
		t.Errorf("ToolSmokeFailed must be terminal and unjudged")
	}
	if judgePhaseFor(suite, false) != kubemootv1alpha1.FitnessJudgePhaseSkipped {
		t.Errorf("judge phase must be Skipped")
	}
}

func TestStartSuiteToolSmokePassStartsRun(t *testing.T) {
	gw := &fakeToolGateway{tools: []gatewayTool{{Name: "t", ServerName: "s"}}}
	srv := smokeServer("s", smokeInput("t"))
	suite := gateSuite()
	suite.Spec.Iterations = 1
	suite.Spec.Scripts = []kubemootv1alpha1.SuiteScript{{TestRef: "a", TestContent: "x"}}
	r := gateReconciler(t, gw, &srv, gateGateway(), suite)
	if _, err := r.startSuite(context.Background(), suite); err != nil {
		t.Fatal(err)
	}
	if suite.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseRunning || suite.Status.StartedAt == nil {
		t.Errorf("a passing smoke must let the suite start: %+v", suite.Status)
	}
}

func TestToolSmokeRetryDoesNotCallToolsAgain(t *testing.T) {
	gw := &fakeToolGateway{tools: []gatewayTool{{Name: "t", ServerName: "s"}}}
	srv := smokeServer("s", smokeInput("t"))
	suite := gateSuite()
	r := gateReconciler(t, gw, &srv, gateGateway(), suite)
	for i := 0; i < 2; i++ {
		if _, stop, err := r.toolSmokeGate(context.Background(), suite); stop || err != nil {
			t.Fatalf("round %d: stop=%t err=%v", i, stop, err)
		}
	}
	if len(gw.called) != 1 {
		t.Errorf("the second pass must reuse the recorded outcome, tools called %d times", len(gw.called))
	}
}

func TestToolSmokeFailedStatusUpdateErrorIsReturned(t *testing.T) {
	gw := &fakeToolGateway{listErr: true}
	srv := smokeServer("s")
	suite := gateSuite()
	r := gateReconciler(t, gw, &srv, gateGateway(), suite)
	scheme := r.Scheme
	r.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(&srv, gateGateway(), suite).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitnessSuite{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return context.DeadlineExceeded
		}}).Build()
	if _, stop, err := r.toolSmokeGate(context.Background(), suite); err == nil || !stop {
		t.Errorf("a failed status write must come back as an error: stop=%t err=%v", stop, err)
	}
}

func TestNewSmokeCallClampsTimeout(t *testing.T) {
	in := smokeInput("t")
	in.Timeout = &metav1.Duration{Duration: time.Hour}
	call := newSmokeCall(in)
	if call.timeout != toolSmokeMaxTimeout {
		t.Errorf("timeout must clamp to %s: %v", toolSmokeMaxTimeout, call)
	}
}

func TestExecuteSmokeCallsCapsParallelism(t *testing.T) {
	var live, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := live.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		live.Add(-1)
		_, _ = w.Write([]byte(`{"isError":false,"content":""}`))
	}))
	defer srv.Close()
	items := make([]smokeItem, 30)
	for i := range items {
		items[i].call = &smokeCall{tool: "t", args: map[string]any{}, timeout: time.Second}
	}
	executeSmokeCalls(context.Background(), srv.Client(), srv.URL, items)
	if peak.Load() > toolSmokeParallelism {
		t.Errorf("peak %d calls in flight, cap %d", peak.Load(), toolSmokeParallelism)
	}
	for i := range items {
		if !items[i].result.Passed {
			t.Fatalf("item %d failed: %+v", i, items[i].result)
		}
	}
}
