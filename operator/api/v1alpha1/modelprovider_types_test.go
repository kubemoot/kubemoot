/*
Copyright 2025.

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

// TestAvailableModelUnmarshal_LegacyString covers the migration path: existing
// ModelProvider CRs in etcd stored availableModels as a []string of bare names.
// The typed informer cache must decode a stored string into {Name: s} rather
// than crash-looping the operator on startup.
func TestAvailableModelUnmarshal_LegacyString(t *testing.T) {
	var m AvailableModel
	if err := json.Unmarshal([]byte(`"qwen3:32b"`), &m); err != nil {
		t.Fatalf("legacy string form failed to decode: %v", err)
	}
	if m.Name != "qwen3:32b" {
		t.Errorf("Name = %q, want %q", m.Name, "qwen3:32b")
	}
	if m.SizeBytes != 0 {
		t.Errorf("SizeBytes = %d, want 0 for legacy form", m.SizeBytes)
	}
}

// TestAvailableModelUnmarshal_ObjectForm covers the current {name,sizeBytes}
// object form written by the operator after a reconcile.
func TestAvailableModelUnmarshal_ObjectForm(t *testing.T) {
	var m AvailableModel
	if err := json.Unmarshal([]byte(`{"name":"qwen3:8b","sizeBytes":4900000000}`), &m); err != nil {
		t.Fatalf("object form failed to decode: %v", err)
	}
	if m.Name != "qwen3:8b" {
		t.Errorf("Name = %q, want %q", m.Name, "qwen3:8b")
	}
	if m.SizeBytes != 4900000000 {
		t.Errorf("SizeBytes = %d, want 4900000000", m.SizeBytes)
	}
}

// TestAvailableModelUnmarshal_MixedSlice covers a status array that mixes legacy
// string entries with new object entries, which can occur transiently when the
// operator upgrades and partially rewrites the slice.
func TestAvailableModelUnmarshal_MixedSlice(t *testing.T) {
	var models []AvailableModel
	raw := `["qwen3:32b",{"name":"qwen3:8b","sizeBytes":4900000000}]`
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		t.Fatalf("mixed slice failed to decode: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len = %d, want 2", len(models))
	}
	if models[0].Name != "qwen3:32b" || models[0].SizeBytes != 0 {
		t.Errorf("models[0] = %+v, want {Name:qwen3:32b SizeBytes:0}", models[0])
	}
	if models[1].Name != "qwen3:8b" || models[1].SizeBytes != 4900000000 {
		t.Errorf("models[1] = %+v, want {Name:qwen3:8b SizeBytes:4900000000}", models[1])
	}
}

// TestAvailableModelUnmarshal_Invalid ensures malformed input still errors
// rather than silently producing a zero value.
func TestAvailableModelUnmarshal_Invalid(t *testing.T) {
	var m AvailableModel
	if err := json.Unmarshal([]byte(`12345`), &m); err == nil {
		t.Error("expected error decoding a bare number into AvailableModel, got nil")
	}
}
