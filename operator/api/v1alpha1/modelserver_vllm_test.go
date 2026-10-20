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

package v1alpha1

import "testing"

func TestVLLMServersAndEffectiveSpecFollowHowTheServersAreNamed(t *testing.T) {
	listed := VLLMServer{Model: "a", Endpoint: "http://a:8000"}
	found := VLLMServer{Model: "b", Endpoint: "http://b:8000"}

	withServers := &ModelServer{Spec: ModelServerSpec{Type: ServerTypeVLLM, Servers: []VLLMServer{listed}}}
	withServers.Status.VLLMServers = []VLLMServer{found} // ignored with spec.servers
	if got := withServers.VLLMServers(); len(got) != 1 || got[0] != listed {
		t.Errorf("servers = %v, want the listed server", got)
	}

	withRef := &ModelServer{Spec: ModelServerSpec{Type: ServerTypeVLLM, ServiceRef: &VLLMServiceRef{Name: "svc"}}}
	withRef.Status.VLLMServers = []VLLMServer{found}
	if got := withRef.VLLMServers(); len(got) != 1 || got[0] != found {
		t.Errorf("serviceRef = %v, want the discovered server", got)
	}
	if got := withRef.EndpointFor("b"); got != found.Endpoint {
		t.Errorf("EndpointFor = %q", got)
	}
	if got := withRef.EndpointFor("missing"); got != "" {
		t.Errorf("EndpointFor for a model no server serves = %q", got)
	}
	spec := withRef.EffectiveSpec()
	spec.Servers[0].Endpoint = "changed"
	if withRef.Status.VLLMServers[0].Endpoint != found.Endpoint {
		t.Error("the effective spec must not share its servers with the status")
	}
}

func TestAnOllamaServerHasNoVLLMServersAndKeepsItsEndpoint(t *testing.T) {
	ollama := &ModelServer{Spec: ModelServerSpec{Type: ServerTypeOllama, Endpoint: "http://ollama:11434"}}
	ollama.Status.VLLMServers = []VLLMServer{{Model: "x", Endpoint: "http://x"}}
	if ollama.VLLMServers() != nil || len(ollama.EffectiveSpec().Servers) != 0 {
		t.Error("an ollama server has no vLLM servers")
	}
	if ollama.EndpointFor("anything") != "http://ollama:11434" {
		t.Errorf("EndpointFor = %q", ollama.EndpointFor("anything"))
	}
}

func TestSleepLevelDefaultsToOne(t *testing.T) {
	if (&ModelServerSpec{}).SleepLevelOrDefault() != 1 || (&ModelServerSpec{SleepLevel: 2}).SleepLevelOrDefault() != 2 {
		t.Error("an unset sleep level is 1 and a set one is kept")
	}
}
