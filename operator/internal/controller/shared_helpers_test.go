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
	"fmt"
	"strings"
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// baseTestDeployment creates a minimal deployment for testing determineDeploymentPhase.
func baseTestDeployment(readyReplicas, totalReplicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: readyReplicas,
			Replicas:      totalReplicas,
		},
	}
}

func TestHelperMatchVersionConstraintShared(t *testing.T) {
	tests := []struct {
		constraint string
		version    string
		expected   bool
	}{
		// Exact match
		{"1.2.3", "1.2.3", true},
		{"1.2.3", "1.2.4", false},
		{"1.2.3", "v1.2.3", true},

		// Caret (^) — compatible with major
		{"^1.2.0", "1.3.0", true},
		{"^1.2.0", "1.2.0", true},
		{"^1.2.0", "1.1.0", false},
		{"^1.2.0", "2.0.0", false},

		// Tilde (~) — compatible with minor
		{"~1.2.0", "1.2.5", true},
		{"~1.2.0", "1.3.0", false},
		{"~1.2.0", "1.2.0", true},

		// Wildcard
		{"1.x", "1.0.0", true},
		{"1.x", "1.99.0", true},
		{"1.x", "2.0.0", false},
		{"2.*", "2.5.0", true},
		{"2.*", "3.0.0", false},

		// Comparison operators
		{">=1.2.0", "1.2.0", true},
		{">=1.2.0", "1.3.0", true},
		{">=1.2.0", "1.1.0", false},
		{">1.2.0", "1.2.1", true},
		{">1.2.0", "1.2.0", false},
		{"<=1.2.0", "1.2.0", true},
		{"<=1.2.0", "1.1.9", true},
		{"<=1.2.0", "1.2.1", false},
		{"<2.0.0", "1.9.9", true},
		{"<2.0.0", "2.0.0", false},

		// Edge cases
		{"1.0.0", "invalid", false},
		{"invalid", "1.0.0", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s", tt.constraint, tt.version), func(t *testing.T) {
			if got := matchVersionConstraintShared(tt.constraint, tt.version); got != tt.expected {
				t.Errorf("matchVersionConstraintShared(%q, %q) = %v, want %v",
					tt.constraint, tt.version, got, tt.expected)
			}
		})
	}
}

// assertParseVersion verifies a single parseVersion case, isolating the
// nil/value branching so the table loop stays flat.
func assertParseVersion(t *testing.T, result, expected []int, isNil bool) {
	t.Helper()
	if isNil {
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
		return
	}
	if result == nil {
		t.Fatal("unexpected nil")
	}
	for i := 0; i < 3; i++ {
		if result[i] != expected[i] {
			t.Errorf("index %d: got %d, want %d", i, result[i], expected[i])
		}
	}
}

func TestHelperParseVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected []int
		isNil    bool
	}{
		{"1.2.3", []int{1, 2, 3}, false},
		{"v1.0.0", []int{1, 0, 0}, false},
		{"1.0.0-beta", []int{1, 0, 0}, false},
		{"1", []int{1, 0, 0}, false},
		{"1.2", []int{1, 2, 0}, false},
		{"invalid", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assertParseVersion(t, parseVersion(tt.input), tt.expected, tt.isNil)
		})
	}
}

