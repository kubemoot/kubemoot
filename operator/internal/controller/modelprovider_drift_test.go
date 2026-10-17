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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/internal/scheduler/record"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/perf"
)

const (
	driftCrewNS = "kubemoot-scheduling"
	driftCrew   = "kubemoot-scheduling"
)

var driftCrewRef = CrewRef{Namespace: driftCrewNS, Name: driftCrew}

// fakeDriftWatch serves a fixed history and records what it was asked.
type fakeDriftWatch struct {
	history  []bus.SchedulerPerformance
	notReady bool
	failAsk  bool
	failRead bool
	asked    []string
}

func (f *fakeDriftWatch) History(string) ([]bus.SchedulerPerformance, error) {
	if f.failRead {
		return nil, errors.New("nats down")
	}
	return f.history, nil
}

func (f *fakeDriftWatch) Ready(context.Context) (bool, string) { return !f.notReady, "not ready" }

func (f *fakeDriftWatch) Ask(_ context.Context, provider string, d *aiv1alpha1.CalibrationDrift) error {
	if f.failAsk {
		return errors.New("write failed")
	}
	f.asked = append(f.asked, provider+" "+d.Key)
	return nil
}

var calibratedAt = metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))

// calibratedGPU is a provider calibrated an hour ago at 150 tokens per second for calTestModelA.
func calibratedGPU() *aiv1alpha1.ModelProvider {
	p := calProvider(&aiv1alpha1.ProviderCalibration{Phase: calibrationPhaseCalibrated, CompletedAt: &calibratedAt,
		Models: []aiv1alpha1.ModelCalibration{{Model: calTestModelA, Fits: true, GenerateTokensPerSecond: 150}}}, "")
	return p
}

// soloCalls is n warm crew calls since the calibration, alone on the provider, at tps.
func soloCalls(n int, tps int64) []bus.SchedulerPerformance {
	samples := make([]bus.SchedulerPerformanceSamplesElem, 0, n)
	for i := range n {
		samples = append(samples, bus.SchedulerPerformanceSamplesElem{
			At: calibratedAt.Add(time.Duration(i+1) * time.Minute).UnixMilli(), Ok: true, ActiveCalls: 1,
			Kind: bus.SchedulerPerformanceSamplesElemKindToolTurn, OutputTokens: tps, EvalMs: 1000})
	}
	return []bus.SchedulerPerformance{{Provider: calTestProvider, Model: calTestModelA, Agent: "crew/a", Samples: samples}}
}

func driftReconciler(t *testing.T, watch DriftWatch, p *aiv1alpha1.ModelProvider) (*ModelProviderReconciler, client.Client) {
	t.Helper()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).Build()
	return &ModelProviderReconciler{Client: cli, Scheme: scheme, Drift: watch}, cli
}

func storedDrift(t *testing.T, cli client.Client) *aiv1alpha1.CalibrationDrift {
	t.Helper()
	got := &aiv1alpha1.ModelProvider{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: "kubemoot", Name: calTestProvider}, got); err != nil {
		t.Fatal(err)
	}
	return got.Status.CalibrationDrift
}

func TestReviewDrift_recordsADriftAndAsksTheCrewOnce(t *testing.T) {
	watch := &fakeDriftWatch{history: soloCalls(perf.MinSamples, 60)}
	p := calibratedGPU()
	r, cli := driftReconciler(t, watch, p)

	r.reviewDrift(context.Background(), p)
	d := storedDrift(t, cli)
	if d == nil || len(d.Models) != 1 || d.Models[0].ObservedTokensPerSecond != 60 ||
		d.Models[0].CalibratedTokensPerSecond != 150 || d.AskedAt == nil {
		t.Fatalf("drift not recorded and asked: %+v", d)
	}
	r.reviewDrift(context.Background(), p)
	if len(watch.asked) != 1 {
		t.Errorf("asked about the same drift again: %v", watch.asked)
	}
}

