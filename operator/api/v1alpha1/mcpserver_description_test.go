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
	"encoding/json"
	"testing"
)

const (
	crdDir         = "crd"
	crdsDir        = "crds"
	descriptionKey = "description"
)

// The description is optional, and the chart's copy of the CRD carries it as the
// generated one does.
func TestMCPServerDescriptionIsAnOptionalBoundedString(t *testing.T) {
	for _, dir := range [][]string{{"..", "..", "config", crdDir, "bases"}, {"..", "..", "chart", "kubemoot-operator", crdsDir}} {
		crd := loadCRD(t, append(dir, "kubemoot.ai_mcpservers.yaml")...)
		spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
		description, ok := spec.Properties[descriptionKey]
		if !ok {
			t.Fatalf("%v: spec has no description field", dir)
		}
		if description.Type != "string" || description.MaxLength == nil || *description.MaxLength != 500 {
			t.Errorf("%v: description = %+v, want a string of at most 500 characters", dir, description)
		}
		for _, required := range spec.Required {
			if required == descriptionKey {
				t.Errorf("%v: description must be optional", dir)
			}
		}
	}
}

func TestMCPServerDescriptionIsOmittedWhenEmpty(t *testing.T) {
	empty, err := json.Marshal(MCPServerSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if !jsonHasNoKey(t, empty, descriptionKey) {
		t.Errorf("empty spec marshals with a description: %s", empty)
	}
	set, err := json.Marshal(MCPServerSpec{Description: "Searches the catalog."})
	if err != nil {
		t.Fatal(err)
	}
	if jsonHasNoKey(t, set, descriptionKey) {
		t.Errorf("set description missing from %s", set)
	}
}

func jsonHasNoKey(t *testing.T, data []byte, key string) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	_, present := m[key]
	return !present
}
