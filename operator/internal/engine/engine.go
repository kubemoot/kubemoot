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

// Package engine is the seam between the operator and a model server's engine
// (Ollama today). An Adapter probes one provider and reports what the published
// provider-state contract carries, and asks the engine to make a model ready or
// release it. The package holds the interface, the snapshot a probe returns, and
// the registry that picks an adapter by ModelProvider spec.type; each engine's
// adapter lives in its own package below and is registered by the caller.
//
// An endpoint is a provider, which may be one server or a pool of servers behind
// one address, so nothing here assumes one process.
package engine

import (
	"context"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// ModelState is where one model stands on a provider.
type ModelState string

const (
	// StateAwake is a model loaded and ready to answer now.
	StateAwake ModelState = "awake"
	// StateAsleep is a model the engine parked and can wake quickly.
	StateAsleep ModelState = "asleep"
	// StateAvailable is a model the provider has but has not loaded.
	StateAvailable ModelState = "available"
)

// Snapshot is what one probe of a provider found. A field the engine does not
// report, or could not read this time, is nil or false; that means "unknown",
// never "none".
type Snapshot struct {
	// Loaded is the models loaded now, with their memory use. It is meaningful
	// only when LoadedKnown is true; an empty list then means nothing is loaded.
	Loaded      []aiv1alpha1.LoadedModel
	LoadedKnown bool
	// Available is the models the provider has downloaded, loaded or not. It is
	// meaningful only when AvailableKnown is true.
	Available      []aiv1alpha1.AvailableModel
	AvailableKnown bool

	// ModelStates is the state of each model by name; nil when the engine could
	// not tell which models are loaded.
	ModelStates map[string]ModelState
	// RequestsRunning and RequestsWaiting are the engine's own request counts.
	RequestsRunning *int
	RequestsWaiting *int
	// KVCacheUsage is the fraction of the engine's key-value cache in use, 0 to 1.
	KVCacheUsage *float64
	// SwitchCostMs is the observed time to make each model ready, by model.
	SwitchCostMs map[string]int64
	// HostMemoryParkedMiB is the host memory holding the weights of sleeping models.
	HostMemoryParkedMiB *int64
}

// VRAMUsedMiB is the accelerator memory in MiB the loaded models hold. Memory a
// model holds in host RAM is not counted.
func (s Snapshot) VRAMUsedMiB() int64 {
	var total int64
	for _, m := range s.Loaded {
		total += m.SizeVRAM
	}
	return total / (1024 * 1024)
}

// Adapter is how the operator talks to one engine. Implementations are safe for
// concurrent use.
type Adapter interface {
	// Probe asks the provider at endpoint what it holds and how busy it is. It
	// returns an error only when the provider gave no usable answer at all; a
	// partial answer returns a Snapshot with the unread fields unknown.
	Probe(ctx context.Context, endpoint string) (Snapshot, error)
	// Ready asks the provider to make model ready to answer. Load on demand stays
	// the rule: callers ask when a call needs the model, never ahead of time.
	Ready(ctx context.Context, endpoint, model string) error
	// Release asks the provider to free what model holds.
	Release(ctx context.Context, endpoint, model string) error
}
