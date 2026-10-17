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
	"errors"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/cluster"
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

// calibrate calls calibrate_provider and waits for the job it started to end.
func calibrate(t *testing.T, f fixture, args map[string]any) (map[string]any, error) {
	t.Helper()
	out, err := call(t, f.set, ToolCalibrateProvider, args)
	f.set.Jobs.Wait()
	return out, err
}

func TestCalibrateProvider_measuresEachModel_andRecordsTheResults(t *testing.T) {
	f := newFixture(t, readyProvider())
	f.eng.Sizes[model] = 9_000

	out, err := calibrate(t, f, map[string]any{argProv: gpu})
	check(t, "started", err == nil && out["started"] == true && len(out["models"].([]any)) == 1, out)

	cal := getProvider(t, f).Status.Calibration
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

func TestCalibrateProvider_aModelThatSpills_isRecordedAsNotFitting(t *testing.T) {
	f := newFixture(t, readyProvider())
	f.eng.Sizes[model] = 9_000
	f.eng.VRAMFraction = 0.5

	_, err := calibrate(t, f, map[string]any{argProv: gpu})
	cal := getProvider(t, f).Status.Calibration
	check(t, "recorded as not fitting", err == nil && cal.Phase == PhaseCalibrated && len(cal.Models) == 1 &&
		!cal.Models[0].Fits && cal.Models[0].Reason != "", cal)
}

func TestCalibrateProvider_pausesAsWaiting_whenCrewCallsTakeTheProvider(t *testing.T) {
	f := newFixture(t, readyProvider())
	f.eng.Sizes[model] = 9_000
	f.eng.OnGenerate = func(n int) {
		if n == 1 { // a crew call arrives after the load
			_ = f.kv.PutKVValue(store.TicketsBucket, gpu+"__crew", []byte(`{"holderId":"crew/a"}`))
		}
	}

	_, err := calibrate(t, f, map[string]any{argProv: gpu})
	cal := getProvider(t, f).Status.Calibration
	check(t, "waiting", err == nil && cal.Phase == PhaseWaiting && strings.Contains(cal.Message, "crew"), cal)
	check(t, "the model the recipe loaded is unloaded", slices.Contains(f.eng.Unloads, model), f.eng.Unloads)
}

func TestCalibrateProvider_failsWhenNothingCanBeMeasured(t *testing.T) {
	f := newFixture(t, readyProvider())
	f.eng.Sizes[model] = 9_000
	f.eng.Fail = true

	_, err := calibrate(t, f, map[string]any{argProv: gpu})
	cal := getProvider(t, f).Status.Calibration
	check(t, "failed", err == nil && cal.Phase == PhaseFailed && strings.Contains(cal.Message, model), cal)
}

func TestCalibrateProvider_reportsAJobAlreadyRunning(t *testing.T) {
	f := newFixture(t, readyProvider())
	f.eng.Sizes[model] = 9_000
	entered, release := blockFirstGenerate(f)

	first, err := call(t, f.set, ToolCalibrateProvider, map[string]any{argProv: gpu})
	check(t, "first starts", err == nil && first["started"] == true, first)
	<-entered
	second, err := call(t, f.set, ToolCalibrateProvider, map[string]any{argProv: gpu})
	check(t, "second reports running", err == nil && second["running"] == true && second["started"] == nil, second)
	close(release)
	f.set.Jobs.Wait()
}

func withAvailable(sizes map[string]int64) *aiv1alpha1.ModelProvider {
	p := readyProvider()
	p.Status.Capacity.AvailableModels = nil
	for name, mib := range sizes {
		p.Status.Capacity.AvailableModels = append(p.Status.Capacity.AvailableModels,
			aiv1alpha1.AvailableModel{Name: name, SizeBytes: mib * 1024 * 1024})
	}
	return p
}

func TestCalibrateProvider_choosesItsTargets(t *testing.T) {
	sizes := map[string]int64{model: 9_000, small: 800, middle: 2_500, large: 40_000, "tiny:0.5b": 400}
	bound := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: crewNS},
		Spec: aiv1alpha1.ModelSpec{ProviderRef: gpu, Model: middle}}
	notDownloaded := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "n", Namespace: crewNS},
		Spec: aiv1alpha1.ModelSpec{ProviderRef: gpu, Model: "pending:8b"}}

	cases := []struct {
		name  string
		f     fixture
		args  map[string]any
		want  []any
		error string
	}{
		{"the models asked for", newFixture(t, withAvailable(sizes)),
			map[string]any{argProv: gpu, argMods: []string{small, small}}, []any{small}, ""},
		{"an unknown model asked for", newFixture(t, withAvailable(sizes)),
			map[string]any{argProv: gpu, argMods: []string{"nope:1b"}}, nil, "downloaded"},
		{"the downloaded bound models", newFixture(t, withAvailable(sizes), bound, notDownloaded),
			map[string]any{argProv: gpu}, []any{middle}, ""},
		{"else the three smallest that fit", newFixture(t, withAvailable(sizes)),
			map[string]any{argProv: gpu}, []any{"tiny:0.5b", small, middle}, ""},
		{"nothing downloaded that fits", newFixture(t, withAvailable(map[string]int64{large: 40_000})),
			map[string]any{argProv: gpu}, nil, "no downloaded model"},
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

func TestCalibrateProvider_refusesAProviderItMayNotProbe(t *testing.T) {
	notReady := readyProvider()
	notReady.Status.Ready = false
	off := false
	disabled := readyProvider()
	disabled.Name = gpuB
	disabled.Spec.Calibration = &aiv1alpha1.ProviderCalibrationSpec{Enabled: &off}
	f := newFixture(t, notReady, disabled)

	_, err := calibrate(t, f, map[string]any{argProv: gpu})
	check(t, "not ready", err != nil && strings.Contains(err.Error(), "not ready"), err)
	_, err = calibrate(t, f, map[string]any{argProv: gpuB})
	check(t, "calibration off", err != nil && strings.Contains(err.Error(), "enabled false"), err)
	_, err = calibrate(t, f, map[string]any{})
	check(t, "provider required", err != nil && strings.Contains(err.Error(), "provider"), err)
	_, err = calibrate(t, f, map[string]any{argProv: gpu, "probe": "load"})
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

// While a run is in progress, the tools that would disturb it refuse the provider.
func TestCalibrateProvider_otherToolsWaitForARun(t *testing.T) {
	f := newFixture(t, readyProvider())
	f.eng.Sizes[model] = 9_000
	entered, release := blockFirstGenerate(f)

	_, err := call(t, f.set, ToolCalibrateProvider, map[string]any{argProv: gpu})
	check(t, "started", err == nil, err)
	<-entered
	_, err = call(t, f.set, ToolProbeModel, map[string]string{argProv: gpu, argMod: model, argPr: "load"})
	check(t, "probe refused", err != nil && strings.Contains(err.Error(), "in progress"), err)
	_, err = call(t, f.set, ToolRecordCalibration, map[string]any{argProv: gpu, argPh: PhaseFailed})
	check(t, "record refused", err != nil && strings.Contains(err.Error(), "in progress"), err)
	close(release)
	f.set.Jobs.Wait()
	check(t, "the run finished", getProvider(t, f).Status.Calibration.Phase == PhaseCalibrated, nil)
}

// A run the server stops before it ends is recorded as Waiting, with the record
// written after the stop.
func TestCalibrateProvider_aStoppedServerRecordsTheRunAsWaiting(t *testing.T) {
	f := newFixture(t, withAvailable(map[string]int64{model: 9_000, small: 800}))
	f.eng.Sizes[model] = 9_000
	f.eng.Sizes[small] = 800
	ctx, stop := context.WithCancel(context.Background())
	f.set.Jobs = &Jobs{Base: ctx}
	f.eng.OnGenerate = func(int) { stop() } // the server stops while the first model is measured

	_, err := call(t, f.set, ToolCalibrateProvider, map[string]any{argProv: gpu, argMods: []string{model, small}})
	f.set.Jobs.Wait()
	cal := getProvider(t, f).Status.Calibration
	check(t, "waiting after the stop, with the model measured before it", err == nil &&
		cal.Phase == PhaseWaiting && strings.Contains(cal.Message, "interrupted") && len(cal.Models) == 1, cal)
}

// The run records Calibrating before the tool returns; when that fails, the tool
// fails and no run is left behind.
func TestCalibrateProvider_aStartThatCannotBeRecorded_fails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.set.Cluster = cluster.Cluster{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(readyProvider()).
		WithStatusSubresource(&aiv1alpha1.ModelProvider{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch,
				...client.SubResourcePatchOption) error {
				return errors.New("status write refused")
			},
		}).Build()}

	_, err := call(t, f.set, ToolCalibrateProvider, map[string]any{argProv: gpu})
	f.set.Jobs.Wait()
	check(t, "the tool fails", err != nil && strings.Contains(err.Error(), "refused"), err)
	check(t, "no run left behind", !f.set.Jobs.Running(gpu), nil)
}

