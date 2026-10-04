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
	"strings"
	"testing"
)

func TestHashResumes_Deterministic(t *testing.T) {
	resumes := []AgentResume{
		{Name: testAlpha, Description: testAlphaDescription, Role: testRoleTooler, Keywords: []string{testK8s, testPods}},
		{Name: testBeta, Description: testBetaDescription, Role: testResearcher, Tools: []string{testToolKubectlGet}},
	}

	h1 := HashResumes(resumes)
	h2 := HashResumes(resumes)

	if h1 != h2 {
		t.Errorf("HashResumes not deterministic: %s != %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("expected SHA-256 hex (64 chars), got %d chars", len(h1))
	}
}

func TestHashResumes_ChangeDetection(t *testing.T) {
	resumes1 := []AgentResume{
		{Name: testAlpha, Description: testAlphaDescription, Role: testRoleTooler},
	}
	resumes2 := []AgentResume{
		{Name: testAlpha, Description: "Alpha agent updated", Role: testRoleTooler},
	}

	h1 := HashResumes(resumes1)
	h2 := HashResumes(resumes2)

	if h1 == h2 {
		t.Error("HashResumes should differ when description changes")
	}
}

func TestHashResumes_PromptChangeDetection(t *testing.T) {
	resumes1 := []AgentResume{
		{Name: testAlpha, Description: testAlphaDescription, Role: testRoleTooler, Prompt: "WHEN asked about nodes THEN list them"},
	}
	resumes2 := []AgentResume{
		{Name: testAlpha, Description: testAlphaDescription, Role: testRoleTooler, Prompt: "WHEN asked about nodes THEN list them with capacity"},
	}
	if HashResumes(resumes1) == HashResumes(resumes2) {
		t.Error("HashResumes should differ when the system prompt changes (so the resume re-embeds)")
	}
}

func TestHashResumes_Empty(t *testing.T) {
	h := HashResumes(nil)
	if h == "" {
		t.Error("HashResumes should return a valid hash even for nil input")
	}
	// Empty SHA-256 is the hash of no data
	h2 := HashResumes([]AgentResume{})
	if h != h2 {
		t.Error("nil and empty slice should produce the same hash")
	}
}

func TestBuildResumeText_Full(t *testing.T) {
	r := AgentResume{
		Name:        "k8s-agent",
		Description: "Kubernetes expert",
		Role:        testRoleTooler,
		Summary:     "Handles pod and service queries",
		Keywords:    []string{"kubernetes", testPods, "services"},
		Tools:       []string{testToolKubectlGet, "kubectl_describe"},
		Channels:    []string{"infrastructure"},
	}

	text := BuildResumeText(r)

	expected := []string{
		"Agent: k8s-agent",
		"Role: tooler",
		"Description: Kubernetes expert",
		"Summary: Handles pod and service queries",
		"Keywords: kubernetes, pods, services",
		"Tools: kubectl_get, kubectl_describe",
		"Channels: infrastructure",
	}
	for _, s := range expected {
		if !strings.Contains(text, s) {
			t.Errorf("BuildResumeText missing %q in output:\n%s", s, text)
		}
	}
}

func TestBuildResumeText_Minimal(t *testing.T) {
	r := AgentResume{
		Name: testSimple,
		Role: testResearcher,
	}

	text := BuildResumeText(r)

	if !strings.Contains(text, "Agent: simple") {
		t.Error("missing agent name")
	}
	if !strings.Contains(text, "Role: researcher") {
		t.Error("missing role")
	}
	// Optional fields should not appear
	if strings.Contains(text, "Description:") {
		t.Error("empty description should not appear")
	}
	if strings.Contains(text, "Keywords:") {
		t.Error("empty keywords should not appear")
	}
}
