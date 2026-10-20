package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/cluster"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/probe"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/store"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/storetest"
)

const (
	gpu       = "gpu-a"
	model     = "qwen3:14b"
	holder    = "kubemoot/accelerator-mcp/pod-a"
	version   = "0.12.0"
	argServer = "server"
	argPh     = "phase"
	argFits   = "fits"
	argMod    = "model"
	argPr     = "probe"
	argLoad   = "loadMs"
	argMods   = "models"
)

var t0 = time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC)

func now() time.Time { return t0 }

func check(t *testing.T, what string, ok bool, got any) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: %+v", what, got)
	}
}

func readyServer() *aiv1alpha1.ModelServer {
	p := &aiv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: gpu, Namespace: opNS},
		Spec:       aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeOllama, Endpoint: "http://gpu-a"},
	}
	p.Status.Ready = true
	p.Status.ServerInfo = &aiv1alpha1.ServerInfo{Version: version}
	p.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{MaxParallel: 2, VRAMTotalMiB: 24_000, ContextLength: 16384,
		AvailableModels: []aiv1alpha1.AvailableModel{{Name: model, SizeBytes: 9_000 * 1024 * 1024}}}
	return p
}

type fixture struct {
	set    *Set
	client client.Client
	kv     *storetest.MemKV
	eng    *storetest.Engine
}

func newFixture(t *testing.T, objects ...client.Object) fixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithStatusSubresource(&aiv1alpha1.ModelServer{}).Build()
	kv, eng := storetest.NewMemKV(), storetest.NewEngine()
	engineFor := func(string) engine.Engine { return eng }
	tickets := store.Tickets{KV: kv, Holder: holder, Now: now}
	history := store.History{KV: kv, Agent: holder, Now: now}
	set := &Set{
		Cluster: cluster.Cluster{Client: c},
		Prober: &probe.Prober{Engine: engineFor, Tickets: tickets, History: history,
			Prompts: probe.DefaultPrompts(), Now: now},
		Tickets: tickets, History: history, Engine: engineFor, Now: now, Jobs: &Jobs{},
	}
	return fixture{set: set, client: c, kv: kv, eng: eng}
}

func call(t *testing.T, s *Set, tool string, args any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := s.Call(context.Background(), tool, raw)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("result is not JSON: %s", out)
	}
	return result, nil
}

func TestListServers_summarizesEach_andShowsAResetRequest(t *testing.T) {
	p := readyServer()
	p.Annotations = map[string]string{cluster.AnnotationReset: "t1", cluster.AnnotationResetScope: resetScopeAll}
	f := newFixture(t, p)

	out, err := call(t, f.set, ToolListServers, nil)
	check(t, "no error", err == nil, err)
	servers := out["servers"].([]any)
	first := servers[0].(map[string]any)
	check(t, "summary", first["name"] == gpu && first["engineVersion"] == version &&
		first["memoryMiB"] == float64(24_000) && first["usableMiB"] == float64(21_120) &&
		first["accelerator"] == true && first["maxParallel"] == float64(2), first)
	reset := first["resetRequested"].(map[string]any)
	check(t, "reset", reset["token"] == "t1" && reset["scope"] == resetScopeAll, reset)
}

func TestServerState_showsLoadedCallsAvailableAndBound(t *testing.T) {
	bound := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "crew-a"},
		Spec: aiv1alpha1.ModelSpec{ServerRef: gpu, Model: model}}
	f := newFixture(t, readyServer(), bound)
	f.eng.Resident[model] = engine.Resident{Model: model, SizeMiB: 9_000, VRAMMiB: 9_000, ContextLength: 16384}
	_ = f.kv.PutKVValue(store.TicketsBucket, gpu+"__c", []byte(`{"holderId":"crew/a","modelName":"qwen3:14b"}`))

	out, err := call(t, f.set, ToolServerState, map[string]string{argServer: gpu})
	check(t, "no error", err == nil, err)
	check(t, "loaded", len(out["loaded"].([]any)) == 1, out["loaded"])
	check(t, "other calls", out["otherCalls"] == float64(1) && len(out["modelsInUse"].([]any)) == 1, out)
	check(t, "available and bound", len(out["available"].([]any)) == 1 && len(out["boundModels"].([]any)) == 1, out)
}

func TestProbeModel_buildsTheRequestFromTheServer(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000

	out, err := call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: model, argPr: probe.KindLoad})
	check(t, "load", err == nil && out["loadMs"] == float64(3000) && out["fits"] == true, out)

	out, err = call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: model, argPr: probe.KindParallel})
	check(t, "parallel uses the server's slots", err == nil && out["parallelRequests"] == float64(2), out)
}

