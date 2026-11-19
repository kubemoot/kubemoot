/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vllm

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

const (
	busyMetrics    = "# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running{model_name=\"x\"} 2.0\n"
	queuedMetrics  = "# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running{model_name=\"x\"} 0.0\n# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting{model_name=\"x\"} 1.0\n"
	noCountMetrics = "# TYPE vllm:kv_cache_usage_perc gauge\nvllm:kv_cache_usage_perc{model_name=\"x\"} 0.5\n"
	fastPoll       = time.Millisecond
)

// switchPair is an awake server for qwen14 and a sleeping one for qwen8, and the spec over them.
func switchPair(t *testing.T) (awake, asleep *fakeServer, adapter *Adapter) {
	t.Helper()
	awake = newFake(t, qwen14, false)
	asleep = newFake(t, qwen8, true)
	adapter = New(http.DefaultClient).WithDrain(fastPoll, 5*time.Second)
	return awake, asleep, adapter
}

func readyQwen8(adapter *Adapter, awake, asleep *fakeServer) error {
	servers := map[string]*fakeServer{qwen14: awake, qwen8: asleep}
	return adapter.Ready(context.Background(), specOf(1, servers, qwen14, qwen8), qwen8)
}

func (f *fakeServer) setMetrics(page string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics = page
}

func TestIdleSwitchIsImmediate(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(idleMetrics)
	// A poll interval longer than the test proves no wait happened.
	adapter.WithDrain(time.Hour, time.Hour)
	if err := readyQwen8(adapter, awake, asleep); err != nil {
		t.Fatal(err)
	}
	if !awake.isAsleep() || asleep.isAsleep() {
		t.Error("the idle server sleeps and the target wakes")
	}
}

func TestBusySwitchWaitsForRunningToReachZeroThenProceeds(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(busyMetrics)
	done := make(chan error, 1)
	go func() { done <- readyQwen8(adapter, awake, asleep) }()

	time.Sleep(50 * time.Millisecond) // the drain is polling a busy server
	if awake.isAsleep() {
		t.Fatal("the busy server was put to sleep while its call was running")
	}
	if len(asleep.posts()) != 0 {
		t.Fatal("the target woke before the accelerator was free")
	}
	awake.setMetrics(idleMetrics)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the switch did not proceed once the server went idle")
	}
	if !awake.isAsleep() || asleep.isAsleep() {
		t.Error("after the drain the old server sleeps and the target is awake")
	}
}

func TestRequestsWaitingInTheEngineQueueAlsoHoldTheSwitch(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(queuedMetrics)
	adapter.WithDrain(fastPoll, 30*time.Millisecond)
	err := readyQwen8(adapter, awake, asleep)
	if err == nil || !strings.Contains(err.Error(), "1 requests were still in flight") {
		t.Fatalf("err = %v, want the queued request named", err)
	}
	if awake.isAsleep() {
		t.Error("a server with a queued request must stay awake")
	}
}

func TestSafetyNetLeavesTheModelAwakeAndSaysWhy(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(busyMetrics)
	adapter.WithDrain(fastPoll, 30*time.Millisecond)
	err := readyQwen8(adapter, awake, asleep)
	if err == nil {
		t.Fatal("a drain that never ends must fail the switch")
	}
	for _, want := range []string{qwen14, "2 requests were still in flight", "safety limit", "stays awake"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	if awake.isAsleep() {
		t.Error("the safety net must leave the model awake")
	}
	if len(awake.posts()) != 0 || len(asleep.posts()) != 0 {
		t.Errorf("no sleep or wake may be sent: %v %v", awake.posts(), asleep.posts())
	}
}

func TestUnavailableMetricFailsTheSwitchWithoutSleeping(t *testing.T) {
	cases := map[string]func(*fakeServer){
		"page without the running gauge": func(f *fakeServer) { f.setMetrics(noCountMetrics) },
		"metrics endpoint absent":        func(f *fakeServer) { f.setMetrics("") },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			awake, asleep, adapter := switchPair(t)
			set(awake)
			err := readyQwen8(adapter, awake, asleep)
			if err == nil || !strings.Contains(err.Error(), "cannot tell whether it is idle") {
				t.Fatalf("err = %v, want a cannot-tell-idle reason", err)
			}
			if awake.isAsleep() || len(asleep.posts()) != 0 {
				t.Error("an unreadable count must not cut a call: the model stays awake and the target does not wake")
			}
		})
	}
}

