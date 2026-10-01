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
	"strings"
	"testing"

	"k8s.io/utils/ptr"
)

func TestDefaultTrueAccessors(t *testing.T) {
	cases := []struct {
		name string
		in   *bool
		want bool
	}{
		{"unset means true", nil, true},
		{"explicit true", ptr.To(true), true},
		{"explicit false", ptr.To(false), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (&MCPGatewaySpec{AdminUI: c.in}).AdminUIEnabled(); got != c.want {
				t.Errorf("AdminUIEnabled = %v, want %v", got, c.want)
			}
			if got := (&DiscussionConfig{Enabled: c.in}).IsEnabled(); got != c.want {
				t.Errorf("DiscussionConfig.IsEnabled = %v, want %v", got, c.want)
			}
			if got := (&TestedConfig{BlockBroken: c.in}).BlockBrokenEnabled(); got != c.want {
				t.Errorf("BlockBrokenEnabled = %v, want %v", got, c.want)
			}
			if got := BoolOrTrue(c.in); got != c.want {
				t.Errorf("BoolOrTrue = %v, want %v", got, c.want)
			}
		})
	}
}

// An explicit false is encoded and decoded as false, so a controller that reads
// and writes the object back keeps it.
func TestExplicitFalseSurvivesRoundTrip(t *testing.T) {
	in := MCPGatewaySpec{AdminUI: ptr.To(false)}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"adminUI":false`) {
		t.Fatalf("explicit false dropped on encode: %s", raw)
	}
	var out MCPGatewaySpec
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.AdminUIEnabled() {
		t.Error("explicit false decoded as enabled")
	}
}

// Objects stored before the type change hold a JSON bool or nothing; both decode
// into the pointer field unchanged, so no migration decoder is needed.
func TestStoredFormsDecode(t *testing.T) {
	cases := map[string]bool{`{}`: true, `{"adminUI":true}`: true, `{"adminUI":false}`: false}
	for raw, want := range cases {
		var s MCPGatewaySpec
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if s.AdminUIEnabled() != want {
			t.Errorf("%s: AdminUIEnabled = %v, want %v", raw, s.AdminUIEnabled(), want)
		}
	}
	var bad MCPGatewaySpec
	if err := json.Unmarshal([]byte(`{"adminUI":"yes"}`), &bad); err == nil {
		t.Error("a non-boolean adminUI must be rejected, as the CRD schema does")
	}
}