func TestProbeModel_rejectsBadInput_andAServerNotReady(t *testing.T) {
	notReady := readyServer()
	notReady.Status.Ready = false
	f := newFixture(t, notReady)

	_, err := call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: model})
	check(t, "probe required", err != nil && strings.Contains(err.Error(), "probe"), err)
	_, err = call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: model, argPr: probe.KindLoad})
	check(t, "not ready", err != nil && strings.Contains(err.Error(), "not ready"), err)
	_, err = call(t, f.set, ToolProbeModel, map[string]string{argServer: "nope", argMod: model, argPr: probe.KindLoad})
	check(t, "unknown server", err != nil, err)
	_, err = f.set.Call(context.Background(), ToolProbeModel, json.RawMessage(`{"server":`))
	check(t, "bad JSON", err != nil, err)
}

func getServer(t *testing.T, f fixture) *aiv1alpha1.ModelServer {
	t.Helper()
	p := &aiv1alpha1.ModelServer{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: "kubemoot", Name: gpu}, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// checkPhase asserts the calibration is in phase and stamped with when it was recorded.
func checkPhase(t *testing.T, cal *aiv1alpha1.ServerCalibration, phase string) {
	t.Helper()
	check(t, phase, cal != nil && cal.Phase == phase && cal.UpdatedAt != nil, cal)
}

func TestRecordCalibration_startThenFinish(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	_, _ = call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrating,
		"message": "measuring qwen3:14b"})
	checkPhase(t, getServer(t, f).Status.Calibration, PhaseCalibrating)

	_, _ = call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: model, argPr: probe.KindLoad})
	out, err := call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrated,
		"message": "measured every model", argMods: []map[string]any{{argMod: model, argFits: true, argLoad: 3000}}})
	check(t, "recorded", err == nil && len(out["unloaded"].([]any)) == 1, out)

	cal := getServer(t, f).Status.Calibration
	check(t, "status", cal.Phase == PhaseCalibrated && cal.CompletedAt != nil && cal.EngineVersion == version &&
		len(cal.Models) == 1 && cal.Models[0].LoadMs == 3000 && cal.Models[0].CalibratedAt != nil, cal)
	var condition *metav1.Condition
	for i, c := range getServer(t, f).Status.Conditions {
		if c.Type == cluster.ConditionCalibrated {
			condition = &getServer(t, f).Status.Conditions[i]
		}
	}
	check(t, "Calibrated condition true", condition != nil && condition.Status == metav1.ConditionTrue, condition)
}

func TestRecordCalibration_aResetOfScopeAll_forgetsHistory_andHonorsTheToken(t *testing.T) {
	p := readyServer()
	p.Annotations = map[string]string{cluster.AnnotationReset: "t2", cluster.AnnotationResetScope: resetScopeAll}
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: PhaseCalibrated,
		Models: []aiv1alpha1.ModelCalibration{{Model: "old:1b"}}}
	f := newFixture(t, p)
	_ = store.History{KV: f.kv, Agent: "crew/agent", Now: now}.Record(gpu, model,
		[]store.Sample{{Kind: "triage", Ok: true}})

	out, _ := call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrating})
	check(t, "history forgotten", out["historyForgotten"] == true && len(f.kv.Keys(store.PerformanceBucket)) == 0, out)
	check(t, "old results cleared", len(getServer(t, f).Status.Calibration.Models) == 0, nil)

	_, _ = call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrated})
	check(t, "token honored", getServer(t, f).Status.Calibration.ResetToken == "t2", nil)
	listed, _ := call(t, f.set, ToolListServers, nil)
	first := listed["servers"].([]any)[0].(map[string]any)
	check(t, "no reset pending", first["resetRequested"] == nil, first)
}

func TestRecordCalibration_mergesAPartialRun_andRejectsABadPhase(t *testing.T) {
	p := readyServer()
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: PhaseCalibrated,
		Models: []aiv1alpha1.ModelCalibration{{Model: "qwen3:8b", Fits: true}, {Model: model, LoadMs: 1}}}
	f := newFixture(t, p)

	_, err := call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseWaiting,
		argMods: []map[string]any{{argMod: model, argFits: true, argLoad: 2}, {argMod: "new:1b", argFits: true}}})
	cal := getServer(t, f).Status.Calibration
	check(t, "merged", err == nil && cal.Phase == PhaseWaiting && len(cal.Models) == 3 && cal.Models[1].LoadMs == 2 &&
		cal.CompletedAt == nil, cal)

	_, err = call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: "Done"})
	check(t, "bad phase", err != nil, err)
	_, err = call(t, f.set, ToolRecordCalibration, map[string]any{argPh: PhaseFailed})
	check(t, "server required", err != nil, err)
}

func TestCall_unknownTool(t *testing.T) {
	f := newFixture(t)
	_, err := f.set.Call(context.Background(), "warm_everything", nil)
	check(t, "unknown tool", err != nil, err)
}

func TestSpecifications_namesTheSixTools(t *testing.T) {
	f := newFixture(t)
	specs := f.set.Specifications()
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s["name"].(string))
		check(t, "every spec has a schema and description", s["inputSchema"] != nil && s["description"] != "", s)
	}
	check(t, "six tools", strings.Join(names, ",") ==
		"list_servers,server_state,probe_model,calibrate_server,observed_performance,record_calibration", names)
}

