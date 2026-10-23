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
	"maps"
	"net/http"
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func twoServers(a, b *fakeServer) aiv1alpha1.ModelServerSpec {
	return aiv1alpha1.ModelServerSpec{
		Type: aiv1alpha1.ServerTypeVLLM,
		Servers: []aiv1alpha1.VLLMServer{
			{Model: qwen14, Endpoint: a.URL},
			{Model: qwen8, Endpoint: b.URL},
		},
	}
}

func TestProbeReportsEachModelsContextWindow(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.maxModelLen = 32768
	b := newFake(t, qwen8, true) // asleep: /v1/models still answers
	b.maxModelLen = 16384
	snap, err := New(http.DefaultClient).Probe(context.Background(), twoServers(a, b))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{qwen14: 32768, qwen8: 16384}
	if !maps.Equal(snap.ContextLengths, want) {
		t.Errorf("context lengths = %v, want %v", snap.ContextLengths, want)
	}
}

func TestProbeLeavesOutAModelWhoseServerReportsNoWindow(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.maxModelLen = 32768
	b := newFake(t, qwen8, false) // an older vLLM: no max_model_len
	snap, err := New(http.DefaultClient).Probe(context.Background(), twoServers(a, b))
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(snap.ContextLengths, map[string]int{qwen14: 32768}) {
		t.Errorf("context lengths = %v, want only %s", snap.ContextLengths, qwen14)
	}
}

func TestProbeReportsTheWindowOfEveryAlias(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.aliases = []string{qwen14Alias}
	a.maxModelLen = 8192
	b := newFake(t, qwen8, false)
	snap, err := New(http.DefaultClient).Probe(context.Background(), aliased(a, b))
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(snap.ContextLengths, map[string]int{qwen14: 8192, qwen14Alias: 8192}) {
		t.Errorf("context lengths = %v", snap.ContextLengths)
	}
}