func TestReviewDrift_waitsForTheCrew_thenAsks(t *testing.T) {
	watch := &fakeDriftWatch{history: soloCalls(perf.MinSamples, 60), notReady: true}
	p := calibratedGPU()
	r, cli := driftReconciler(t, watch, p)

	r.reviewDrift(context.Background(), p)
	if d := storedDrift(t, cli); d == nil || d.AskedAt != nil || len(watch.asked) != 0 {
		t.Fatalf("the drift should be recorded, not asked: %+v %v", d, watch.asked)
	}
	watch.notReady = false
	r.reviewDrift(context.Background(), p)
	if d := storedDrift(t, cli); d == nil || d.AskedAt == nil || len(watch.asked) != 1 {
		t.Errorf("asked once the crew was ready: %+v %v", d, watch.asked)
	}
}

func TestReviewDrift_clearsADriftOnceTheSpeedsAgree(t *testing.T) {
	watch := &fakeDriftWatch{history: soloCalls(perf.MinSamples, 60)}
	p := calibratedGPU()
	r, cli := driftReconciler(t, watch, p)
	r.reviewDrift(context.Background(), p)

	watch.history = soloCalls(perf.MinSamples, 145)
	r.reviewDrift(context.Background(), p)
	if d := storedDrift(t, cli); d != nil {
		t.Errorf("drift kept after the speeds agreed: %+v", d)
	}
}

func TestReviewDrift_leavesAloneWhatItCannotJudge(t *testing.T) {
	waiting := calibratedGPU()
	waiting.Status.Calibration.Phase = calibrationPhaseWaiting
	cases := []struct {
		name  string
		watch *fakeDriftWatch
		p     *aiv1alpha1.ModelProvider
	}{
		{"never calibrated", &fakeDriftWatch{history: soloCalls(perf.MinSamples, 60)}, calProvider(nil, "")},
		{"calibration not settled", &fakeDriftWatch{history: soloCalls(perf.MinSamples, 60)}, waiting},
		{"too few calls", &fakeDriftWatch{history: soloCalls(perf.MinSamples-1, 60)}, calibratedGPU()},
		{"history unreadable", &fakeDriftWatch{failRead: true}, calibratedGPU()},
		{"the ask fails", &fakeDriftWatch{history: soloCalls(perf.MinSamples, 60), failAsk: true}, calibratedGPU()},
	}
	for _, c := range cases {
		r, cli := driftReconciler(t, c.watch, c.p)
		r.reviewDrift(context.Background(), c.p)
		d := storedDrift(t, cli)
		if c.name == "the ask fails" {
			if d == nil || d.AskedAt != nil {
				t.Errorf("%s: the drift should be recorded as not asked: %+v", c.name, d)
			}
			continue
		}
		if d != nil || len(c.watch.asked) != 0 {
			t.Errorf("%s: drift %+v, asked %v", c.name, d, c.watch.asked)
		}
	}
	none := &ModelProviderReconciler{}
	none.reviewDrift(context.Background(), calibratedGPU()) // no watch: nothing to do, no panic
}

func TestNextDrift(t *testing.T) {
	now := metav1.Now()
	slow := []perf.ModelDrift{{Model: calTestModelA, CalibratedTokensPerSecond: 150, ObservedTokensPerSecond: 60, Samples: 8}}
	if nextDrift(nil, nil, calibratedAt, now) != nil {
		t.Error("no drifted model is no drift")
	}
	first := nextDrift(nil, slow, calibratedAt, now)
	asked := metav1.Now()
	first.AskedAt = &asked
	same := nextDrift(first, slow, calibratedAt, metav1.NewTime(now.Add(time.Hour)))
	if same.Key != first.Key || same.AskedAt == nil || !same.DetectedAt.Equal(&first.DetectedAt) {
		t.Errorf("the same drift should keep its key, detection, and ask: %+v", same)
	}
	recalibrated := nextDrift(first, slow, metav1.NewTime(calibratedAt.Add(time.Hour)), now)
	if recalibrated.Key == first.Key || recalibrated.AskedAt != nil {
		t.Errorf("a drift from a newer calibration is a new drift: %+v", recalibrated)
	}
}

