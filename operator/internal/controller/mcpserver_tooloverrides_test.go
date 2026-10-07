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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	toolOverrideServerName = "mcp-under-test"
	toolOverrideTool       = "helm_list"
)

func toolOverrideServer(overrides ...kubemootv1alpha1.ToolOverride) *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: toolOverrideServerName, Namespace: ehNamespace},
		Spec:       kubemootv1alpha1.MCPServerSpec{ToolOverrides: overrides},
		Status:     kubemootv1alpha1.MCPServerStatus{Endpoint: "http://k8s.tools.svc:8080"},
	}
}

func TestRegistrationPayloadCarriesToolOverrides(t *testing.T) {
	r := &MCPGatewayReconciler{}
	mcp := toolOverrideServer(kubemootv1alpha1.ToolOverride{
		Name:        toolOverrideTool,
		Description: "truthful",
		Parameters:  map[string]kubemootv1alpha1.ToolParameterOverride{"namespace": {Description: "this namespace"}},
	})

	payload := r.buildRegistrationPayload(context.Background(), kubemootv1alpha1.ImplementationKubemoot, mcp)

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ToolOverrides []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  map[string]struct {
				Description string `json:"description"`
			} `json:"parameters"`
		} `json:"toolOverrides"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.ToolOverrides) != 1 || got.ToolOverrides[0].Name != toolOverrideTool ||
		got.ToolOverrides[0].Parameters["namespace"].Description != "this namespace" {
		t.Errorf("payload overrides = %s", raw)
	}
}

func TestRegistrationPayloadOmitsOverridesWhenUnset(t *testing.T) {
	r := &MCPGatewayReconciler{}
	payload := r.buildRegistrationPayload(context.Background(), kubemootv1alpha1.ImplementationKubemoot, toolOverrideServer())
	if _, ok := payload["toolOverrides"]; ok {
		t.Errorf("no overrides configured, payload should not carry the key: %v", payload)
	}
}

func TestRegistrationPayloadSkipsOverridesForContextForge(t *testing.T) {
	r := &MCPGatewayReconciler{}
	mcp := toolOverrideServer(kubemootv1alpha1.ToolOverride{Name: toolOverrideTool, Description: "x"})
	payload := r.buildRegistrationPayload(context.Background(), kubemootv1alpha1.ImplementationContextForge, mcp)
	if _, ok := payload["toolOverrides"]; ok {
		t.Errorf("ContextForge does not take overrides: %v", payload)
	}
}
