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

package controller

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

func TestDrainLogMarksAndClearsModelsPerServer(t *testing.T) {
	var l drainLog
	l.Set("a", []string{"m2", "m1"}, true)
	l.Set("b", []string{"m1"}, true)
	if got := l.For("a"); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Errorf("a = %v", got)
	}
	l.Set("a", []string{"m1", "m2"}, false)
	if l.For("a") != nil || len(l.For("b")) != 1 {
		t.Error("clearing one server must leave the other draining")
	}
	l.Forget("b")
	if l.For("b") != nil {
		t.Error("Forget drops the server")
	}
	l.Set("c", []string{"x"}, false) // clearing what was never set is harmless
}

func TestADrainingModelIsPublishedAsDrainingAndLeavesTheLoadedModels(t *testing.T) {
	state := ServerState{
		LoadedModels:             []string{vllmModel14, candModel32},
		LoadedModelFootprintsMiB: map[string]int64{vllmModel14: 1, candModel32: 2},
		ModelStates:              map[string]string{vllmModel14: string(engine.StateAwake), candModel32: string(engine.StateAsleep)},
	}
	applyDraining(&state, []string{vllmModel14})
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var back ServerState
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ModelStates[vllmModel14] != string(engine.StateDraining) || back.ModelStates[candModel32] != string(engine.StateAsleep) {
		t.Errorf("modelStates = %v", back.ModelStates)
	}
	if slices.Contains(back.LoadedModels, vllmModel14) || len(back.LoadedModelFootprintsMiB) != 1 {
		t.Errorf("a draining model is not awake for readers that skip modelStates: %v %v", back.LoadedModels, back.LoadedModelFootprintsMiB)
	}
}

func TestApplyDrainingWithNothingDrainingChangesNothing(t *testing.T) {
	state := ServerState{LoadedModels: []string{vllmModel14}}
	applyDraining(&state, nil)
	if state.ModelStates != nil || len(state.LoadedModels) != 1 {
		t.Errorf("state = %+v", state)
	}
}

func TestSetDrainingKeepsTheMarkAcrossARefreshThatCannotProbe(t *testing.T) {
	r := &ModelServerReconciler{}
	p := vllmServer()
	p.Spec.Servers = nil // nothing to probe: the refresh fails and the mark must stay
	r.SetDraining(context.Background(), p, []string{vllmModel14}, true)
	if got := r.drains.For(p.Name); !slices.Equal(got, []string{vllmModel14}) {
		t.Errorf("drains = %v", got)
	}
	r.SetDraining(context.Background(), p, []string{vllmModel14}, false)
	if r.drains.For(p.Name) != nil {
		t.Error("the mark clears when the switch ends")
	}
}
