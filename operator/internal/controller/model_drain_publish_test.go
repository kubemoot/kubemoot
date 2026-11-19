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
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// kvFake is an in-memory KV bucket set the state publish and the ticket reads use.
type kvFake struct {
	mu      sync.Mutex
	data    map[string][]byte
	failPut error
}

func newKV() *kvFake { return &kvFake{data: map[string][]byte{}} }

func (k *kvFake) PutKVValue(bucket, key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failPut != nil {
		return k.failPut
	}
	k.data[bucket+"/"+key] = value
	return nil
}

func (k *kvFake) GetKVValue(bucket, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.data[bucket+"/"+key], nil
}

func (k *kvFake) ListKVKeys(bucket string) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for key := range k.data {
		if rest, ok := strings.CutPrefix(key, bucket+"/"); ok {
			out = append(out, rest)
		}
	}
	sort.Strings(out)
	return out, nil
}

// published is the state last written for the server.
func (k *kvFake) published(t *testing.T, server string) ServerState {
	t.Helper()
	raw, _ := k.GetKVValue(ServerStateBucket, server)
	if raw == nil {
		t.Fatalf("nothing published for %s", server)
	}
	var s ServerState
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// deadVLLM is a server whose vLLM servers do not answer, so every probe of it fails.
func deadVLLM() *aiv1alpha1.ModelServer {
	p := vllmServer()
	p.Spec.Servers = []aiv1alpha1.VLLMServer{server(vllmModel14, "http://127.0.0.1:1")}
	return p
}

func TestDrainingIsPublishedEvenWhenTheProbeFails(t *testing.T) {
	kv := newKV()
	r := &ModelServerReconciler{StateStore: kv}
	p := deadVLLM()
	if err := r.SetDraining(context.Background(), p, []string{vllmModel14}, true); err != nil {
		t.Fatal(err)
	}
	if got := kv.published(t, p.Name).ModelStates[vllmModel14]; got != string(engine.StateDraining) {
		t.Errorf("published modelStates[%s] = %q after a failed probe, want draining", vllmModel14, got)
	}
}

func TestAFailureToPublishDrainingIsReturned(t *testing.T) {
	kv := newKV()
	kv.failPut = errors.New("bus down")
	r := &ModelServerReconciler{StateStore: kv}
	if err := r.SetDraining(context.Background(), deadVLLM(), []string{vllmModel14}, true); err == nil {
		t.Error("a mark nothing published must fail the switch")
	}
}

func TestClearingDrainingPublishesEvenWhenTheProbeFails(t *testing.T) {
	kv := newKV()
	r := &ModelServerReconciler{StateStore: kv}
	p := deadVLLM()
	_ = r.SetDraining(context.Background(), p, []string{vllmModel14}, true)
	if err := r.SetDraining(context.Background(), p, []string{vllmModel14}, false); err != nil {
		t.Fatal(err)
	}
	if got, ok := kv.published(t, p.Name).ModelStates[vllmModel14]; ok {
		t.Errorf("published modelStates[%s] = %q after the clear, want it gone", vllmModel14, got)
	}
}

func ticket(t *testing.T, kv *kvFake, server, id, model string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{jsonKeyServer: server, "modelName": model, "modelFootprintMiB": 0})
	_ = kv.PutKVValue(bus.ServerTicketsBucket, server+"__"+id, raw)
}

func TestPlacedCallsCountsTheTicketsOnTheServerForTheModelsOnly(t *testing.T) {
	kv := newKV()
	r := &ModelServerReconciler{StateStore: kv}
	p := vllmServer()
	ticket(t, kv, p.Name, "a", vllmModel14)
	ticket(t, kv, p.Name, "b", "alias14")
	ticket(t, kv, p.Name, "c", vllmModel8)                                             // another model: the call that asked for the switch
	ticket(t, kv, "other-server", "d", vllmModel14)                                    // another ModelServer
	_ = kv.PutKVValue(bus.ServerTicketsBucket, p.Name+"__e", []byte(`{"server":"x"}`)) // no model name
	n, err := r.PlacedCalls(context.Background(), p, []string{vllmModel14, "alias14"})
	if err != nil || n != 2 {
		t.Errorf("placed = %d, %v; want 2", n, err)
	}
	if n, _ := (&ModelServerReconciler{}).PlacedCalls(context.Background(), p, []string{vllmModel14}); n != 0 {
		t.Errorf("with no KV there are no tickets, got %d", n)
	}
}

func TestStartRepublishesAStaleDrainingEntryOnceTheBusIsReady(t *testing.T) {
	kv := newKV()
	p := deadVLLM()
	stale, _ := json.Marshal(ServerState{Name: p.Name, ModelStates: map[string]string{vllmModel14: "draining"}})
	_ = kv.PutKVValue(ServerStateBucket, p.Name, stale)

	scheme := agentReconcileScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).Build()
	r := &ModelServerReconciler{StateStore: kv}
	polls := 0
	rep := &ServerStateRepublisher{Reader: reader, Servers: r, PollInterval: time.Millisecond,
		Ready: func() error {
			polls++
			if polls < 3 {
				return errors.New("bus not ready")
			}
			return nil
		}}
	if err := rep.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if polls < 3 {
		t.Errorf("the publish must wait for the bus by state, polled %d times", polls)
	}
	if got, ok := kv.published(t, p.Name).ModelStates[vllmModel14]; ok && got == "draining" {
		t.Error("the stale draining entry was not overwritten at start")
	}
}
