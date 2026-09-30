/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	candProvider  = "ollama"
	candLow       = "low"
	candHigh      = "high"
	candMedium    = "medium"
	candModel32   = "qwen3:32b"
	candModel14   = "qwen3:14b"
	candFamily    = "family"
	candTierDev   = "dev"
	candTierProd  = "prod"
	candQwen      = "qwen3"
	candTier      = "tier"
	candReasoning = "reasoning"
	candBias      = "0.8"
	candCrew      = "crew-a"
	candAgent     = "k8s"
)

func candModel(name, modelID string, ready bool, lbls map[string]string) kubemootv1alpha1.Model {
	return kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: lbls},
		Spec:       kubemootv1alpha1.ModelSpec{Model: modelID, ProviderRef: candProvider},
		Status:     kubemootv1alpha1.ModelStatus{Ready: ready},
	}
}

func TestRankedCandidates_BoundFirstThenQualityThenName(t *testing.T) {
	models := []kubemootv1alpha1.Model{
		candModel(candLow, testModelID, true, map[string]string{latencyClassLabel: candLow}),
		candModel(candHigh, candModel32, true, map[string]string{latencyClassLabel: candHigh}),
		candModel(candMedium, candModel14, true, map[string]string{latencyClassLabel: candMedium}),
		candModel("cold", "llama3:70b", false, map[string]string{latencyClassLabel: candHigh}),
	}
	rule := &kubemootv1alpha1.SchedulingRule{Phase: phaseMulling}

	// bias 0.7: high=70, medium=60, low=30. The bound model leads even when it is not the best.
	got := rankedCandidates(testModelID, rule, models, 0.7, true)
	want := []modelCandidate{
		{Model: testModelID, Score: 30},
		{Model: candModel32, Score: 70},
		{Model: candModel14, Score: 60},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rankedCandidates = %+v, want %+v (not-Ready models are excluded)", got, want)
	}
}

func TestRankedCandidates_RequireFiltersAndDuplicatesKeepBestScore(t *testing.T) {
	models := []kubemootv1alpha1.Model{
		candModel("a-on-rig0", candModel32, true, map[string]string{candTier: candTierProd, candFamily: candQwen}),
		candModel("a-on-rig1", candModel32, true, map[string]string{candTier: candTierProd}),
		candModel("b", candModel14, true, map[string]string{candTier: candTierProd}),
		candModel(candTierDev, "llama3:8b", true, map[string]string{candTier: candTierDev}),
	}
	rule := &kubemootv1alpha1.SchedulingRule{
		Phase:   phaseMulling,
		Require: &metav1.LabelSelector{MatchLabels: map[string]string{candTier: candTierProd}},
		Prefer: []kubemootv1alpha1.PreferenceTerm{{
			Weight:   100,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{candFamily: candQwen}},
		}},
	}
	got := rankedCandidates(candModel32, rule, models, 0, false)
	want := []modelCandidate{{Model: candModel32, Score: 100}, {Model: candModel14, Score: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rankedCandidates = %+v, want %+v", got, want)
	}
}

func TestRankedCandidates_MalformedRequireAdmitsNothing(t *testing.T) {
	rule := &kubemootv1alpha1.SchedulingRule{Require: &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: candTier, Operator: "Bogus"}},
	}}
	got := rankedCandidates(testModelID, rule, []kubemootv1alpha1.Model{candModel("m", testModelID, true, nil)}, 0, false)
	if len(got) != 0 {
		t.Fatalf("want no candidates under a malformed selector, got %+v", got)
	}
}

