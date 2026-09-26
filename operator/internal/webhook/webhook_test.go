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

package webhook

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

// TestValidateAgent — Scheduler v2 uses spec.capabilities (open string set)
// validated indirectly via CrewSchedulingPolicy at scheduling time. The webhook
// is intentionally permissive on Agent shape now; this test pins that behavior
// so a regression to over-eager validation is caught.
func TestValidateAgent(t *testing.T) {
	cases := []*aiv1alpha1.Agent{
		// Empty agent — accepted (capabilities is optional)
		{
			ObjectMeta: metav1.ObjectMeta{Name: "test-empty"},
			Spec:       aiv1alpha1.AgentSpec{},
		},
		// Agent with capabilities — accepted
		{
			ObjectMeta: metav1.ObjectMeta{Name: "test-with-caps"},
			Spec: aiv1alpha1.AgentSpec{
				Capabilities: []string{"tool-calling", "kubernetes"},
			},
		},
		// Agent with arbitrary capability strings — accepted (open set)
		{
			ObjectMeta: metav1.ObjectMeta{Name: "test-arbitrary-caps"},
			Spec: aiv1alpha1.AgentSpec{
				Capabilities: []string{"some/future/capability"},
			},
		},
	}
	for _, agent := range cases {
		t.Run(agent.Name, func(t *testing.T) {
			if _, err := validateAgent(agent); err != nil {
				t.Errorf("unexpected error validating %s: %v", agent.Name, err)
			}
		})
	}
}

func TestValidateMCPServer(t *testing.T) {
	tests := []struct {
		name    string
		server  *aiv1alpha1.MCPServer
		wantErr string
	}{
		{
			name: "valid with image",
			server: &aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec:       aiv1alpha1.MCPServerSpec{Image: "mcp/fetch:latest"},
			},
		},
		{
			name: "valid with externalEndpoint",
			server: &aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.MCPServerSpec{
					ExternalEndpoint: "http://some-server:8080",
					Transport:        aiv1alpha1.TransportHTTP,
				},
			},
		},
		{
			name: "both image and externalEndpoint",
			server: &aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.MCPServerSpec{
					Image:            "mcp/fetch:latest",
					ExternalEndpoint: "http://some-server:8080",
				},
			},
			wantErr: "image and externalEndpoint are mutually exclusive",
		},
		{
			name: "neither image nor externalEndpoint",
			server: &aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec:       aiv1alpha1.MCPServerSpec{},
			},
			wantErr: "one of image or externalEndpoint must be set",
		},
		{
			name: "stdio with externalEndpoint",
			server: &aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.MCPServerSpec{
					ExternalEndpoint: "http://some-server:8080",
					Transport:        aiv1alpha1.TransportStdio,
				},
			},
			wantErr: "transport stdio requires a managed pod",
		},
		{
			name: "stdio with image is valid",
			server: &aiv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.MCPServerSpec{
					Image:     "mcp/fetch:latest",
					Transport: aiv1alpha1.TransportStdio,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateMCPServer(tt.server)
			assertWantErr(t, err, tt.wantErr)
		})
	}
}

func TestValidateRAGSource(t *testing.T) {
	tests := []struct {
		name    string
		rs      *aiv1alpha1.RAGSource
		wantErr string
	}{
		{
			name: "valid git source",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeGit,
						Git: &aiv1alpha1.GitSource{
							URL:   "https://github.com/example/repo",
							Paths: []string{"docs", "content/en"},
						},
					},
				},
			},
		},
		{
			name: "git type without git block",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeGit,
					},
				},
			},
			wantErr: "source.type is git but source.git is not set",
		},
		{
			name: "s3 type without s3 block",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeS3,
					},
				},
			},
			wantErr: "source.type is s3 but source.s3 is not set",
		},
		{
			name: "url type without url block",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeURL,
					},
				},
			},
			wantErr: "source.type is url but source.url is not set",
		},
		{
			name: "glob star in git paths",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeGit,
						Git: &aiv1alpha1.GitSource{
							URL:   "https://github.com/example/repo",
							Paths: []string{"docs/**/*.md"},
						},
					},
				},
			},
			wantErr: "glob patterns are not supported",
		},
		{
			name: "glob question mark in git paths",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeGit,
						Git: &aiv1alpha1.GitSource{
							URL:   "https://github.com/example/repo",
							Paths: []string{"docs/file?.md"},
						},
					},
				},
			},
			wantErr: "glob patterns are not supported",
		},
		{
			name: "glob bracket in git paths",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeGit,
						Git: &aiv1alpha1.GitSource{
							URL:   "https://github.com/example/repo",
							Paths: []string{"docs/[a-z]"},
						},
					},
				},
			},
			wantErr: "glob patterns are not supported",
		},
		{
			name: "glob brace in git paths",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeGit,
						Git: &aiv1alpha1.GitSource{
							URL:   "https://github.com/example/repo",
							Paths: []string{"docs/{a,b}"},
						},
					},
				},
			},
			wantErr: "glob patterns are not supported",
		},
		{
			name: "second path invalid",
			rs: &aiv1alpha1.RAGSource{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.RAGSourceSpec{
					Source: aiv1alpha1.SourceConfig{
						Type: aiv1alpha1.RAGSourceTypeGit,
						Git: &aiv1alpha1.GitSource{
							URL:   "https://github.com/example/repo",
							Paths: []string{"docs", "content/*.md"},
						},
					},
				},
			},
			wantErr: "source.git.paths[1]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateRAGSource(tt.rs)
			assertWantErr(t, err, tt.wantErr)
		})
	}
}