func TestProbeModel_refusesAServerWithCalibrationOff(t *testing.T) {
	off := false
	p := readyServer()
	p.Spec.Calibration = &aiv1alpha1.ServerCalibrationSpec{Enabled: &off}
	f := newFixture(t, p)

	_, err := call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: model, argPr: probe.KindLoad})
	check(t, "refused", err != nil && strings.Contains(err.Error(), "calibration.enabled"), err)
	check(t, "nothing sent", f.eng.Generates == 0, f.eng.Generates)
	listed, _ := call(t, f.set, ToolListServers, nil)
	first := listed["servers"].([]any)[0].(map[string]any)
	check(t, "listed as off", first["calibrationEnabled"] == false, first)
}

func TestProbeModel_refusesAModelTheServerHasNotReported(t *testing.T) {
	f := newFixture(t, readyServer())
	_, err := call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: "mystery:70b", argPr: probe.KindLoad})
	check(t, "refused", err != nil && strings.Contains(err.Error(), "downloaded"), err)
	check(t, "nothing sent", f.eng.Generates == 0, f.eng.Generates)
}

func TestArguments_areStrict(t *testing.T) {
	f := newFixture(t, readyServer())
	cases := map[string]any{
		"an unknown field": map[string]any{argServer: gpu, argMod: model, argPr: probe.KindLoad, "warm": true},
		"a model without fits": map[string]any{argServer: gpu, argPh: PhaseWaiting,
			argMods: []map[string]any{{argMod: model}}},
		"a model without a name": map[string]any{argServer: gpu, argPh: PhaseWaiting,
			argMods: []map[string]any{{argFits: true}}},
		"a negative measurement": map[string]any{argServer: gpu, argPh: PhaseWaiting,
			argMods: []map[string]any{{argMod: model, argFits: true, argLoad: -5}}},
		"a misspelled measurement": map[string]any{argServer: gpu, argPh: PhaseWaiting,
			argMods: []map[string]any{{argMod: model, argFits: true, "acceleratorMiB": 5}}},
	}
	for name, args := range cases {
		tool := ToolRecordCalibration
		if name == "an unknown field" {
			tool = ToolProbeModel
		}
		_, err := call(t, f.set, tool, args)
		check(t, name+" is rejected", err != nil, err)
	}
	check(t, "nothing written", getServer(t, f).Status.Calibration == nil, getServer(t, f).Status.Calibration)
}

func TestRecordCalibration_resumingAfterAResetDoesNotWipeTheNewResults(t *testing.T) {
	p := readyServer()
	p.Annotations = map[string]string{cluster.AnnotationReset: "t5", cluster.AnnotationResetScope: resetScopeAll}
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: PhaseCalibrated,
		Models: []aiv1alpha1.ModelCalibration{{Model: "old:1b"}}}
	f := newFixture(t, p)
	result := []map[string]any{{argMod: model, argFits: true, argLoad: 3000}}

	first, _ := call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrating})
	_, _ = call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseWaiting, argMods: result})
	_ = store.History{KV: f.kv, Agent: holder, Now: now}.Record(gpu, model,
		[]store.Sample{{Kind: store.KindCalibration, Ok: true}})
	again, _ := call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrating})

	cal := getServer(t, f).Status.Calibration
	check(t, "the reset acted once", first["historyForgotten"] == true && again["historyForgotten"] == false, again)
	check(t, "the paused run's results stay", len(cal.Models) == 1 && cal.Models[0].Model == model &&
		cal.ResetToken == "t5", cal)
	check(t, "the new samples stay", len(f.kv.Keys(store.PerformanceBucket)) == 1, f.kv.Keys(store.PerformanceBucket))
}

func TestRecordCalibration_aFailedForgetLeavesTheResetPending(t *testing.T) {
	p := readyServer()
	p.Annotations = map[string]string{cluster.AnnotationReset: "t9", cluster.AnnotationResetScope: resetScopeAll}
	f := newFixture(t, p)
	f.kv.FailList = true

	_, err := call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrating})
	check(t, "error", err != nil, err)
	check(t, "nothing recorded, reset still pending", getServer(t, f).Status.Calibration == nil &&
		cluster.ResetRequested(getServer(t, f)), getServer(t, f).Status.Calibration)
}

func TestRecordCalibration_reportsAFailedUnload(t *testing.T) {
	f := newFixture(t, readyServer())
	_, _ = call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: model, argPr: probe.KindLoad})
	f.eng.FailUnload = true
	out, err := call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseFailed})
	check(t, "recorded with the unload error", err == nil && out["unloadError"] != nil, out)
}

func TestRecordCalibration_aResetDuringTheRunStaysPending(t *testing.T) {
	p := readyServer()
	p.Annotations = map[string]string{cluster.AnnotationReset: "t1"}
	f := newFixture(t, p)
	_, _ = call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrating})

	current := getServer(t, f)
	current.Annotations[cluster.AnnotationReset] = "t2"
	if err := f.client.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	_, _ = call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseCalibrated})
	check(t, "still pending", cluster.ResetRequested(getServer(t, f)), getServer(t, f).Status.Calibration)
}