func TestAskerGivingUpDuringTheDrainLeavesTheModelAwake(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(busyMetrics)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	servers := map[string]*fakeServer{qwen14: awake, qwen8: asleep}
	err := adapter.Ready(ctx, specOf(1, servers, qwen14, qwen8), qwen8)
	if err == nil || !strings.Contains(err.Error(), "the request ended") {
		t.Fatalf("err = %v, want the request-ended reason", err)
	}
	if awake.isAsleep() {
		t.Error("the model stays awake")
	}
}

func TestReleaseWaitsForTheServerToGoIdle(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.setMetrics(busyMetrics)
	adapter := New(http.DefaultClient).WithDrain(fastPoll, 5*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- adapter.Release(context.Background(), specOf(1, map[string]*fakeServer{qwen14: a}, qwen14), qwen14)
	}()
	time.Sleep(50 * time.Millisecond)
	if a.isAsleep() {
		t.Fatal("released while a call was running")
	}
	a.setMetrics(idleMetrics)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !a.isAsleep() {
		t.Error("the server sleeps once idle")
	}
}

func TestDrainDefaults(t *testing.T) {
	a := New(http.DefaultClient)
	if a.pollEvery() != DefaultDrainPoll || a.limit() != DefaultDrainLimit {
		t.Errorf("defaults = %v %v", a.pollEvery(), a.limit())
	}
}

// drainEvents records what an adapter reports to its DrainNotifier, and the /metrics reads
// the server had received by the time of each report.
type drainEvents struct {
	mu     sync.Mutex
	events []string
	reads  []int
	server *fakeServer
}

func (d *drainEvents) notify(_ context.Context, models []string, draining bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := "end"
	if draining {
		state = "start"
	}
	d.events = append(d.events, state+":"+strings.Join(models, ","))
	n := 0
	for _, c := range d.server.calls() {
		if strings.HasPrefix(c, "GET "+pathMetrics) {
			n++
		}
	}
	d.reads = append(d.reads, n)
	return nil
}

func TestDrainingIsPublishedBeforeTheCountIsReadAndClearedAfterTheSleep(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(busyMetrics)
	events := &drainEvents{server: awake}
	ctx := engine.WithDrain(context.Background(), engine.Drain{Notify: events.notify})
	servers := map[string]*fakeServer{qwen14: awake, qwen8: asleep}
	spec := specOf(1, servers, qwen14, qwen8)

	done := make(chan error, 1)
	go func() { done <- adapter.Ready(ctx, spec, qwen8) }()
	time.Sleep(50 * time.Millisecond)
	awake.setMetrics(idleMetrics) // the drain completes after the state was published
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events.events, []string{"start:" + qwen14, "end:" + qwen14}) {
		t.Fatalf("events = %v", events.events)
	}
	if events.reads[0] != 0 {
		t.Errorf("draining must be published before the first read of the count, %d reads had happened", events.reads[0])
	}
	if !awake.isAsleep() {
		t.Error("the server sleeps once the drain completes")
	}
}

func TestDrainingIsClearedWhenTheSwitchFailsAndTheModelStaysAwake(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(busyMetrics)
	adapter.WithDrain(fastPoll, 30*time.Millisecond)
	events := &drainEvents{server: awake}
	ctx := engine.WithDrain(context.Background(), engine.Drain{Notify: events.notify})
	servers := map[string]*fakeServer{qwen14: awake, qwen8: asleep}
	if err := adapter.Ready(ctx, specOf(1, servers, qwen14, qwen8), qwen8); err == nil {
		t.Fatal("the safety net must fail the switch")
	}
	if !slices.Equal(events.events, []string{"start:" + qwen14, "end:" + qwen14}) {
		t.Errorf("a model that stays awake must stop being draining: %v", events.events)
	}
}