func TestHelperCompareVersions(t *testing.T) {
	tests := []struct {
		a, b     []int
		expected int
	}{
		{[]int{1, 0, 0}, []int{1, 0, 0}, 0},
		{[]int{2, 0, 0}, []int{1, 0, 0}, 1},
		{[]int{1, 0, 0}, []int{2, 0, 0}, -1},
		{[]int{1, 2, 0}, []int{1, 1, 0}, 1},
		{[]int{1, 1, 3}, []int{1, 1, 2}, 1},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%v_%v", tt.a, tt.b), func(t *testing.T) {
			if got := compareVersions(tt.a, tt.b); got != tt.expected {
				t.Errorf("got %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestHelperSanitizeK8sName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with/slash", "with-slash"},
		{"with_underscore", "with-underscore"},
		{"with spaces", "with-spaces"},
		{"with.dots", "with-dots"},
		{"UPPERCASE", "uppercase"},
		{"123-starts-with-number", "mcp-123-starts-with-number"},
		{"", "unnamed-server"},
		{"---", "unnamed-server"},
		{strings.Repeat("a", 100), strings.Repeat("a", 63)},
		{"hello@world!", "helloworld"},
		{"-leading-dash", "leading-dash"},
		{"trailing-dash-", "trailing-dash"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := sanitizeK8sName(tt.input); got != tt.expected {
				t.Errorf("sanitizeK8sName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestHelperGetModuleName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"mcp-server-fetch", "mcp_server_fetch"},
		{"package>=1.0.0", "package"},
		{"simple", "simple"},
		{"a-b>=2.0", "a_b"},
		{"package==1.0.0", "package"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := getModuleName(tt.input); got != tt.expected {
				t.Errorf("getModuleName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestHelperComputeTrialStats(t *testing.T) {
	t.Run("empty trials", func(t *testing.T) {
		status := &kubemootv1alpha1.MCPServerReportStatus{}
		computeTrialStats(status)
		if status.SuccessRate != "N/A" {
			t.Errorf("expected N/A, got %s", status.SuccessRate)
		}
	})

	t.Run("mixed trials", func(t *testing.T) {
		now := metav1.Now()
		status := &kubemootv1alpha1.MCPServerReportStatus{
			Trials: []kubemootv1alpha1.TrialRecord{
				{Success: true, TestedAt: &now, Version: "1.0", Transport: "http"},
				{Success: false, TestedAt: &now},
				{Success: true, TestedAt: &now, Version: "1.1", Transport: "sse"},
			},
		}
		computeTrialStats(status)
		assertMixedTrialStats(t, status)
	})
}

// assertMixedTrialStats verifies the computed stats for the mixed-trials case,
// isolating the flat chain of independent assertions from the test body.
func assertMixedTrialStats(t *testing.T, status *kubemootv1alpha1.MCPServerReportStatus) {
	t.Helper()
	if status.SuccessCount != 2 {
		t.Errorf("expected 2 successes, got %d", status.SuccessCount)
	}
	if status.FailureCount != 1 {
		t.Errorf("expected 1 failure, got %d", status.FailureCount)
	}
	if status.SuccessRate != "67%" {
		t.Errorf("expected 67%%, got %s", status.SuccessRate)
	}
	if status.RecommendedVersion != "1.1" {
		t.Errorf("expected recommended version 1.1, got %s", status.RecommendedVersion)
	}
	if status.RecommendedTransport != "sse" {
		t.Errorf("expected recommended transport sse, got %s", status.RecommendedTransport)
	}
	if status.LastTested == nil {
		t.Error("expected LastTested to be set")
	}
	if status.LastSuccessful == nil {
		t.Error("expected LastSuccessful to be set")
	}
}

func TestHelperMatchGlob(t *testing.T) {
	tests := []struct {
		pattern, value string
		expected       bool
	}{
		{"*.json", "test.json", true},
		{"*.json", "test.yaml", false},
		{"hello*", "helloworld", true},
		{"he?lo", "hello", true},
		{"exact", "exact", true},
		{"exact", "other", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s", tt.pattern, tt.value), func(t *testing.T) {
			got, err := matchGlob(tt.pattern, tt.value)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.expected)
			}
		})
	}
}

func TestHelperParseServiceFromEndpoint(t *testing.T) {
	tests := []struct {
		endpoint, defaultNS string
		wantSvc, wantNS     string
	}{
		{"http://ollama.ollama-rig0:11434", "default", "ollama", "ollama-rig0"},
		{"http://ollama.ollama.svc.cluster.local:11434", "default", "ollama", "ollama"},
		{"http://localhost:11434", "myns", "localhost", "myns"},
		{"://bad", "default", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			svc, ns := parseServiceFromEndpoint(tt.endpoint, tt.defaultNS)
			if svc != tt.wantSvc || ns != tt.wantNS {
				t.Errorf("got (%q, %q), want (%q, %q)", svc, ns, tt.wantSvc, tt.wantNS)
			}
		})
	}
}

func TestHelperDetermineDeploymentPhase(t *testing.T) {
	t.Run("all ready", func(t *testing.T) {
		// Use appsv1.DeploymentStatus directly to avoid import
		phase, ready, _ := determineDeploymentPhase(baseTestDeployment(2, 2))
		if phase != "Ready" || !ready {
			t.Errorf("expected Ready/true, got %s/%v", phase, ready)
		}
	})

	t.Run("degraded", func(t *testing.T) {
		phase, ready, _ := determineDeploymentPhase(baseTestDeployment(1, 2))
		if phase != "Degraded" || ready {
			t.Errorf("expected Degraded/false, got %s/%v", phase, ready)
		}
	})

	t.Run("deploying", func(t *testing.T) {
		phase, ready, _ := determineDeploymentPhase(baseTestDeployment(0, 2))
		if phase != "Deploying" || ready {
			t.Errorf("expected Deploying/false, got %s/%v", phase, ready)
		}
	})
}

func TestHelperParseAgentDecision(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		d, err := parseAgentDecision(`{"action":"allow","confidence":0.9,"reason":"good"}`)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != "allow" || d.Confidence != 0.9 {
			t.Errorf("unexpected decision: %v", d)
		}
	})

	t.Run("JSON in prose", func(t *testing.T) {
		d, err := parseAgentDecision(`Here is my evaluation: {"action":"deny","confidence":0.8,"reason":"risky"} done.`)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != "deny" {
			t.Errorf("expected deny, got %s", d.Action)
		}
	})

	t.Run("invalid action", func(t *testing.T) {
		_, err := parseAgentDecision(`{"action":"maybe","confidence":0.5}`)
		if err == nil {
			t.Error("expected error for invalid action")
		}
	})

	t.Run("no JSON", func(t *testing.T) {
		_, err := parseAgentDecision("this is not json at all")
		if err == nil {
			t.Error("expected error for non-JSON")
		}
	})
}

func TestHelperContainsIgnoreCase(t *testing.T) {
	tests := []struct {
		str, substr string
		expected    bool
	}{
		{"Hello World", "hello", true},
		{"Hello World", "WORLD", true},
		{"Hello World", "missing", false},
		{"", "test", false},
		{"test", "", true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s", tt.str, tt.substr), func(t *testing.T) {
			if got := containsIgnoreCase(tt.str, tt.substr); got != tt.expected {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}