func TestDriftQueryAndRecord(t *testing.T) {
	d := &aiv1alpha1.CalibrationDrift{Key: "abc", Models: []aiv1alpha1.ModelDrift{
		{Model: calTestModelA, CalibratedTokensPerSecond: 150, ObservedTokensPerSecond: 60, Samples: 9}}}
	q := driftQuery(calTestProvider, d)
	for _, want := range []string{calTestProvider, calTestModelA, "60", "150", "9 calls", "recalibrate"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %q", q, want)
		}
	}
	rec := driftRecord(driftCrewRef, calTestProvider, d, time.Now())
	if rec.Kind != record.KindFollowup || rec.Namespace != driftCrewNS || rec.Crew != driftCrew ||
		rec.Query != q || !strings.HasSuffix(rec.ScheduleID, "abc") {
		t.Errorf("record: %+v", rec)
	}
}

func TestParseCrewRef(t *testing.T) {
	if got, ok := ParseCrewRef(" kubemoot-scheduling/kubemoot-scheduling "); !ok || got != driftCrewRef {
		t.Errorf("namespace/name: %+v %v", got, ok)
	}
	for _, bad := range []string{"", "crew", "/crew", "ns/", "a/b/c"} {
		if _, ok := ParseCrewRef(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestNATSDriftWatch_Ready(t *testing.T) {
	scheme := agentReconcileScheme(t)
	agent := func(name string, ready bool) *aiv1alpha1.Agent {
		a := &aiv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: driftCrewNS,
			Labels: map[string]string{labelCrew: driftCrew}}}
		a.Status.Ready = ready
		return a
	}
	crew := &aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: driftCrew, Namespace: driftCrewNS}}
	cases := []struct {
		name string
		objs []client.Object
		want bool
	}{
		{"no crew", nil, false},
		{"no agents", []client.Object{crew}, false},
		{"an agent not ready", []client.Object{crew, agent("a", true), agent("b", false)}, false},
		{"every agent ready", []client.Object{crew, agent("a", true), agent("b", true)}, true},
	}
	for _, c := range cases {
		w := NATSDriftWatch{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(c.objs...).Build(), Crew: driftCrewRef}
		if ready, why := w.Ready(context.Background()); ready != c.want || (!ready && why == "") {
			t.Errorf("%s: ready %v, why %q", c.name, ready, why)
		}
	}
}

func TestNATSDriftWatch_withoutNATS(t *testing.T) {
	for _, w := range []NATSDriftWatch{{}, {KV: kubemootnats.NewPublisher(""), Now: time.Now}} {
		if _, err := w.History(calTestProvider); !errors.Is(err, errNATSUnavailable) {
			t.Errorf("History without NATS: %v", err)
		}
		if err := w.Ask(context.Background(), calTestProvider, &aiv1alpha1.CalibrationDrift{Key: "k"}); !errors.Is(err, errNATSUnavailable) {
			t.Errorf("Ask without NATS: %v", err)
		}
	}
}

// memKV is an in-memory KV with switchable failures.
type memKV struct {
	data                    map[string][]byte
	failGet, failPut, lossy bool
}

func newMemKV() *memKV { return &memKV{data: map[string][]byte{}} }

