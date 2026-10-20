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
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

const (
	calTestServer   = "ollama-gpu"
	calTestOperator = "kubemoot"
	calTestModelA   = "qwen3:8b"
	calTestModelB   = "llama3.2:3b"
	calTestVersion  = "v0-test"
)

func calServer(cal *aiv1alpha1.ServerCalibration, engine string) *aiv1alpha1.ModelServer {
	p := &aiv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: calTestServer, Namespace: "kubemoot"},
		Status:     aiv1alpha1.ModelServerStatus{Calibration: cal},
	}
	if engine != "" {
		p.Status.ServerInfo = &aiv1alpha1.ServerInfo{Version: engine}
	}
	return p
}

func calibrated(engine string, models ...string) *aiv1alpha1.ServerCalibration {
	done := metav1.Now()
	cal := &aiv1alpha1.ServerCalibration{Phase: "Calibrated", EngineVersion: engine, CompletedAt: &done}
	for _, m := range models {
		cal.Models = append(cal.Models, aiv1alpha1.ModelCalibration{Model: m, Fits: true})
	}
	return cal
}

func TestNeedOf(t *testing.T) {
	calibrating := &aiv1alpha1.ServerCalibration{Phase: "Calibrating"}
	reset := calServer(calibrated("0.12.0", calTestModelA), "0.12.0")
	reset.Annotations = map[string]string{calibrationResetAnnotation: "t2"}
	acted := calServer(calibrated("0.12.0", calTestModelA), "0.12.0")
	acted.Annotations = map[string]string{calibrationResetAnnotation: "t1"}
	acted.Status.Calibration.ResetToken = "t1"
	resetOnNew := calServer(nil, "")
	resetOnNew.Annotations = map[string]string{calibrationResetAnnotation: "t3"}

	cases := []struct {
		name    string
		p       *aiv1alpha1.ModelServer
		bound   []string
		key     string
		inWords string
	}{
		{"never calibrated", calServer(nil, "0.12.0"), nil, needKeyNew, "has not been calibrated"},
		{"calibrating with no results yet", calServer(calibrating, "0.12.0"), nil, needKeyNew, "has not been calibrated"},
		{"a reset comes first", reset, []string{calTestModelB}, "reset/t2", "reset was requested"},
		{"a reset on a new server", resetOnNew, nil, "reset/t3", "reset was requested"},
		{"a reset already acted on", acted, []string{calTestModelA}, "", ""},
		{"engine changed", calServer(calibrated("0.11.0", calTestModelA), "0.12.0"), nil,
			"engine/0.12.0", "from 0.11.0 to 0.12.0"},
		{"engine unknown now", calServer(calibrated("0.11.0", calTestModelA), ""), nil, "", ""},
		{"engine not recorded", calServer(calibrated("", calTestModelA), "0.12.0"), nil, "", ""},
		{"bound models missing", calServer(calibrated("0.12.0", calTestModelA), "0.12.0"),
			[]string{calTestModelB, calTestModelA, calTestModelB, "a:1b"}, "models/", "a:1b, llama3.2:3b"},
		{"every bound model measured", calServer(calibrated("0.12.0", calTestModelA), "0.12.0"),
			[]string{calTestModelA}, "", ""},
		{"calibrated with no models", calServer(calibrated("0.12.0"), "0.12.0"), nil, "", ""},
	}
	for _, c := range cases {
		got := needOf(c.p, c.bound)
		if !strings.HasPrefix(got.Key, c.key) || (c.key == "" && got.Key != "") {
			t.Errorf("%s: key %q, want prefix %q", c.name, got.Key, c.key)
		}
		if !strings.Contains(got.Reason, c.inWords) {
			t.Errorf("%s: reason %q, want it to contain %q", c.name, got.Reason, c.inWords)
		}
	}
	a := needOf(calServer(calibrated("0.12.0"), "0.12.0"), []string{calTestModelA, calTestModelB})
	b := needOf(calServer(calibrated("0.12.0"), "0.12.0"), []string{calTestModelB, calTestModelA})
	if a.Key != b.Key {
		t.Errorf("the models key depends on order: %q vs %q", a.Key, b.Key)
	}
}

