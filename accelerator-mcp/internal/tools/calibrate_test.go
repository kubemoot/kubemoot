/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/cluster"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/probe"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/store"
)

const (
	crewNS = "crew-a"
	gpuB   = "gpu-b"
	opNS   = "kubemoot"
	small  = "small:1b"
	middle = "middle:4b"
	large  = "large:70b"
)

// calibrate calls calibrate_server and waits for the job it started to end.
func calibrate(t *testing.T, f fixture, args map[string]any) (map[string]any, error) {
	t.Helper()
	out, err := call(t, f.set, ToolCalibrateServer, args)
	f.set.Jobs.Wait()
	return out, err
}

func TestCalibrateServer_measuresEachModel_andRecordsTheResults(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000

	out, err := calibrate(t, f, map[string]any{argServer: gpu})
	check(t, "started", err == nil && out["started"] == true && len(out["models"].([]any)) == 1, out)

	cal := getServer(t, f).Status.Calibration
	check(t, "calibrated", cal.Phase == PhaseCalibrated && cal.CompletedAt != nil && len(cal.Models) == 1, cal)
	check(t, "every number copied from a probe", fullyMeasured(cal.Models[0]), cal.Models[0])
	check(t, "the model the recipe loaded is unloaded", slices.Contains(f.eng.Unloads, model), f.eng.Unloads)
}

// fullyMeasured reports whether the fixture's model has every recipe number: the
// fake engine loads in 3000 ms, keeps 9000 MiB resident with 8192 tokens of
// context, and generates at 50 tokens per second.
func fullyMeasured(m aiv1alpha1.ModelCalibration) bool {
	loaded := m.Model == model && m.Fits && m.LoadMs == 3000 && m.ResidentMiB == 9_000 && m.ContextLength == 8192
	speeds := m.GenerateTokensPerSecond == 50 && m.PromptTokensPerSecond > 0
	parallel := m.ParallelRequests == 2 && m.ParallelGenerateTokensPerSecond > 0
	return loaded && speeds && parallel
}

func TestCalibrateServer_aModelThatSpills_isRecordedAsNotFitting(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	f.eng.VRAMFraction = 0.5

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	cal := getServer(t, f).Status.Calibration
	check(t, "recorded as not fitting", err == nil && cal.Phase == PhaseCalibrated && len(cal.Models) == 1 &&
		!cal.Models[0].Fits && cal.Models[0].Reason != "", cal)
}

func TestCalibrateServer_pausesAsWaiting_whenCrewCallsTakeTheServer(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	f.eng.OnGenerate = func(n int) {
		if n == 1 { // a crew call arrives after the load
			_ = f.kv.PutKVValue(store.TicketsBucket, gpu+"__crew", []byte(`{"holderId":"crew/a"}`))
		}
	}

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	cal := getServer(t, f).Status.Calibration
	check(t, "waiting", err == nil && cal.Phase == PhaseWaiting && strings.Contains(cal.Message, "crew"), cal)
	check(t, "the model the recipe loaded is unloaded", slices.Contains(f.eng.Unloads, model), f.eng.Unloads)
}

func TestCalibrateServer_failsWhenNothingCanBeMeasured(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	f.eng.Fail = true

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	cal := getServer(t, f).Status.Calibration
	check(t, "failed", err == nil && cal.Phase == PhaseFailed && strings.Contains(cal.Message, model), cal)
}

func TestCalibrateServer_reportsAJobAlreadyRunning(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	entered, release := blockFirstGenerate(f)

	first, err := call(t, f.set, ToolCalibrateServer, map[string]any{argServer: gpu})
	check(t, "first starts", err == nil && first["started"] == true, first)
	<-entered
	second, err := call(t, f.set, ToolCalibrateServer, map[string]any{argServer: gpu})
	check(t, "second reports running", err == nil && second["running"] == true && second["started"] == nil, second)
	close(release)
	f.set.Jobs.Wait()
}

func withAvailable(sizes map[string]int64) *aiv1alpha1.ModelServer {
	p := readyServer()
	p.Status.Capacity.AvailableModels = nil
	for name, mib := range sizes {
		p.Status.Capacity.AvailableModels = append(p.Status.Capacity.AvailableModels,
			aiv1alpha1.AvailableModel{Name: name, SizeBytes: mib * 1024 * 1024})
	}
	return p
}

