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

package engine

import (
	"context"
	"net/http"
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

type stubAdapter struct{ client *http.Client }

func (stubAdapter) Probe(context.Context, string) (Snapshot, error) { return Snapshot{}, nil }
func (stubAdapter) Ready(context.Context, string, string) error     { return nil }
func (stubAdapter) Release(context.Context, string, string) error   { return nil }

func TestRegistryReturnsTheRegisteredAdapter(t *testing.T) {
	reg := NewRegistry()
	reg.Register(aiv1alpha1.ProviderTypeOllama, func(c *http.Client) Adapter { return stubAdapter{client: c} })
	client := &http.Client{}
	got, err := reg.Adapter(aiv1alpha1.ProviderTypeOllama, client)
	if err != nil {
		t.Fatal(err)
	}
	if got.(stubAdapter).client != client {
		t.Error("the factory must receive the caller's client")
	}
}

func TestRegistryRefusesAnUnknownType(t *testing.T) {
	reg := NewRegistry()
	reg.Register(aiv1alpha1.ProviderTypeOllama, func(*http.Client) Adapter { return stubAdapter{} })
	for _, unknown := range []aiv1alpha1.ProviderType{"vllm", "", "OLLAMA"} {
		if _, err := reg.Adapter(unknown, nil); err == nil {
			t.Errorf("type %q has no adapter and must fail", unknown)
		}
	}
}

func TestSnapshotVRAMUsedMiB(t *testing.T) {
	snap := Snapshot{Loaded: []aiv1alpha1.LoadedModel{
		{Name: "a", SizeVRAM: 1 << 30},
		{Name: "b", SizeVRAM: 1 << 30, Size: 5 << 30},
		{Name: "cpu", Size: 3 << 30},
	}}
	if got := snap.VRAMUsedMiB(); got != 2048 {
		t.Errorf("VRAMUsedMiB = %d, want 2048 (host residency is not counted)", got)
	}
	if got := (Snapshot{}).VRAMUsedMiB(); got != 0 {
		t.Errorf("empty snapshot = %d, want 0", got)
	}
}
