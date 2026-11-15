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

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func loadMCPServerSpecSchema(t *testing.T, path ...string) apiextensionsv1.JSONSchemaProps {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, path...)...))
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatal(err)
	}
	return crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
}

func TestMCPServerCRDToolOverridesSchema(t *testing.T) {
	spec := loadMCPServerSpecSchema(t, "config", "crd", "bases", "kubemoot.ai_mcpservers.yaml")
	overrides, ok := spec.Properties["toolOverrides"]
	if !ok {
		t.Fatal("spec.toolOverrides is missing from the CRD")
	}
	if overrides.Type != "array" || overrides.MaxItems == nil || *overrides.MaxItems != 64 {
		t.Errorf("toolOverrides should be an array capped at 64 items, got %+v", overrides)
	}
	if overrides.XListType == nil || *overrides.XListType != "map" {
		t.Errorf("toolOverrides should be a map list so tool names are unique")
	}
	checkToolOverrideItemSchema(t, overrides.Items.Schema)
}

func checkToolOverrideItemSchema(t *testing.T, item *apiextensionsv1.JSONSchemaProps) {
	t.Helper()
	if len(item.Required) != 1 || item.Required[0] != "name" {
		t.Errorf("only name is required on an override, got %v", item.Required)
	}
	if item.Properties["name"].MinLength == nil || *item.Properties["name"].MinLength != 1 {
		t.Errorf("an empty tool name must be rejected")
	}
	param := item.Properties["parameters"].AdditionalProperties.Schema
	if len(param.Required) != 1 || param.Required[0] != "description" {
		t.Errorf("a parameter override requires its description, got %v", param.Required)
	}
}

func TestMCPServerChartCRDMatchesGenerated(t *testing.T) {
	generated, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", "kubemoot.ai_mcpservers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	chart, err := os.ReadFile(filepath.Join("..", "..", "chart", "kubemoot-operator", "crds", "kubemoot.ai_mcpservers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, chart) {
		t.Error("chart CRD differs from config/crd/bases; run make manifests")
	}
}

func TestMCPServerToolOverridesRoundTrip(t *testing.T) {
	in := `
toolOverrides:
- name: helm_list
  description: lists one namespace
  parameters:
    namespace:
      description: only this namespace
`
	var spec MCPServerSpec
	if err := yaml.UnmarshalStrict([]byte(in), &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.ToolOverrides) != 1 || spec.ToolOverrides[0].Parameters["namespace"].Description != "only this namespace" {
		t.Errorf("unexpected overrides: %+v", spec.ToolOverrides)
	}
	if err := yaml.UnmarshalStrict([]byte("toolOverrides:\n- name: x\n  behavior: y\n"), &spec); err == nil {
		t.Error("an unknown override field must be rejected")
	}
}

func TestMCPServerCRDToolArgumentRulesSchema(t *testing.T) {
	spec := loadMCPServerSpecSchema(t, "config", "crd", "bases", "kubemoot.ai_mcpservers.yaml")
	param := spec.Properties["toolOverrides"].Items.Schema.Properties["parameters"].AdditionalProperties.Schema
	rules, ok := param.Properties["rules"]
	if !ok {
		t.Fatal("a parameter override has no rules in the CRD")
	}
	if rules.MaxItems == nil || *rules.MaxItems != 16 {
		t.Errorf("rules should be capped at 16 items, got %+v", rules.MaxItems)
	}
	rule := rules.Items.Schema
	if len(rule.Required) != 2 {
		t.Errorf("reject and message are both required, got %v", rule.Required)
	}
	reject := rule.Properties["reject"]
	if reject.MaxLength == nil || len(reject.XValidations) != 1 {
		t.Errorf("reject needs a length cap and the regex check, got %+v", reject)
	}
	if len(param.Required) != 1 || param.Required[0] != "description" {
		t.Errorf("adding rules must not change what a parameter requires, got %v", param.Required)
	}
}

func TestMCPServerToolArgumentRulesRoundTrip(t *testing.T) {
	in := `
toolOverrides:
- name: resources_list
  parameters:
    fieldSelector:
      description: a selector
      rules:
      - reject: "[<>]"
        message: no comparison operators
`
	var spec MCPServerSpec
	if err := yaml.UnmarshalStrict([]byte(in), &spec); err != nil {
		t.Fatal(err)
	}
	rules := spec.ToolOverrides[0].Parameters["fieldSelector"].Rules
	if len(rules) != 1 || rules[0].Reject != "[<>]" || rules[0].Message != "no comparison operators" {
		t.Errorf("unexpected rules: %+v", rules)
	}
	if err := yaml.UnmarshalStrict([]byte("toolOverrides:\n- name: x\n  parameters:\n    p:\n      description: d\n      rules:\n      - allow: y\n"), &spec); err == nil {
		t.Error("an unknown rule field must be rejected")
	}
}
