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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// fakeAdapter is an engine adapter whose models have a state the test controls.
type fakeAdapter struct {
	mu        sync.Mutex
	states    map[string]engine.ModelState
	readied   []string
	released  []string
	readyErr  error
	probeErr  error
	lagProbes int // probes after an action that still report the old state
	pending   map[string]engine.ModelState
	inAction  atomic.Int32
	overlap   atomic.Bool
	hold      time.Duration
}

func (f *fakeAdapter) Probe(_ context.Context, _ aiv1alpha1.ModelProviderSpec) (engine.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.probeErr != nil {
		return engine.Snapshot{}, f.probeErr
	}
	if len(f.pending) > 0 {
		if f.lagProbes > 0 {
			f.lagProbes--
		} else {
			for m, s := range f.pending {
				f.states[m] = s
			}
			f.pending = nil
		}
	}
	out := map[string]engine.ModelState{}
	for m, s := range f.states {
		out[m] = s
	}
	return engine.Snapshot{ModelStates: out}, nil
}

func (f *fakeAdapter) change(model string, to engine.ModelState) error {
	if f.inAction.Add(1) > 1 {
		f.overlap.Store(true)
	}
	defer f.inAction.Add(-1)
	time.Sleep(f.hold)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lagProbes > 0 {
		f.pending = map[string]engine.ModelState{model: to}
		return nil
	}
	f.states[model] = to
	return nil
}

func (f *fakeAdapter) Ready(_ context.Context, _ aiv1alpha1.ModelProviderSpec, model string) error {
	f.mu.Lock()
	f.readied = append(f.readied, model)
	err := f.readyErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.change(model, engine.StateAwake)
}

func (f *fakeAdapter) Release(_ context.Context, _ aiv1alpha1.ModelProviderSpec, model string) error {
	f.mu.Lock()
	f.released = append(f.released, model)
	f.mu.Unlock()
	return f.change(model, engine.StateAsleep)
}

type recordingRefresher struct {
	mu    sync.Mutex
	calls []int64
}

func (r *recordingRefresher) RefreshAfterSwitch(_ context.Context, _ *aiv1alpha1.ModelProvider, _ string, ms int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ms)
}

const lifecycleProvider = "vllm-rig0"

func lifecycleServer(t *testing.T, adapter *fakeAdapter, providers ...*aiv1alpha1.ModelProvider) (*ModelLifecycleServer, *recordingRefresher) {
	t.Helper()
	scheme := agentReconcileScheme(t)
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, p := range providers {
		builder = builder.WithObjects(p)
	}
	registry := engine.NewRegistry()
	factory := func(*http.Client) engine.Adapter { return adapter }
	registry.Register(aiv1alpha1.ProviderTypeVLLM, factory)
	registry.Register(aiv1alpha1.ProviderTypeOllama, factory)
	ref := &recordingRefresher{}
	return &ModelLifecycleServer{
		Reader: builder.Build(), Refresher: ref, Engines: registry,
		PollInterval: time.Millisecond, SafetyLimit: 300 * time.Millisecond,
	}, ref
}

func namedProvider(name string, typ aiv1alpha1.ProviderType) *aiv1alpha1.ModelProvider {
	return &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS1},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: typ},
	}
}

func request(t *testing.T, action bus.ModelLifecycleRequestAction, model string) []byte {
	t.Helper()
	raw, err := json.Marshal(bus.ModelLifecycleRequest{Action: action, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var lifecycleSubject = bus.ModelLifecycleSubject(lifecycleProvider)

func asleepAwake() *fakeAdapter {
	return &fakeAdapter{states: map[string]engine.ModelState{vllmModel14: engine.StateAsleep, "qwen3:32b": engine.StateAwake}}
}

func TestReadyWakesAnAsleepModelAndRepliesOnceItIsAwake(t *testing.T) {
	adapter := asleepAwake()
	adapter.lagProbes = 3 // the server reports asleep for a few probes after the wake was answered
	s, ref := lifecycleServer(t, adapter, namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM))
	reply := s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, vllmModel14))
	if !reply.Ok || reply.State != bus.ModelLifecycleReplyStateAwake {
		t.Fatalf("reply = %+v", reply)
	}
	if len(adapter.readied) != 1 || adapter.states[vllmModel14] != engine.StateAwake {
		t.Errorf("readied %v, state %v", adapter.readied, adapter.states)
	}
	if len(ref.calls) != 1 {
		t.Errorf("the state must be published once: %v", ref.calls)
	}
}

