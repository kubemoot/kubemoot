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

// FuzzAvailableModelUnmarshal feeds arbitrary stored status JSON to the tolerant
// AvailableModel decoder. A decoded value re-encodes to the object form and
// decodes back to itself, and a legacy bare string decodes exactly as the
// object form with that name.
func FuzzAvailableModelUnmarshal(f *testing.F) {
	for _, s := range []string{
		`"qwen3:32b"`,
		`{"name":"qwen3:8b","sizeBytes":4900000000}`,
		`["qwen3:32b",{"name":"qwen3:8b","sizeBytes":4900000000}]`,
		`12345`,
		`null`,
		`"é😀"`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var m AvailableModel
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal %+v: %v", m, err)
		}
		var back AvailableModel
		if err := json.Unmarshal(b, &back); err != nil || back != m {
			t.Fatalf("%s re-decoded to (%+v, %v), want %+v", b, back, err, m)
		}
		checkLegacyMatchesObject(t, data, m)
	})
}

// checkLegacyMatchesObject asserts a bare-string input decodes like the object
// form {"name": s}.
func checkLegacyMatchesObject(t *testing.T, data []byte, m AvailableModel) {
	t.Helper()
	var name string
	if json.Unmarshal(data, &name) != nil {
		return
	}
	obj, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		t.Fatalf("marshal object form: %v", err)
	}
	var want AvailableModel
	if err := json.Unmarshal(obj, &want); err != nil || want != m {
		t.Fatalf("legacy %s = %+v, object form = (%+v, %v)", data, m, want, err)
	}
}