func TestShouldAsk(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Minute))
	later := metav1.NewTime(time.Now())
	earlier := metav1.NewTime(time.Now().Add(-time.Hour))
	need := calibrationNeed{Key: needKeyNew, Reason: "r"}
	idle := func() (bool, error) { return true, nil }
	busy := func() (bool, error) { return false, nil }
	broken := func() (bool, error) { return false, errors.New("nats down") }
	withState := func(phase string, updated *metav1.Time, reqKey string) *aiv1alpha1.ModelServer {
		p := calServer(&aiv1alpha1.ServerCalibration{Phase: phase, UpdatedAt: updated}, "")
		if reqKey != "" {
			p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: reqKey, RequestedAt: asked}
		}
		return p
	}

	cases := []struct {
		name string
		p    *aiv1alpha1.ModelServer
		need calibrationNeed
		idle func() (bool, error)
		want bool
		err  bool
	}{
		{"no need", withState("", nil, ""), calibrationNeed{}, idle, false, false},
		{"never asked", withState("", nil, ""), need, busy, true, false},
		{"asked about another need", withState("Calibrated", &later, "engine/1"), need, busy, true, false},
		{"asked, in progress", withState("Calibrating", &later, needKeyNew), need, idle, false, false},
		{"asked, failed", withState("Failed", &later, needKeyNew), need, idle, false, false},
		{"paused before the request", withState(calibrationPhaseWaiting, &earlier, needKeyNew), need, idle, false, false},
		{"paused, still busy", withState(calibrationPhaseWaiting, &later, needKeyNew), need, busy, false, false},
		{"paused, now idle", withState(calibrationPhaseWaiting, &later, needKeyNew), need, idle, true, false},
		{"paused, idle unknown", withState(calibrationPhaseWaiting, &later, needKeyNew), need, broken, false, true},
	}
	for _, c := range cases {
		got, err := shouldAsk(c.p, c.need, c.idle)
		if got != c.want || (err != nil) != c.err {
			t.Errorf("%s: got %v err %v, want %v err %v", c.name, got, err, c.want, c.err)
		}
	}
}

// fakeCalibrator records the calibrations it is asked to start.
type fakeCalibrator struct {
	started  []string
	notReady bool
	fail     bool
	running  bool
	idle     bool
}

func (f *fakeCalibrator) Ready(context.Context) (bool, string) {
	return !f.notReady, "not ready"
}

func (f *fakeCalibrator) Calibrate(_ context.Context, server string) (bool, error) {
	if f.fail {
		return false, errors.New("refused")
	}
	if f.running {
		return false, nil
	}
	f.started = append(f.started, server)
	return true, nil
}

func (f *fakeCalibrator) Idle(string) (bool, error) { return f.idle, nil }

func calReconciler(t *testing.T, cal Calibrator, objs ...client.Object) *ModelServerReconciler {
	t.Helper()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &ModelServerReconciler{Client: cli, Scheme: scheme, Calibration: cal}
}

func TestRequestCalibration(t *testing.T) {
	ctx := context.Background()
	off := false
	model := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "crew-a"},
		Spec: aiv1alpha1.ModelSpec{Model: calTestModelB, ServerRef: calTestServer}}
	otherServer := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "o", Namespace: "crew-a"},
		Spec: aiv1alpha1.ModelSpec{Model: "other:1b", ServerRef: "elsewhere"}}
	disabled := calServer(nil, "")
	disabled.Spec.Calibration = &aiv1alpha1.ServerCalibrationSpec{Enabled: &off}

	cases := []struct {
		name    string
		cal     *fakeCalibrator
		objs    []client.Object
		server  *aiv1alpha1.ModelServer
		wantKey string
	}{
		{"calibration disabled", &fakeCalibrator{}, nil, disabled, ""},
		{"the server is not ready", &fakeCalibrator{notReady: true}, nil, calServer(nil, ""), ""},
		{"a new server", &fakeCalibrator{}, nil, calServer(nil, ""), needKeyNew},
		{"bound models", &fakeCalibrator{}, []client.Object{model, otherServer},
			calServer(calibrated("0.12.0", calTestModelA), "0.12.0"), "models/"},
		{"the call fails", &fakeCalibrator{fail: true}, nil, calServer(nil, ""), ""},
		{"a calibration is already running", &fakeCalibrator{running: true}, nil, calServer(nil, ""), ""},
	}
	for _, c := range cases {
		r := calReconciler(t, c.cal, c.objs...)
		r.requestCalibration(ctx, c.server)
		checkRequest(t, c.name, c.cal, c.server, c.wantKey)
		// The same need is not asked twice.
		before := len(c.cal.started)
		r.requestCalibration(ctx, c.server)
		if len(c.cal.started) != before {
			t.Errorf("%s: asked again for the same need: %v", c.name, c.cal.started)
		}
	}
}