func TestValidateCrewFitness(t *testing.T) {
	tests := []struct {
		name    string
		cf      *aiv1alpha1.CrewFitness
		wantErr string
	}{
		{
			name: "valid",
			cf: &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:      "hello-world",
					TestRef:      "discussion-health",
					ConfigMapRef: "hello-world-fitness-tests",
				},
			},
		},
		{
			name: "empty crewRef",
			cf: &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.CrewFitnessSpec{
					TestRef:      "discussion-health",
					ConfigMapRef: "hello-world-fitness-tests",
				},
			},
			wantErr: "spec.crewRef is required",
		},
		{
			name: "empty testRef",
			cf: &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:      "hello-world",
					ConfigMapRef: "hello-world-fitness-tests",
				},
			},
			wantErr: "spec.testRef is required",
		},
		{
			name: "empty configMapRef and testContent",
			cf: &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef: "hello-world",
					TestRef: "discussion-health",
				},
			},
			wantErr: "either spec.configMapRef or spec.testContent is required",
		},
		{
			name: "valid with testContent",
			cf: &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:     "hello-world",
					TestRef:     "discussion-health",
					TestContent: "DESCRIPTION: test\nASSERT response_received",
				},
			},
		},
		{
			name: "both configMapRef and testContent",
			cf: &aiv1alpha1.CrewFitness{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: aiv1alpha1.CrewFitnessSpec{
					CrewRef:      "hello-world",
					TestRef:      "discussion-health",
					ConfigMapRef: "hello-world-fitness-tests",
					TestContent:  "DESCRIPTION: test",
				},
			},
			wantErr: "spec.configMapRef and spec.testContent are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateCrewFitness(tt.cf)
			assertWantErr(t, err, tt.wantErr)
		})
	}
}

func TestValidateCrew(t *testing.T) {
	tests := []struct {
		name        string
		crew        *aiv1alpha1.Crew
		wantErr     string
		wantWarning string
	}{
		{
			name: "valid crew",
			crew: &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "hello-world"},
				Spec:       aiv1alpha1.CrewSpec{},
			},
		},
		{
			name: "valid with discussion enabled warns",
			crew: &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "hello-world"},
				Spec: aiv1alpha1.CrewSpec{
					Discussion: &aiv1alpha1.DiscussionConfig{Enabled: true},
				},
			},
			wantWarning: "discussion is enabled",
		},
		{
			name: "discussion disabled no warning",
			crew: &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "hello-world"},
				Spec: aiv1alpha1.CrewSpec{
					Discussion: &aiv1alpha1.DiscussionConfig{Enabled: false},
				},
			},
		},
		{
			name: "name too long",
			crew: &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "this-crew-name-is-way-too-long-and-exceeds-the-fifty-eight-char"},
				Spec:       aiv1alpha1.CrewSpec{},
			},
			wantErr: "exceeds 58 characters",
		},
		{
			name: "name with uppercase",
			crew: &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "Hello-World"},
				Spec:       aiv1alpha1.CrewSpec{},
			},
			wantErr: "not DNS-1123 compliant",
		},
		{
			name: "name with underscores",
			crew: &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "hello_world"},
				Spec:       aiv1alpha1.CrewSpec{},
			},
			wantErr: "not DNS-1123 compliant",
		},
		{
			name: "name starting with hyphen",
			crew: &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "-hello"},
				Spec:       aiv1alpha1.CrewSpec{},
			},
			wantErr: "not DNS-1123 compliant",
		},
		{
			name: "single character name",
			crew: &aiv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: "a"},
				Spec:       aiv1alpha1.CrewSpec{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := validateCrew(tt.crew)
			assertWantErr(t, err, tt.wantErr)
			assertWantWarning(t, warnings, tt.wantWarning)
		})
	}
}

// assertWantErr checks an error against an expected substring. An empty want
// means no error is expected.
func assertWantErr(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Errorf("expected error containing %q, got nil", want)
	} else if !contains(err.Error(), want) {
		t.Errorf("expected error containing %q, got %q", want, err.Error())
	}
}

// assertWantWarning checks that warnings contain the expected substring. An
// empty want means no warning is asserted.
func assertWantWarning(t *testing.T, warnings []string, want string) {
	t.Helper()
	if want == "" {
		return
	}
	if len(warnings) == 0 {
		t.Errorf("expected warning containing %q, got none", want)
		return
	}
	for _, w := range warnings {
		if contains(w, want) {
			return
		}
	}
	t.Errorf("expected warning containing %q, got %v", want, warnings)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
