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
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
)

// AgentResume holds the resume data for a single agent in the crew.
// Exported so it can be serialized to JSON for NATS KV storage.
type AgentResume struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Channels    []string `json:"channels,omitempty"`
	Role        string   `json:"role"`
	Summary     string   `json:"triageSummary,omitempty"`
	// Prompt is the agent's full assembled system prompt included for EMBEDDING
	// only (the indexer folds it into the resume text for sharper top-K ranking).
	// It rides the NATS-KV view, not the triage path: the resume query returns
	// agent NAMES only, so the prompt never reaches a triage LLM call
	// (embed-rich, triage-lean).
	Prompt string `json:"prompt,omitempty"`
}

// writeAgentHashSection writes the deterministic hash contribution for each
// AgentResume into h. The format is identical in HashResumes and
// CombinedResumeHash so the agent portion of the combined hash is stable.
func writeAgentHashSection(h io.Writer, agents []AgentResume) {
	for _, r := range agents {
		// Writes to a hash never fail.
		_, _ = fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%s|%s\n",
			r.Name, r.Description, r.Role, r.Summary,
			strings.Join(r.Keywords, ","),
			strings.Join(r.Tools, ","),
			strings.Join(r.Channels, ","),
			r.Prompt,
		)
	}
}

// HashResumes computes a deterministic SHA-256 hash over a slice of AgentResume.
// The resumes must already be sorted by Name for deterministic output. Prompt is
// included so a prompt edit re-embeds the resume.
func HashResumes(resumes []AgentResume) string {
	h := sha256.New()
	writeAgentHashSection(h, resumes)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// BuildResumeText produces a human-readable text representation of an AgentResume,
// suitable for embedding as a single document in a vector store.
func BuildResumeText(r AgentResume) string {
	var sb strings.Builder
	sb.WriteString("Agent: ")
	sb.WriteString(r.Name)
	sb.WriteString("\nRole: ")
	sb.WriteString(r.Role)
	if r.Description != "" {
		sb.WriteString("\nDescription: ")
		sb.WriteString(r.Description)
	}
	if r.Summary != "" {
		sb.WriteString("\nSummary: ")
		sb.WriteString(r.Summary)
	}
	if len(r.Keywords) > 0 {
		sb.WriteString("\nKeywords: ")
		sb.WriteString(strings.Join(r.Keywords, ", "))
	}
	if len(r.Tools) > 0 {
		sb.WriteString("\nTools: ")
		sb.WriteString(strings.Join(r.Tools, ", "))
	}
	if len(r.Channels) > 0 {
		sb.WriteString("\nChannels: ")
		sb.WriteString(strings.Join(r.Channels, ", "))
	}
	return sb.String()
}

// SkillResume holds the resume data for a single Skill in the crew.
// Kind is always "skill" and acts as the discriminator in the shared resume
// pool (agents use "role", not "kind", so there is no field collision).
type SkillResume struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        string `json:"kind"` // always "skill"
	Order       int32  `json:"order,omitempty"`
}

// BuildSkillResumeText produces a human-readable text representation of a
// SkillResume, suitable for embedding as a single document in a vector store.
func BuildSkillResumeText(s SkillResume) string {
	var sb strings.Builder
	sb.WriteString("Skill: ")
	sb.WriteString(s.Name)
	if s.Description != "" {
		sb.WriteString("\nDescription: ")
		sb.WriteString(s.Description)
	}
	return sb.String()
}

// CombinedResumeHash computes a deterministic hash that covers both agent
// resumes and skill resumes. When skills is empty (nil or zero-length) the
// result is IDENTICAL to HashResumes(agents) so existing crews with no Skills
// defined see no hash change and no re-embed is triggered.
func CombinedResumeHash(agents []AgentResume, skills []SkillResume) string {
	if len(skills) == 0 {
		return HashResumes(agents)
	}
	h := sha256.New()
	writeAgentHashSection(h, agents)
	// Skill section - appended after all agents so the separator is implicit.
	for _, s := range skills {
		// Writes to a hash never fail.
		_, _ = fmt.Fprintf(h, "skill|%s|%s|%d\n", s.Name, s.Description, s.Order)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
