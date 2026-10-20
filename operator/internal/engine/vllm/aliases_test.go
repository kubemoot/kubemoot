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
	"net/http"
	"slices"
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

const (
	qwen14Alias = "fourteen"
	postWakeUp  = "POST /wake_up"
)

// aliased is a spec in which one server answers to two names and another to one.
func aliased(a, b *fakeServer) aiv1alpha1.ModelServerSpec {
	return aiv1alpha1.ModelServerSpec{
		Type: aiv1alpha1.ServerTypeVLLM,
		Servers: []aiv1alpha1.VLLMServer{
			{Model: qwen14, Endpoint: a.URL},
			{Model: qwen14Alias, Endpoint: a.URL},
			{Model: qwen8, Endpoint: b.URL},
		},
	}
}

func TestProbeReportsEveryNameOfAServerAndCountsItsRequestsOnce(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.aliases = []string{qwen14Alias}
	b := newFake(t, qwen8, true)
	snap, err := New(http.DefaultClient).Probe(context.Background(), aliased(a, b))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]engine.ModelState{qwen14: engine.StateAwake, qwen14Alias: engine.StateAwake, qwen8: engine.StateAsleep} {
		if snap.ModelStates[name] != want {
			t.Errorf("state of %s = %q, want %q", name, snap.ModelStates[name], want)
		}
	}
	if len(snap.Available) != 3 || len(snap.Loaded) != 2 {
		t.Errorf("available %v loaded %v", snap.Available, snap.Loaded)
	}
	// metricsPage reports 3 running and 2 waiting on each server; the aliased one counts once.
	if snap.RequestsRunning == nil || *snap.RequestsRunning != 6 || *snap.RequestsWaiting != 4 {
		t.Errorf("counts = %v/%v, want 6/4 (two servers, not three names)", snap.RequestsRunning, snap.RequestsWaiting)
	}
}

func TestProbeLeavesOutAServerThatDoesNotServeAListedAlias(t *testing.T) {
	a := newFake(t, qwen14, false) // lists no alias
	b := newFake(t, qwen8, true)
	snap, err := New(http.DefaultClient).Probe(context.Background(), aliased(a, b))
	if err != nil {
		t.Fatal(err)
	}
	if _, listed := snap.ModelStates[qwen14]; listed {
		t.Errorf("a server that does not serve a listed name is left out: %v", snap.ModelStates)
	}
	if snap.ModelStates[qwen8] != engine.StateAsleep {
		t.Errorf("the other server is still reported: %v", snap.ModelStates)
	}
	// With no server serving what is listed, the probe fails.
	b.unhealthy = true
	if _, err := New(http.DefaultClient).Probe(context.Background(), aliased(a, b)); err == nil {
		t.Error("a probe in which no server serves its listed names must fail")
	}
}

func TestReadyByAnAliasDoesNotPutTheTargetsOwnServerToSleep(t *testing.T) {
	a := newFake(t, qwen14, true)
	a.aliases = []string{qwen14Alias}
	b := newFake(t, qwen8, false)
	if err := New(http.DefaultClient).Ready(context.Background(), aliased(a, b), qwen14Alias); err != nil {
		t.Fatal(err)
	}
	if got := a.posts(); !slices.Equal(got, []string{postWakeUp}) {
		t.Errorf("the aliased server was sent %v, want only a wake", got)
	}
	if got := b.posts(); !slices.Equal(got, []string{"POST /sleep?level=1"}) {
		t.Errorf("the other server was sent %v, want one sleep", got)
	}
}
