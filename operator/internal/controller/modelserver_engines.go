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
	"sync"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/internal/engine/ollama"
	"github.com/kubemoot/kubemoot/operator/internal/engine/vllm"
)

var (
	defaultEngines     *engine.Registry
	defaultEnginesOnce sync.Once
)

// DefaultEngines is the one registry of engine adapters, by ModelServer
// spec.type. The CRD enum decides which types admission accepts; this registry
// decides which of them have an adapter.
func DefaultEngines() *engine.Registry {
	defaultEnginesOnce.Do(func() {
		defaultEngines = engine.NewRegistry()
		defaultEngines.Register(aiv1alpha1.ServerTypeOllama, ollama.Factory)
		defaultEngines.Register(aiv1alpha1.ServerTypeVLLM, vllm.Factory)
	})
	return defaultEngines
}
