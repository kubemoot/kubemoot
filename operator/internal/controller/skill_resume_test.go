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
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestSkillResume_EmptyPoolNoOp is the critical regression guard: when no
// Skills are defined, CombinedResumeHash must return the SAME value as
// HashResumes so existing crews see no hash change and no re-embed is triggered.
func TestSkillResume_EmptyPoolNoOp(t *testing.T) {
	agents := []AgentResume{
		{Name: "alpha", Description: "Alpha agent", Role: "tooler", Keywords: []string{"k8s", "pods"}},
		{Name: "beta", Description: "Beta agent", Role: "researcher", Tools: []string{"kubectl_get"}},
	}

	agentOnlyHash := HashResumes(agents)

	// nil skills
	if got := CombinedResumeHash(agents, nil); got != agentOnlyHash {
		t.Errorf("CombinedResumeHash(agents, nil) = %s, want %s (must be byte-identical to HashResumes)", got, agentOnlyHash)
	}
	// empty slice
	if got := CombinedResumeHash(agents, []SkillResume{}); got != agentOnlyHash {
		t.Errorf("CombinedResumeHash(agents, []) = %s, want %s (must be byte-identical to HashResumes)", got, agentOnlyHash)
	}
}

// TestSkillResume_ChangeDetection verifies that adding a skill changes the
// combined hash so the resume RAGSource is re-embedded.
func TestSkillResume_ChangeDetection(t *testing.T) {
	agents := []AgentResume{
		{Name: "alpha", Description: "Alpha agent", Role: "tooler"},
	}
	skills := []SkillResume{
		{Name: "gpu-basics", Description: "GPU diagnostics", Kind: "skill", Order: 100},
	}

	withoutSkills := CombinedResumeHash(agents, nil)
	withSkills := CombinedResumeHash(agents, skills)

	if withoutSkills == withSkills {
		t.Error("CombinedResumeHash should differ when a skill is added")
	}

	// Adding a second skill should also change the hash
	skills2 := append(skills, SkillResume{Name: "net-diag", Description: "Network check", Kind: "skill", Order: 200})
	withTwoSkills := CombinedResumeHash(agents, skills2)
	if withSkills == withTwoSkills {
		t.Error("CombinedResumeHash should differ when a second skill is added")
	}
}

// TestBuildSkillResumeText verifies the human-readable embedding format.
func TestBuildSkillResumeText(t *testing.T) {
	s := SkillResume{
		Name:        "gpu-basics",
		Description: "GPU diagnostics and troubleshooting",
		Kind:        "skill",
		Order:       100,
	}

	text := BuildSkillResumeText(s)

	if !strings.Contains(text, "Skill: gpu-basics") {
		t.Errorf("missing skill name in text: %q", text)
	}
	if !strings.Contains(text, "Description: GPU diagnostics and troubleshooting") {
		t.Errorf("missing description in text: %q", text)
	}
}

// TestBuildSkillResumeText_Empty verifies that an empty description is omitted.
func TestBuildSkillResumeText_Empty(t *testing.T) {
	s := SkillResume{Name: "bare", Kind: "skill"}
	text := BuildSkillResumeText(s)
	if !strings.Contains(text, "Skill: bare") {
		t.Errorf("missing skill name: %q", text)
	}
	if strings.Contains(text, "Description:") {
		t.Errorf("empty description should not appear in text: %q", text)
	}
}

// TestMarshalResumePayload_NoSkillsIdentity asserts that marshalResumePayload
// with no skills produces bytes identical to json.Marshal(agents), preserving
// the wire-format contract for crews that have no Skills defined.
func TestMarshalResumePayload_NoSkillsIdentity(t *testing.T) {
	agents := []AgentResume{
		{Name: "alpha", Description: "Alpha agent", Role: "tooler", Keywords: []string{"k8s"}},
		{Name: "beta", Description: "Beta agent", Role: "researcher", Tools: []string{"kubectl_get"}},
	}
	want, err := json.Marshal(agents)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	gotNil, err := marshalResumePayload(agents, nil)
	if err != nil {
		t.Fatalf("marshalResumePayload(agents, nil): %v", err)
	}
	if !bytes.Equal(gotNil, want) {
		t.Errorf("marshalResumePayload(agents, nil) not identical to json.Marshal(agents):\ngot  %s\nwant %s", gotNil, want)
	}

	gotEmpty, err := marshalResumePayload(agents, []SkillResume{})
	if err != nil {
		t.Fatalf("marshalResumePayload(agents, []SkillResume{}): %v", err)
	}
	if !bytes.Equal(gotEmpty, want) {
		t.Errorf("marshalResumePayload(agents, []) not identical to json.Marshal(agents):\ngot  %s\nwant %s", gotEmpty, want)
	}
}

// TestListCrewSkills verifies that listCrewSkills returns sorted SkillResume
// entries and that skills from other crews are excluded.
func TestListCrewSkills(t *testing.T) {
	scheme := skillScheme(t) // reuse helper from skill_controller_test.go

	skill1 := mkSkill("crew-x", "net-diag", "homelab-pilot", 200, "Network check", "WHEN network")
	skill2 := mkSkill("crew-x", "gpu-basics", "homelab-pilot", 100, "GPU check", "WHEN gpu")
	otherCrew := mkSkill("crew-x", "other-skill", "other-crew", 50, "Other", "WHEN other")
	coord := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "homelab-coordinator",
			Namespace: "crew-x",
			Labels:    map[string]string{crewLabelKey: "homelab-pilot"},
		},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: "coordinator"},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skill1, skill2, otherCrew, coord).Build()
	rec := &AgentReconciler{Client: cli}

	skills := rec.listCrewSkills(context.Background(), coord, "homelab-pilot")

	if len(skills) != 2 {
		t.Fatalf("expected 2 skills (other-crew excluded), got %d: %+v", len(skills), skills)
	}
	// gpu-basics has order=100 so it comes before net-diag (order=200)
	if skills[0].Name != "gpu-basics" || skills[1].Name != "net-diag" {
		t.Errorf("skills not sorted by order: %q, %q", skills[0].Name, skills[1].Name)
	}
	if skills[0].Kind != "skill" {
		t.Errorf("expected Kind=skill, got %q", skills[0].Kind)
	}
}