func TestReadyOnAnAwakeModelDoesNothing(t *testing.T) {
	adapter := asleepAwake()
	s, ref := lifecycleServer(t, adapter, namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM))
	reply := s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, "qwen3:32b"))
	if !reply.Ok || reply.SwitchMs != 0 || len(adapter.readied) != 0 || len(ref.calls) != 0 {
		t.Errorf("reply %+v readied %v published %v", reply, adapter.readied, ref.calls)
	}
}

func TestReleasePutsAnAwakeModelToSleep(t *testing.T) {
	adapter := asleepAwake()
	s, _ := lifecycleServer(t, adapter, namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM))
	reply := s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionRelease, "qwen3:32b"))
	if !reply.Ok || reply.State != bus.ModelLifecycleReplyStateAsleep || reply.SwitchMs != 0 {
		t.Fatalf("reply = %+v", reply)
	}
	if len(adapter.released) != 1 {
		t.Errorf("released = %v", adapter.released)
	}
	again := s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionRelease, "qwen3:32b"))
	if !again.Ok || len(adapter.released) != 1 {
		t.Errorf("an asleep model needs nothing: %+v released %v", again, adapter.released)
	}
}

func TestRequestsThatCannotBeServedAreRefusedWithAReason(t *testing.T) {
	adapter := asleepAwake()
	s, _ := lifecycleServer(t, adapter,
		namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM),
		namedProvider("ollama-cpu", aiv1alpha1.ProviderTypeOllama))
	good := request(t, bus.ModelLifecycleRequestActionReady, vllmModel14)
	cases := map[string]struct {
		subject string
		body    []byte
	}{
		"malformed body":   {lifecycleSubject, []byte("{")},
		"no model":         {lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, "")},
		"unknown action":   {lifecycleSubject, []byte(`{"action":"reboot","model":"m"}`)},
		"unknown provider": {bus.ModelLifecycleSubject("nope"), good},
		"ollama provider":  {bus.ModelLifecycleSubject("ollama-cpu"), good},
		"unknown model":    {lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, "other")},
	}
	for name, c := range cases {
		reply := s.Handle(context.Background(), c.subject, c.body)
		if reply.Ok || reply.Error == "" {
			t.Errorf("%s: reply = %+v, want a refusal with a reason", name, reply)
		}
	}
	if len(adapter.readied) != 0 {
		t.Errorf("nothing may be woken for a refused request: %v", adapter.readied)
	}
}

func TestAnAdapterFailureIsTheReply(t *testing.T) {
	adapter := asleepAwake()
	adapter.readyErr = errors.New("putting \"qwen3:32b\" to sleep: POST /sleep returned 500")
	s, ref := lifecycleServer(t, adapter, namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM))
	reply := s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, vllmModel14))
	if reply.Ok || reply.Error != adapter.readyErr.Error() || len(ref.calls) != 0 {
		t.Errorf("reply %+v published %v", reply, ref.calls)
	}
	adapter.probeErr = errors.New("no server answered")
	if r := s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, vllmModel14)); r.Ok {
		t.Errorf("a provider that cannot be probed cannot be asked: %+v", r)
	}
}

func TestAProviderThatNeverReportsTheStateEndsAtTheSafetyLimit(t *testing.T) {
	adapter := asleepAwake()
	adapter.lagProbes = 1 << 30 // the state never follows the action
	s, ref := lifecycleServer(t, adapter, namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM))
	start := time.Now()
	reply := s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, vllmModel14))
	if reply.Ok || len(ref.calls) != 0 {
		t.Fatalf("reply %+v published %v", reply, ref.calls)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Errorf("gave up after %v, before the safety limit", time.Since(start))
	}
}

func TestRequestsOnOneProviderNeverInterleave(t *testing.T) {
	adapter := asleepAwake()
	adapter.hold = 30 * time.Millisecond
	s, _ := lifecycleServer(t, adapter, namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM))
	var wg sync.WaitGroup
	for _, m := range []string{vllmModel14, "qwen3:32b", vllmModel14} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, m))
		}()
	}
	wg.Wait()
	if adapter.overlap.Load() {
		t.Error("two wakes ran on one accelerator at once")
	}
}