func TestReleaseReportsEveryNameOfTheServerDraining(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.aliases = []string{"alias14"}
	a.setMetrics(idleMetrics)
	events := &drainEvents{server: a}
	ctx := engine.WithDrain(context.Background(), engine.Drain{Notify: events.notify})
	spec := specOf(1, map[string]*fakeServer{qwen14: a}, qwen14)
	spec.Servers = append(spec.Servers, aiv1alpha1.VLLMServer{Model: "alias14", Endpoint: a.URL})
	if err := New(http.DefaultClient).Release(ctx, spec, "alias14"); err != nil {
		t.Fatal(err)
	}
	if want := "start:" + qwen14 + ",alias14"; events.events[0] != want {
		t.Errorf("events = %v, want %s first", events.events, want)
	}
}

// placedCalls is a Drain.Placed whose answer the test changes.
type placedCalls struct {
	mu  sync.Mutex
	n   int
	err error
}

func (p *placedCalls) set(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n = n
}

func (p *placedCalls) count(context.Context, []string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n, p.err
}

func TestAnIdleServerWithAPlacedCallNotYetArrivedHoldsTheSwitch(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(idleMetrics) // the engine reports nothing: the placed call has not arrived
	placed := &placedCalls{n: 1}
	ctx := engine.WithDrain(context.Background(), engine.Drain{Placed: placed.count})
	servers := map[string]*fakeServer{qwen14: awake, qwen8: asleep}
	done := make(chan error, 1)
	go func() { done <- adapter.Ready(ctx, specOf(1, servers, qwen14, qwen8), qwen8) }()

	time.Sleep(50 * time.Millisecond)
	if awake.isAsleep() {
		t.Fatal("slept with a placed call outstanding on an idle-looking server")
	}
	placed.set(0) // the call ended
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !awake.isAsleep() {
		t.Error("the server sleeps once no call is outstanding")
	}
}

func TestAPlacedCallThatNeverEndsFailsTheSwitchAtTheSafetyNet(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(idleMetrics)
	adapter.WithDrain(fastPoll, 30*time.Millisecond)
	ctx := engine.WithDrain(context.Background(), engine.Drain{Placed: (&placedCalls{n: 2}).count})
	servers := map[string]*fakeServer{qwen14: awake, qwen8: asleep}
	err := adapter.Ready(ctx, specOf(1, servers, qwen14, qwen8), qwen8)
	if err == nil || !strings.Contains(err.Error(), "2 requests were still in flight or placed") {
		t.Fatalf("err = %v", err)
	}
	if awake.isAsleep() {
		t.Error("the model stays awake")
	}
}

func TestPlacedCallsThatCannotBeReadFailTheSwitchWithoutSleeping(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(idleMetrics)
	ctx := engine.WithDrain(context.Background(), engine.Drain{Placed: (&placedCalls{err: errors.New("bucket gone")}).count})
	servers := map[string]*fakeServer{qwen14: awake, qwen8: asleep}
	err := adapter.Ready(ctx, specOf(1, servers, qwen14, qwen8), qwen8)
	if err == nil || !strings.Contains(err.Error(), "cannot tell whether it is idle") || !strings.Contains(err.Error(), "bucket gone") {
		t.Fatalf("err = %v", err)
	}
	if awake.isAsleep() {
		t.Error("an unreadable ledger must not cut a call")
	}
}

func TestAFailureToPublishDrainingFailsTheSwitchBeforeAnythingIsRead(t *testing.T) {
	awake, asleep, adapter := switchPair(t)
	awake.setMetrics(idleMetrics)
	ctx := engine.WithDrain(context.Background(), engine.Drain{
		Notify: func(_ context.Context, _ []string, draining bool) error {
			if draining {
				return errors.New("bus down")
			}
			return nil
		},
	})
	servers := map[string]*fakeServer{qwen14: awake, qwen8: asleep}
	err := adapter.Ready(ctx, specOf(1, servers, qwen14, qwen8), qwen8)
	if err == nil || !strings.Contains(err.Error(), "could not publish it as draining") {
		t.Fatalf("err = %v", err)
	}
	if awake.isAsleep() || len(awake.posts()) != 0 {
		t.Error("with nothing stopping new calls the server must stay awake")
	}
}