func TestCalibrateServer_choosesItsTargets(t *testing.T) {
	sizes := map[string]int64{model: 9_000, small: 800, middle: 2_500, large: 40_000, "tiny:0.5b": 400}
	bound := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: crewNS},
		Spec: aiv1alpha1.ModelSpec{ServerRef: gpu, Model: middle}}
	notDownloaded := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "n", Namespace: crewNS},
		Spec: aiv1alpha1.ModelSpec{ServerRef: gpu, Model: "pending:8b"}}

	cases := []struct {
		name  string
		f     fixture
		args  map[string]any
		want  []any
		error string
	}{
		{"the models asked for", newFixture(t, withAvailable(sizes)),
			map[string]any{argServer: gpu, argMods: []string{small, small}}, []any{small}, ""},
		{"an unknown model asked for", newFixture(t, withAvailable(sizes)),
			map[string]any{argServer: gpu, argMods: []string{"nope:1b"}}, nil, "downloaded"},
		{"the downloaded bound models", newFixture(t, withAvailable(sizes), bound, notDownloaded),
			map[string]any{argServer: gpu}, []any{middle}, ""},
		{"else the three smallest that fit", newFixture(t, withAvailable(sizes)),
			map[string]any{argServer: gpu}, []any{"tiny:0.5b", small, middle}, ""},
		{"nothing downloaded that fits", newFixture(t, withAvailable(map[string]int64{large: 40_000})),
			map[string]any{argServer: gpu}, nil, "no downloaded model"},
	}
	for _, c := range cases {
		out, err := calibrate(t, c.f, c.args)
		if c.error != "" {
			check(t, c.name, err != nil && strings.Contains(err.Error(), c.error), err)
			continue
		}
		check(t, c.name, err == nil && slices.Equal(out["models"].([]any), c.want), out)
	}
}

func TestCalibrateServer_refusesAServerItMayNotProbe(t *testing.T) {
	notReady := readyServer()
	notReady.Status.Ready = false
	off := false
	disabled := readyServer()
	disabled.Name = gpuB
	disabled.Spec.Calibration = &aiv1alpha1.ServerCalibrationSpec{Enabled: &off}
	f := newFixture(t, notReady, disabled)

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	check(t, "not ready", err != nil && strings.Contains(err.Error(), "not ready"), err)
	_, err = calibrate(t, f, map[string]any{argServer: gpuB})
	check(t, "calibration off", err != nil && strings.Contains(err.Error(), "enabled false"), err)
	_, err = calibrate(t, f, map[string]any{})
	check(t, "server required", err != nil && strings.Contains(err.Error(), "server"), err)
	_, err = calibrate(t, f, map[string]any{argServer: gpu, "probe": "load"})
	check(t, "unknown argument", err != nil, err)
}

// blockFirstGenerate holds the job inside its first engine call until release is
// closed; entered receives once the job is inside it.
func blockFirstGenerate(f fixture) (entered chan struct{}, release chan struct{}) {
	entered, release = make(chan struct{}, 1), make(chan struct{})
	f.eng.OnGenerate = func(n int) {
		if n == 1 {
			entered <- struct{}{}
			<-release
		}
	}
	return entered, release
}

// While a run is in progress, the tools that would disturb it refuse the server.
func TestCalibrateServer_otherToolsWaitForARun(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	entered, release := blockFirstGenerate(f)

	_, err := call(t, f.set, ToolCalibrateServer, map[string]any{argServer: gpu})
	check(t, "started", err == nil, err)
	<-entered
	_, err = call(t, f.set, ToolProbeModel, map[string]string{argServer: gpu, argMod: model, argPr: "load"})
	check(t, "probe refused", err != nil && strings.Contains(err.Error(), "in progress"), err)
	_, err = call(t, f.set, ToolRecordCalibration, map[string]any{argServer: gpu, argPh: PhaseFailed})
	check(t, "record refused", err != nil && strings.Contains(err.Error(), "in progress"), err)
	close(release)
	f.set.Jobs.Wait()
	check(t, "the run finished", getServer(t, f).Status.Calibration.Phase == PhaseCalibrated, nil)
}