func (m *memKV) ListKVKeys(bucket string) ([]string, error) {
	var keys []string
	for k := range m.data {
		if b, key, ok := strings.Cut(k, "/"); ok && b == bucket {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (m *memKV) GetKVValue(bucket, key string) ([]byte, error) {
	if m.failGet {
		return nil, errors.New("get failed")
	}
	return m.data[bucket+"/"+key], nil
}

func (m *memKV) PutKVValue(bucket, key string, value []byte) error {
	if m.failPut {
		return errors.New("put failed")
	}
	if !m.lossy {
		m.data[bucket+"/"+key] = value
	}
	return nil
}

func (m *memKV) Configured() bool { return true }

func TestNATSDriftWatch_History(t *testing.T) {
	kv := newMemKV()
	raw, _ := json.Marshal(soloCalls(2, 60)[0])
	kv.data[bus.PerformanceBucket+"/"+bus.PerformanceKey(calTestProvider, calTestModelA, "crew/a")] = raw
	kv.data[bus.PerformanceBucket+"/"+bus.PerformanceKey(calTestProvider, "broken:1b", "crew/a")] = []byte(`{"samples":`)
	other, _ := json.Marshal(bus.SchedulerPerformance{Provider: "elsewhere", Model: calTestModelA})
	kv.data[bus.PerformanceBucket+"/"+bus.PerformanceKey("elsewhere", calTestModelA, "crew/a")] = other
	w := NATSDriftWatch{KV: kv}

	got, err := w.History(calTestProvider)
	if err != nil || len(got) != 1 || got[0].Provider != calTestProvider || len(got[0].Samples) != 2 {
		t.Fatalf("history: %+v %v", got, err)
	}
	kv.failGet = true
	if _, err := w.History(calTestProvider); err == nil {
		t.Error("an unreadable entry should be an error")
	}
}

func TestNATSDriftWatch_Ask(t *testing.T) {
	drift := &aiv1alpha1.CalibrationDrift{Key: "k1", Models: []aiv1alpha1.ModelDrift{{Model: calTestModelA}}}
	kv := newMemKV()
	w := NATSDriftWatch{KV: kv, Crew: driftCrewRef, Now: time.Now}
	if err := w.Ask(context.Background(), calTestProvider, drift); err != nil {
		t.Fatalf("ask: %v", err)
	}
	var rec record.Record
	if err := json.Unmarshal(kv.data[record.Bucket+"/calibration-drift-"+calTestProvider+"-k1"], &rec); err != nil ||
		rec.Crew != driftCrew || !strings.Contains(rec.Query, calTestModelA) {
		t.Errorf("stored record: %+v %v", rec, err)
	}
	kv.lossy = true
	if err := w.Ask(context.Background(), calTestProvider, &aiv1alpha1.CalibrationDrift{Key: "k2"}); !errors.Is(err, errNotStored) {
		t.Errorf("a write that did not land: %v", err)
	}
	kv.failPut = true
	if err := w.Ask(context.Background(), calTestProvider, drift); err == nil {
		t.Error("a failed put should be an error")
	}
}

// A failed ask takes back the recorded ask, so the drift is asked again later.
func TestReviewDrift_aFailedAskIsTakenBack(t *testing.T) {
	watch := &fakeDriftWatch{history: soloCalls(perf.MinSamples, 60), failAsk: true}
	p := calibratedGPU()
	r, cli := driftReconciler(t, watch, p)
	r.reviewDrift(context.Background(), p)
	if d := storedDrift(t, cli); d == nil || d.AskedAt != nil {
		t.Fatalf("the drift should be recorded as not asked: %+v", d)
	}
	watch.failAsk = false
	r.reviewDrift(context.Background(), p)
	if d := storedDrift(t, cli); d == nil || d.AskedAt == nil || len(watch.asked) != 1 {
		t.Errorf("asked once the ask could succeed: %+v %v", d, watch.asked)
	}
}

// When the status cannot be written, the crew is not asked: an ask is recorded first.
func TestReviewDrift_noAskWithoutAStatusWrite(t *testing.T) {
	watch := &fakeDriftWatch{history: soloCalls(perf.MinSamples, 60)}
	p := calibratedGPU()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string,
			client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			return errors.New("status write refused")
		}}).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, Drift: watch}
	r.reviewDrift(context.Background(), p)
	if len(watch.asked) != 0 {
		t.Errorf("asked without recording it: %v", watch.asked)
	}
}

// After a status patch, the reconcile's full status update does not conflict and
// keeps what the patch stored.
func TestPatchStatus_thenUpdateStatus(t *testing.T) {
	p := calibratedGPU()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme}
	current := &aiv1alpha1.ModelProvider{}
	if err := cli.Get(context.Background(), client.ObjectKeyFromObject(p), current); err != nil {
		t.Fatal(err)
	}
	drift := &aiv1alpha1.CalibrationDrift{Key: "k", DetectedAt: metav1.Now()}
	if !r.patchStatus(context.Background(), current, func(s *aiv1alpha1.ModelProviderStatus) { s.CalibrationDrift = drift }) {
		t.Fatal("patch failed")
	}
	current.Status.Message = "reconciled"
	if err := cli.Status().Update(context.Background(), current); err != nil {
		t.Fatalf("the status update after a patch conflicted: %v", err)
	}
	if d := storedDrift(t, cli); d == nil || d.Key != "k" {
		t.Errorf("the update lost what the patch stored: %+v", d)
	}
}