func TestCalibrateProvider_withoutAJobsRunner(t *testing.T) {
	f := newFixture(t, readyProvider())
	f.set.Jobs = nil
	_, err := call(t, f.set, ToolCalibrateProvider, map[string]any{argProv: gpu})
	check(t, "refused", errors.Is(err, errNoJobs) || (err != nil && strings.Contains(err.Error(), "jobs runner")), err)
	check(t, "Running on a nil runner", !f.set.Jobs.Running(gpu), nil)
}

func TestRecoverInterrupted_marksOnlyCalibratingRunsAsWaiting(t *testing.T) {
	interrupted := readyProvider()
	interrupted.Status.Calibration = &aiv1alpha1.ProviderCalibration{Phase: PhaseCalibrating}
	done := readyProvider()
	done.Name = gpuB
	done.Status.Calibration = &aiv1alpha1.ProviderCalibration{Phase: PhaseCalibrated}
	f := newFixture(t, interrupted, done)

	check(t, "recovered", f.set.RecoverInterrupted(context.Background()) == nil, nil)
	got := getProvider(t, f).Status.Calibration
	check(t, "interrupted run is Waiting", got.Phase == PhaseWaiting && strings.Contains(got.Message, "interrupted"), got)
	other := &aiv1alpha1.ModelProvider{}
	_ = f.client.Get(context.Background(), client.ObjectKey{Namespace: opNS, Name: gpuB}, other)
	check(t, "a finished run is left alone", other.Status.Calibration.Phase == PhaseCalibrated, other.Status.Calibration)
}