// A run the server stops before it ends is recorded as Waiting, with the record
// written after the stop.
func TestCalibrateServer_aStoppedServerRecordsTheRunAsWaiting(t *testing.T) {
	f := newFixture(t, withAvailable(map[string]int64{model: 9_000, small: 800}))
	f.eng.Sizes[model] = 9_000
	f.eng.Sizes[small] = 800
	ctx, stop := context.WithCancel(context.Background())
	f.set.Jobs = &Jobs{Base: ctx}
	f.eng.OnGenerate = func(int) { stop() } // the server stops while the first model is measured

	_, err := call(t, f.set, ToolCalibrateServer, map[string]any{argServer: gpu, argMods: []string{model, small}})
	f.set.Jobs.Wait()
	cal := getServer(t, f).Status.Calibration
	check(t, "waiting after the stop, with the model measured before it", err == nil &&
		cal.Phase == PhaseWaiting && strings.Contains(cal.Message, "interrupted") && len(cal.Models) == 1, cal)
}

// The run records Calibrating before the tool returns; when that fails, the tool
// fails and no run is left behind.
func TestCalibrateServer_aStartThatCannotBeRecorded_fails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.set.Cluster = cluster.Cluster{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyServer()).
		WithStatusSubresource(&aiv1alpha1.ModelServer{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch,
				...client.SubResourcePatchOption) error {
				return errors.New("status write refused")
			},
		}).Build()}

	_, err := call(t, f.set, ToolCalibrateServer, map[string]any{argServer: gpu})
	f.set.Jobs.Wait()
	check(t, "the tool fails", err != nil && strings.Contains(err.Error(), "refused"), err)
	check(t, "no run left behind", !f.set.Jobs.Running(gpu), nil)
}

func TestCalibrateServer_withoutAJobsRunner(t *testing.T) {
	f := newFixture(t, readyServer())
	f.set.Jobs = nil
	_, err := call(t, f.set, ToolCalibrateServer, map[string]any{argServer: gpu})
	check(t, "refused", errors.Is(err, errNoJobs) || (err != nil && strings.Contains(err.Error(), "jobs runner")), err)
	check(t, "Running on a nil runner", !f.set.Jobs.Running(gpu), nil)
}

func TestRecoverInterrupted_marksOnlyCalibratingRunsAsWaiting(t *testing.T) {
	interrupted := readyServer()
	interrupted.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: PhaseCalibrating}
	done := readyServer()
	done.Name = gpuB
	done.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: PhaseCalibrated}
	f := newFixture(t, interrupted, done)

	check(t, "recovered", f.set.RecoverInterrupted(context.Background()) == nil, nil)
	got := getServer(t, f).Status.Calibration
	check(t, "interrupted run is Waiting", got.Phase == PhaseWaiting && strings.Contains(got.Message, "interrupted"), got)
	other := &aiv1alpha1.ModelServer{}
	_ = f.client.Get(context.Background(), client.ObjectKey{Namespace: opNS, Name: gpuB}, other)
	check(t, "a finished run is left alone", other.Status.Calibration.Phase == PhaseCalibrated, other.Status.Calibration)
}

// history writes one agent's performance entry for model on gpu.
func history(t *testing.T, f fixture, model string, samples ...bus.SchedulerPerformanceSamplesElem) {
	t.Helper()
	raw, _ := json.Marshal(bus.SchedulerPerformance{Server: gpu, Model: model, Agent: "crew/a", Samples: samples})
	key := bus.PerformanceKey(gpu, model, "crew/a")
	check(t, "history written", f.kv.PutKVValue(store.PerformanceBucket, key, raw) == nil, key)
}

func soloCall(at time.Time, tps int64) bus.SchedulerPerformanceSamplesElem {
	return bus.SchedulerPerformanceSamplesElem{At: at.UnixMilli(), Ok: true, ActiveCalls: 1,
		Kind: bus.SchedulerPerformanceSamplesElemKindToolTurn, OutputTokens: tps, EvalMs: 1000}
}