// checkRequest asserts one calibration with a key starting wantKey was started
// and recorded, or none when wantKey is empty.
func checkRequest(t *testing.T, name string, cal *fakeCalibrator, p *aiv1alpha1.ModelServer, wantKey string) {
	t.Helper()
	req := p.Status.CalibrationRequest
	if wantKey == "" {
		if len(cal.started) != 0 || req != nil {
			t.Errorf("%s: started %v, request %+v; want none", name, cal.started, req)
		}
		return
	}
	if len(cal.started) != 1 || req == nil || !strings.HasPrefix(req.Key, wantKey) {
		t.Fatalf("%s: started %v, request %+v; want one %q request", name, cal.started, req, wantKey)
	}
	if req.Reason == "" || req.RequestedAt.IsZero() {
		t.Errorf("%s: request not recorded fully: %+v", name, req)
	}
}

// A calibration paused as Waiting is started again once the server is idle.
func TestRequestCalibration_resumesAPausedCalibrationWhenIdle(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Minute))
	paused := metav1.NewTime(time.Now())
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseWaiting, UpdatedAt: &paused}, "")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: asked}

	busy := &fakeCalibrator{}
	calReconciler(t, busy).requestCalibration(context.Background(), p)
	if len(busy.started) != 0 {
		t.Errorf("resumed while crew calls hold the server: %v", busy.started)
	}
	idle := &fakeCalibrator{idle: true}
	calReconciler(t, idle).requestCalibration(context.Background(), p)
	if len(idle.started) != 1 || !p.Status.CalibrationRequest.RequestedAt.After(asked.Time) {
		t.Errorf("did not resume once idle: %v %+v", idle.started, p.Status.CalibrationRequest)
	}
}

func TestRequestCalibration_withoutACalibrator(t *testing.T) {
	r := calReconciler(t, nil)
	p := calServer(nil, "")
	r.requestCalibration(context.Background(), p)
	if p.Status.CalibrationRequest != nil {
		t.Errorf("recorded a request with no way to calibrate: %+v", p.Status.CalibrationRequest)
	}
}

// calMCPServer is an MCP server over SSE whose calibrate_server tool records the
// servers it is asked about, or refuses them.
func calMCPServer(t *testing.T, refuse bool) (*httptest.Server, *[]string) {
	return calServerAnswering(t, refuse, `{"server":"x","started":true,"models":["a"]}`)
}

