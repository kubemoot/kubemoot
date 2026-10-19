/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"encoding/json"
	"sort"
	"strconv"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// Env vars carrying each phase's ranked candidate models to the agent runtime.
// The runtime's per-call pick may use a candidate that is warm with room instead
// of loading the bound model (KUBEMOOT_MODEL_MODEL stays the preferred model).
const (
	envModelCandidatesMulling = "KUBEMOOT_MODEL_CANDIDATES_MULLING"
	envModelCandidatesTriage  = "KUBEMOOT_MODEL_CANDIDATES_TRIAGE"

	// The rule's preferFaster per phase: how many points the runtime adds to a
	// candidate in proportion to its measured speed.
	envModelPreferFasterMulling = "KUBEMOOT_MODEL_PREFER_FASTER_MULLING"
	envModelPreferFasterTriage  = "KUBEMOOT_MODEL_PREFER_FASTER_TRIAGE"
)

// preferFasterOf is the rule's preferFaster, 0 when the rule is nil or sets none.
func preferFasterOf(rule *kubemootv1alpha1.SchedulingRule) int32 {
	if rule == nil {
		return 0
	}
	return rule.PreferFaster
}

// modelCandidate is one entry of a phase's candidate list: the model identifier
// on the provider and its quality score under the crew's scheduling policy.
type modelCandidate struct {
	Model string `json:"model"`
	Score int64  `json:"score"`
}

// phaseQualityBias returns the agent's effective quality bias for a phase rule.
// It applies only when the rule has no explicit Prefer block (explicit Prefer
// always wins) and the policy declares a QualityBias map; ok=false otherwise.
func phaseQualityBias(agent *kubemootv1alpha1.Agent, policy *kubemootv1alpha1.CrewSchedulingPolicy, rule *kubemootv1alpha1.SchedulingRule) (float64, bool) {
	if rule == nil || len(rule.Prefer) > 0 || policy == nil || len(policy.Spec.QualityBias) == 0 {
		return 0, false
	}
	return effectiveQualityBias(agent.Spec.Capabilities, policy.Spec.QualityBias)
}

// modelQualityScore is the policy-driven, provider-independent part of a
// candidate's score: the matching Prefer weights plus the qualityBias (model size)
// and ability contributions. Locality, provider weight, and load terms are left
// out so the score compares models, not placements.
func modelQualityScore(rule *kubemootv1alpha1.SchedulingRule, m *kubemootv1alpha1.Model, sc phaseScoring) int64 {
	score, _ := policyScore(rule, m, sc)
	return score
}

// policyScore is the one composition of the policy-driven score terms, with a
// reason token per term: the matching Prefer weights, the qualityBias (model size)
// contribution, and the ability points. The pick (scoreCandidate) and the candidate
// list (modelQualityScore) both read it, so they cannot drift apart.
func policyScore(rule *kubemootv1alpha1.SchedulingRule, m *kubemootv1alpha1.Model, sc phaseScoring) (int64, []string) {
	score, reasons := preferRuleScore(rule, m)
	if sc.HaveBias {
		if biasScore, biasReason := qualityBiasScore(sc.Bias, m.Labels); biasReason != "" {
			score += biasScore
			reasons = append(reasons, biasReason)
		}
	}
	if abilityScore, abilityReason := sc.abilityScore(m.Labels); abilityReason != "" {
		score += abilityScore
		reasons = append(reasons, abilityReason)
	}
	return score, reasons
}

// matchesRequire reports whether the Model satisfies the rule's hard constraints
// (Require and RequireExpressions). A nil rule admits every Model; a malformed
// constraint admits none (pickModel reports that error on the feasibility pass).
func matchesRequire(rule *kubemootv1alpha1.SchedulingRule, m *kubemootv1alpha1.Model) bool {
	ok, err := requireMatches(rule, m.Labels)
	return err == nil && ok
}

// rankedCandidates lists the models an agent may run for a phase: every Ready
// Model the rule's Require selector admits, one entry per model identifier
// (keeping its best quality score). The bound model comes first; the rest follow
// by quality score, highest first, then by name. Provider readiness and VRAM are
// left to the runtime's per-call pick, which reads live provider state, so the
// list (and the Deployment env) stays stable while providers come and go.
func rankedCandidates(bound string, rule *kubemootv1alpha1.SchedulingRule, models []kubemootv1alpha1.Model, sc phaseScoring) []modelCandidate {
	best := map[string]int64{}
	for i := range models {
		m := &models[i]
		if !m.Status.Ready || !matchesRequire(rule, m) {
			continue
		}
		score := modelQualityScore(rule, m, sc)
		if prev, seen := best[m.Spec.Model]; !seen || score > prev {
			best[m.Spec.Model] = score
		}
	}
	out := make([]modelCandidate, 0, len(best))
	for model, score := range best {
		out = append(out, modelCandidate{Model: model, Score: score})
	}
	sort.Slice(out, func(i, j int) bool { return candidateLess(out[i], out[j], bound) })
	return out
}

// candidateLess orders the bound model first, then higher scores, then names.
func candidateLess(a, b modelCandidate, bound string) bool {
	if (a.Model == bound) != (b.Model == bound) {
		return a.Model == bound
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Model < b.Model
}

// candidateEnvVars returns the per-phase candidate list and preferFaster env vars; a
// phase with no candidates, or no preferFaster, contributes none.
func candidateEnvVars(mulling, triage *modelPick) []corev1.EnvVar {
	var env []corev1.EnvVar
	env = appendPhaseEnv(env, mulling, envModelCandidatesMulling, envModelPreferFasterMulling)
	return appendPhaseEnv(env, triage, envModelCandidatesTriage, envModelPreferFasterTriage)
}

// appendPhaseEnv adds a phase's candidate list and preferFaster to env.
func appendPhaseEnv(env []corev1.EnvVar, pick *modelPick, candidatesName, preferFasterName string) []corev1.EnvVar {
	if v := candidatesJSON(pick); v != "" {
		env = append(env, corev1.EnvVar{Name: candidatesName, Value: v})
	}
	if pick != nil && pick.PreferFaster > 0 {
		env = append(env, corev1.EnvVar{Name: preferFasterName, Value: strconv.Itoa(int(pick.PreferFaster))})
	}
	return env
}

// candidatesJSON renders a pick's candidates as a JSON list, or "" when there are none.
func candidatesJSON(pick *modelPick) string {
	if pick == nil || len(pick.Candidates) == 0 {
		return ""
	}
	b, err := json.Marshal(pick.Candidates)
	if err != nil {
		return ""
	}
	return string(b)
}