func TestTheProviderIsFoundByItsEscapedNameNotItsNamespace(t *testing.T) {
	adapter := asleepAwake()
	s, _ := lifecycleServer(t, adapter, namedProvider("vllm.rig0", aiv1alpha1.ProviderTypeVLLM))
	reply := s.Handle(context.Background(), bus.ModelLifecycleSubject("vllm.rig0"), request(t, bus.ModelLifecycleRequestActionReady, vllmModel14))
	if !reply.Ok {
		t.Errorf("a name with a dot is one subject token: %+v", reply)
	}
}

func TestSwitchTimesAreKeptPerProviderAndMergedIntoThePublishedState(t *testing.T) {
	var l switchLog
	l.Record("a", "m1", 900)
	l.Record("a", "m2", 0) // an already-awake model is not a switch
	l.Record("b", "m1", 4100)
	if got := l.For("a"); len(got) != 1 || got["m1"] != 900 {
		t.Errorf("For(a) = %v", got)
	}
	merged := mergeSwitchCosts(map[string]int64{"m1": 5, "x": 7}, l.For("a"))
	if merged["m1"] != 900 || merged["x"] != 7 {
		t.Errorf("merged = %v: observed wins, reported others stay", merged)
	}
	if got := mergeSwitchCosts(map[string]int64{"x": 1}, nil); got["x"] != 1 || len(got) != 1 {
		t.Errorf("nothing observed leaves what the engine reported: %v", got)
	}
	l.Forget("a")
	if l.For("a") != nil || l.For("b")["m1"] != 4100 {
		t.Error("Forget must drop one provider only")
	}
}

func TestRefreshAfterSwitchRecordsTheCostForThePublishedState(t *testing.T) {
	r := &ModelProviderReconciler{}
	p := vllmProvider()
	srv := fakeVLLM{metrics: vllmMetrics(0, 0, 0)}.start(t, vllmModel14)
	p.Spec.Servers = []aiv1alpha1.VLLMServer{server(vllmModel14, srv.URL)}
	r.RefreshAfterSwitch(context.Background(), p, vllmModel14, 1234)
	if got := r.switches.For(p.Name)[vllmModel14]; got != 1234 {
		t.Errorf("recorded %d, want 1234", got)
	}
}

func TestARequestQueuedBehindAStuckWakeEndsAtTheSafetyLimit(t *testing.T) {
	adapter := asleepAwake()
	adapter.hold = 2 * time.Second // the first wake outlasts the 300 ms safety limit
	s, _ := lifecycleServer(t, adapter, namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM))
	first := make(chan bus.ModelLifecycleReply, 1)
	go func() {
		first <- s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, vllmModel14))
	}()
	time.Sleep(50 * time.Millisecond) // the first request holds the provider's turn
	start := time.Now()
	queued := s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionRelease, "qwen3:32b"))
	if queued.Ok || time.Since(start) > time.Second {
		t.Errorf("the queued request must give up with the limit, not wait for the stuck one: %+v after %v", queued, time.Since(start))
	}
	<-first
}

func TestTwoRequestsToWakeTheSameModelWakeItOnce(t *testing.T) {
	adapter := asleepAwake()
	adapter.hold = 50 * time.Millisecond
	s, ref := lifecycleServer(t, adapter, namedProvider(lifecycleProvider, aiv1alpha1.ProviderTypeVLLM))
	replies := make(chan bus.ModelLifecycleReply, 2)
	for range 2 {
		go func() {
			replies <- s.Handle(context.Background(), lifecycleSubject, request(t, bus.ModelLifecycleRequestActionReady, vllmModel14))
		}()
	}
	for range 2 {
		if r := <-replies; !r.Ok || r.State != bus.ModelLifecycleReplyStateAwake {
			t.Fatalf("reply = %+v", r)
		}
	}
	if len(adapter.readied) != 1 || len(ref.calls) != 1 {
		t.Errorf("the second request waited on the first wake instead of waking again: readied %v, published %v",
			adapter.readied, ref.calls)
	}
}