// calServerAnswering is calMCPServer whose tool answers with answer.
func calServerAnswering(t *testing.T, refuse bool, answer string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	server := mcp.NewServer(&mcp.Implementation{Name: "accelerator-mcp-test", Version: calTestVersion}, nil)
	server.AddTool(&mcp.Tool{Name: calibrationTool, InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				Server string `json:"server"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			mu.Lock()
			asked = append(asked, args.Server)
			mu.Unlock()
			if refuse {
				return &mcp.CallToolResult{IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: "server " + args.Server + " is not ready"}}}, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: answer}}}, nil
		})
	srv := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func calServerObject(endpoint string, ready bool) *aiv1alpha1.MCPServer {
	s := &aiv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: calibrationServer, Namespace: calTestOperator}}
	s.Status.Ready = ready
	s.Status.Endpoint = endpoint
	return s
}

func TestMCPCalibrator_ReadyAndCalibrate(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", calTestOperator)
	srv, asked := calMCPServer(t, false)
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(calServerObject(srv.URL+"/", true)).Build()
	c := MCPCalibrator{Client: cli, HTTPClient: srv.Client(), Version: calTestVersion}

	if ready, why := c.Ready(context.Background()); !ready {
		t.Fatalf("a ready server: %s", why)
	}
	if started, err := c.Calibrate(context.Background(), calTestServer); err != nil || !started {
		t.Fatalf("calibrate: started %v, err %v", started, err)
	}
	if !slices.Equal(*asked, []string{calTestServer}) {
		t.Errorf("the tool was asked about %v", *asked)
	}
}

func TestMCPCalibrator_refusalsAndAbsence(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", calTestOperator)
	srv, _ := calMCPServer(t, true)
	scheme := agentReconcileScheme(t)

	refusing := MCPCalibrator{Client: fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(calServerObject(srv.URL, true)).Build(), HTTPClient: srv.Client()}
	if _, err := refusing.Calibrate(context.Background(), calTestServer); err == nil ||
		!strings.Contains(err.Error(), "not ready") {
		t.Errorf("a refusal should carry the tool's reason: %v", err)
	}
	starting := MCPCalibrator{Client: fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(calServerObject("", false)).Build()}
	if ready, _ := starting.Ready(context.Background()); ready {
		t.Error("a server that is not ready reported ready")
	}
	absent := MCPCalibrator{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	if ready, why := absent.Ready(context.Background()); ready || why == "" {
		t.Errorf("no server: ready %v, why %q", ready, why)
	}
	if _, err := absent.Calibrate(context.Background(), calTestServer); err == nil {
		t.Error("calibrating with no server should fail")
	}
}

func TestMCPCalibrator_IdleWithoutNATS(t *testing.T) {
	for _, c := range []MCPCalibrator{{}, {Publisher: kubemootnats.NewPublisher("")}} {
		if idle, err := c.Idle(calTestServer); idle || !errors.Is(err, errNATSUnavailable) {
			t.Errorf("Idle without NATS: %v %v", idle, err)
		}
	}
}

func TestStartedRun(t *testing.T) {
	cases := []struct {
		answer  string
		started bool
		err     bool
	}{
		{`{"server":"p","started":true,"models":["m"]}`, true, false},
		{`{"server":"p","running":true}`, false, false},
		{`{"server":"p"}`, false, true},
		{`not json`, false, true},
	}
	for _, c := range cases {
		started, err := startedRun(c.answer)
		if started != c.started || (err != nil) != c.err {
			t.Errorf("%s: started %v err %v", c.answer, started, err)
		}
	}
}

// A run already going is reported as not started, so its need is asked again later.
func TestMCPCalibrator_aRunAlreadyGoing(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", calTestOperator)
	srv, _ := calServerAnswering(t, false, `{"server":"x","running":true}`)
	scheme := agentReconcileScheme(t)
	c := MCPCalibrator{Client: fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(calServerObject(srv.URL, true)).Build(), HTTPClient: srv.Client()}
	if started, err := c.Calibrate(context.Background(), calTestServer); err != nil || started {
		t.Errorf("running: started %v, err %v", started, err)
	}
}

// The request is stored by its own patch, so a later conflict on the reconciler's
// status update cannot lose it; what the reconcile discovered is kept in memory.
func TestRequestCalibration_storesTheRequestByItself(t *testing.T) {
	p := calServer(nil, "0.12.0")
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme, Calibration: &fakeCalibrator{}}
	inMemory := p.DeepCopy()
	inMemory.Status.Message = "discovered this reconcile"
	r.requestCalibration(context.Background(), inMemory)

	stored := &aiv1alpha1.ModelServer{}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(p), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.CalibrationRequest == nil || stored.Status.CalibrationRequest.Key != needKeyNew {
		t.Errorf("request not stored: %+v", stored.Status.CalibrationRequest)
	}
	if inMemory.Status.Message != "discovered this reconcile" || inMemory.Status.CalibrationRequest == nil {
		t.Errorf("the in-memory status lost what the reconcile found: %+v", inMemory.Status)
	}
}

// A calibration that Failed is asked again once the server becomes ready after it.
func TestShouldAsk_retriesAFailureAfterTheServerRecovers(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Hour))
	failed := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	recovered := metav1.NewTime(time.Now())
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &failed}, "")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: asked}
	need := calibrationNeed{Key: needKeyNew}
	never := func() (bool, error) { return false, nil }

	p.Status.Ready = true
	p.Status.Conditions = []metav1.Condition{{Type: conditionTypeReady, Status: metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(asked.Add(time.Minute))}}
	if ask, _ := shouldAsk(p, need, never); ask {
		t.Error("retried a failure on a server that has not changed since")
	}
	p.Status.Conditions[0].LastTransitionTime = recovered
	if ask, _ := shouldAsk(p, need, never); !ask {
		t.Error("did not retry a failure after the server became ready again")
	}
	p.Status.Ready = false
	if ask, _ := shouldAsk(p, need, never); ask {
		t.Error("retried on a server that is not ready")
	}
}