func TestObservedPerformance_comparesCrewCallsWithTheCalibration(t *testing.T) {
	p := readyServer()
	done := metav1.NewTime(t0.Add(-time.Hour))
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: PhaseCalibrated, CompletedAt: &done,
		Models: []aiv1alpha1.ModelCalibration{{Model: model, Fits: true, GenerateTokensPerSecond: 50}}}
	f := newFixture(t, p)
	calls := make([]bus.SchedulerPerformanceSamplesElem, 0, 11)
	for i := range 10 {
		calls = append(calls, soloCall(t0.Add(-time.Duration(30-i)*time.Minute), 20))
	}
	calls = append(calls, soloCall(t0.Add(-2*time.Hour), 999)) // before the calibration
	history(t, f, model, calls...)
	_ = f.kv.PutKVValue(store.TicketsBucket, gpu+"__crew", []byte(`{"holderId":"crew/b"}`))

	out, err := call(t, f.set, ToolObservedPerformance, map[string]string{argServer: gpu})
	check(t, "no error", err == nil, err)
	observed := out["observed"].([]any)[0].(map[string]any)
	check(t, "solo speed since the calibration", observed["soloTokensPerSecond"] == float64(20) &&
		observed["soloSamples"] == float64(10), observed)
	drifted := out["drifted"].([]any)
	check(t, "drifted", len(drifted) == 1 && drifted[0].(map[string]any)["calibratedTokensPerSecond"] == float64(50), drifted)
	check(t, "calls now and the rule", out["callsNow"] == float64(1) && strings.Contains(out["driftRule"].(string), "alone"), out)
}

func TestObservedPerformance_failures(t *testing.T) {
	f := newFixture(t, readyServer())
	_, err := call(t, f.set, ToolObservedPerformance, map[string]string{})
	check(t, "server required", err != nil, err)
	_, err = call(t, f.set, ToolObservedPerformance, map[string]string{argServer: "nope"})
	check(t, "unknown server", err != nil, err)
	f.kv.Set(&f.kv.FailList, true)
	_, err = call(t, f.set, ToolObservedPerformance, map[string]string{argServer: gpu})
	check(t, "unreadable history is an error", err != nil && strings.Contains(err.Error(), "history"), err)
}

func TestObservedPerformance_aServerNeverCalibrated(t *testing.T) {
	f := newFixture(t, readyServer())
	history(t, f, model, soloCall(t0, 20))
	out, err := call(t, f.set, ToolObservedPerformance, map[string]string{argServer: gpu})
	check(t, "observed, nothing to drift from", err == nil && len(out["observed"].([]any)) == 1 &&
		len(out["drifted"].([]any)) == 0, out)
}

// vllmServer is a ready vLLM server whose one vLLM server serves model, which the fake
// engine plays; the probe asks the engine factory for it by the request's engine type.
func vllmCalibrationFixture(t *testing.T) fixture {
	t.Helper()
	p := &aiv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm-rig1", Namespace: opNS},
		Spec: aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeVLLM,
			Servers: []aiv1alpha1.VLLMServer{{Model: model, Endpoint: "http://vllm-rig1:8000"}}},
	}
	p.Status.Ready = true
	p.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{
		AvailableModels: []aiv1alpha1.AvailableModel{{Name: model}}}
	f := newFixture(t, p)
	var asked []probe.Request
	f.set.Prober.(*probe.Prober).For = func(req probe.Request) engine.Engine {
		asked = append(asked, req)
		return f.eng
	}
	t.Cleanup(func() {
		for _, req := range asked {
			if req.EngineType != "vllm" || req.Endpoint != "http://vllm-rig1:8000" {
				t.Errorf("a vLLM probe must name its engine and the model's own server: %+v", req)
			}
		}
	})
	return f
}

func TestCalibrateServer_aVLLMServerIsCalibratedLikeAnOllamaOne(t *testing.T) {
	f := vllmCalibrationFixture(t)

	out, err := calibrate(t, f, map[string]any{argServer: "vllm-rig1"})
	check(t, "started", err == nil && out["started"] == true, out)

	cal := getNamedServer(t, f, "vllm-rig1").Status.Calibration
	check(t, "calibrated", cal != nil && cal.Phase == PhaseCalibrated && len(cal.Models) == 1, cal)
	m := cal.Models[0]
	check(t, "the speeds modelSpeeds publishes", m.Model == model && m.Fits && m.GenerateTokensPerSecond == 50 &&
		m.PromptTokensPerSecond > 0 && m.ContextLength == 8192, m)
	check(t, "the parallel probe uses the vLLM probe width, not a slot count",
		m.ParallelRequests == cluster.VLLMProbeWidth, m)
	check(t, "the server is put back as it was found", slices.Contains(f.eng.Unloads, model), f.eng.Unloads)
}

func getNamedServer(t *testing.T, f fixture, name string) *aiv1alpha1.ModelServer {
	t.Helper()
	p := &aiv1alpha1.ModelServer{}
	if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: opNS, Name: name}, p); err != nil {
		t.Fatal(err)
	}
	return p
}
