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
	"reflect"
	"testing"
)

func TestParseReplicateSecretsAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        []SecretRef
	}{
		{
			name:        "nil annotations",
			annotations: nil,
			want:        nil,
		},
		{
			name:        "annotation absent",
			annotations: map[string]string{"other": "value"},
			want:        nil,
		},
		{
			name:        "empty value",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": ""},
			want:        nil,
		},
		{
			name:        "single name uses operator namespace",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "harbor-pull-secret"},
			want:        []SecretRef{{Name: "harbor-pull-secret"}},
		},
		{
			name:        "single ns/name pair",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "homelab-pilot/proxmox-secret"},
			want:        []SecretRef{{Name: "proxmox-secret", Namespace: "homelab-pilot"}},
		},
		{
			name:        "mixed list",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "harbor-pull-secret,homelab-pilot/proxmox-secret,db-creds"},
			want: []SecretRef{
				{Name: "harbor-pull-secret"},
				{Name: "proxmox-secret", Namespace: "homelab-pilot"},
				{Name: "db-creds"},
			},
		},
		{
			name:        "whitespace trimmed",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "  foo  ,  ns1 / bar  ,baz"},
			want: []SecretRef{
				{Name: "foo"},
				{Name: "bar", Namespace: "ns1"},
				{Name: "baz"},
			},
		},
		{
			name:        "empty entries skipped",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "foo,,bar,"},
			want: []SecretRef{
				{Name: "foo"},
				{Name: "bar"},
			},
		},
		{
			name:        "malformed entries skipped",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "/no-ns,no-name/,ns/name/extra,ok"},
			want: []SecretRef{
				{Name: "ok"},
			},
		},
		{
			name:        "only slash skipped",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "/"},
			want:        nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseReplicateSecretsAnnotation(tt.annotations)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseReplicateSecretsAnnotation() = %v, want %v", got, tt.want)
			}
		})
	}
}