func TestPhaseQualityBias(t *testing.T) {
	agent := &kubemootv1alpha1.Agent{Spec: kubemootv1alpha1.AgentSpec{Capabilities: []string{candReasoning}}}
	policy := &kubemootv1alpha1.CrewSchedulingPolicy{Spec: kubemootv1alpha1.CrewSchedulingPolicySpec{
		QualityBias: map[string]string{candReasoning: candBias},
	}}
	plain := &kubemootv1alpha1.SchedulingRule{Phase: phaseMulling}
	if b, ok := phaseQualityBias(agent, policy, plain); !ok || b != 0.8 {
		t.Errorf("bias = %v ok=%v, want 0.8 true", b, ok)
	}
	withPrefer := &kubemootv1alpha1.SchedulingRule{Prefer: []kubemootv1alpha1.PreferenceTerm{{Weight: 1}}}
	if _, ok := phaseQualityBias(agent, policy, withPrefer); ok {
		t.Error("an explicit Prefer block disables the auto-derived bias")
	}
	if _, ok := phaseQualityBias(agent, nil, plain); ok {
		t.Error("no policy, no bias")
	}
	if _, ok := phaseQualityBias(agent, policy, nil); ok {
		t.Error("no rule, no bias")
	}
}

func TestCandidateEnvVars_RenderJSONPerPhase(t *testing.T) {
	mulling := &modelPick{ModelID: candModel32, Candidates: []modelCandidate{
		{Model: candModel32, Score: 70}, {Model: candModel14, Score: 60},
	}}
	triage := &modelPick{ModelID: testModelID}
	env := candidateEnvVars(mulling, triage)
	if len(env) != 1 || env[0].Name != envModelCandidatesMulling {
		t.Fatalf("want only the mulling list (triage has none), got %+v", env)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(env[0].Value), &parsed); err != nil {
		t.Fatalf("candidates env is not JSON: %v", err)
	}
	if parsed[0]["model"] != candModel32 || parsed[0]["score"] != float64(70) || parsed[1]["model"] != candModel14 {
		t.Errorf("unexpected candidates JSON %s", env[0].Value)
	}
	if got := candidateEnvVars(nil, nil); len(got) != 0 {
		t.Errorf("nil picks render no env, got %+v", got)
	}
}

// pickModel publishes the phase's candidates with the bound model first, and
// buildEnvVars hands them to the runtime while keeping KUBEMOOT_MODEL_MODEL.
func TestPickModel_PublishesRankedCandidates(t *testing.T) {
	scheme := agentReconcileScheme(t)
	high := candModel("big", candModel32, true, map[string]string{latencyClassLabel: candHigh})
	medium := candModel("mid", candModel14, true, map[string]string{latencyClassLabel: candMedium})
	prov := &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: candProvider, Namespace: "ns"},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Endpoint: testOllamaURL},
		Status:     kubemootv1alpha1.ModelProviderStatus{Ready: true},
	}
	policy := &kubemootv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pol", Namespace: "ns"},
		Spec: kubemootv1alpha1.CrewSchedulingPolicySpec{
			CrewRef:     candCrew,
			Rules:       []kubemootv1alpha1.SchedulingRule{{Phase: phaseMulling}},
			QualityBias: map[string]string{candReasoning: "0.9"},
		},
	}
	objs := []client.Object{&high, &medium, prov, policy}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: candAgent, Namespace: "ns", Labels: map[string]string{labelCrew: candCrew}},
		Spec:       kubemootv1alpha1.AgentSpec{Capabilities: []string{candReasoning}},
	}

	pick, err := r.pickModel(context.Background(), agent, phaseMulling, "")
	if err != nil {
		t.Fatalf("pickModel: %v", err)
	}
	want := []modelCandidate{{Model: candModel32, Score: 90}, {Model: candModel14, Score: 20}}
	if pick.ModelID != candModel32 || !reflect.DeepEqual(pick.Candidates, want) {
		t.Fatalf("pick=%s candidates=%+v, want qwen3:32b and %+v", pick.ModelID, pick.Candidates, want)
	}

	env := r.buildEnvVars(context.Background(), agent, pick, pick, 8080)
	if v, ok := envValue(env, "KUBEMOOT_MODEL_MODEL"); !ok || v != candModel32 {
		t.Errorf("KUBEMOOT_MODEL_MODEL = %q, want the preferred model", v)
	}
	if v, ok := envValue(env, envModelCandidatesMulling); !ok || v != `[{"model":"qwen3:32b","score":90},{"model":"qwen3:14b","score":20}]` {
		t.Errorf("%s = %q", envModelCandidatesMulling, v)
	}
	if _, ok := envValue(env, envModelCandidatesTriage); !ok {
		t.Errorf("%s missing", envModelCandidatesTriage)
	}
}
