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
	"fmt"
	"net/http"
	"sync"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Factory builds an Adapter that makes its HTTP calls with client.
type Factory func(client *http.Client) Adapter

// Registry maps a ModelServer spec.type to the adapter for that engine.
type Registry struct {
	mu        sync.RWMutex
	factories map[aiv1alpha1.ServerType]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[aiv1alpha1.ServerType]Factory{}}
}

// Register adds the factory for a server type, replacing any earlier one.
func (r *Registry) Register(t aiv1alpha1.ServerType, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[t] = f
}

// Adapter returns the adapter for a server type, built over client. It fails
// for a type no adapter is registered for.
func (r *Registry) Adapter(t aiv1alpha1.ServerType, client *http.Client) (Adapter, error) {
	r.mu.RLock()
	f, ok := r.factories[t]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no engine adapter for server type %q", t)
	}
	return f(client), nil
}
