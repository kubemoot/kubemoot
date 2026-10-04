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
	"fmt"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// scoreCandidate sums the soft-preference weight, the image-locality bonus (when
// the provider already has the model loaded), the provider phase score, and the
// load penalty. These tests drive the pure scoring path with no client.
func TestScoreCandidate_PreferAndLocality(t *testing.T) {
	rule := &kubemootv1alpha1.SchedulingRule{
		Phase: testMulling,
		Prefer: []kubemootv1alpha1.PreferenceTerm{{
			Weight:   25,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{testTier: testFast}},
		}},
	}
	m := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testTier: testFast}},
		Spec:       kubemootv1alpha1.ModelSpec{Model: testModelID},
	}
	prov := &kubemootv1alpha1.ModelProvider{
		Status: kubemootv1alpha1.ModelProviderStatus{
			Capacity: &kubemootv1alpha1.DiscoveredCapacity{
				LoadedModels: []kubemootv1alpha1.LoadedModel{{Name: testModelID}},
			},
		},
	}
	score, reasons := scoreCandidate(testMulling, rule, m, prov, false)
	// 25 (prefer) + 10 (locality bonus) = 35.
	if score != 35 {
		t.Errorf("score = %d, want 35 (prefer 25 + locality 10); reasons=%v", score, reasons)
	}
	if len(reasons) == 0 {
		t.Error("scoreCandidate should report the reason fragments")
	}
}

// Agent pods must talk to the same NATS the operator does: the chart's nats.url
// reaches the operator as NATS_URL and is forwarded; the in-cluster default is
// only for an operator that has none.
func TestAgentNATSURL(t *testing.T) {
	t.Setenv("NATS_URL", "")
	if got := agentNATSURL(); got != defaultNATSURL {
		t.Errorf("no NATS_URL: want default %q, got %q", defaultNATSURL, got)
	}
	t.Setenv("NATS_URL", "nats://messaging.example:4222")
	if got := agentNATSURL(); got != "nats://messaging.example:4222" {
		t.Errorf("NATS_URL set: want it forwarded, got %q", got)
	}
}

// buildEnvVars assembles the agent Deployment env. With a crew label, an
// MCPGateway, and a Crew in the namespace it exercises the skills-dir, gateway,
// and working-memory env branches.
func TestBuildEnvVars(t *testing.T) {
	scheme := agentReconcileScheme(t)
	gw := &kubemootv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "ns"}}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: testCrewA, Namespace: "ns"}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(gw, crew).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testK8s, Namespace: "ns", Labels: map[string]string{labelCrew: testCrewA}},
	}
	mulling := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}
	triage := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}

	env := r.buildEnvVars(context.Background(), agent, mulling, triage, 8080)
	byName := map[string]string{}
	for _, e := range env {
		byName[e.Name] = e.Value
	}
	if byName["KUBEMOOT_SKILLS_DIR"] != skillsMountPath {
		t.Errorf("crew agent should get KUBEMOOT_SKILLS_DIR, got %q", byName["KUBEMOOT_SKILLS_DIR"])
	}
	if byName["KUBEMOOT_GATEWAY_ENABLED"] != testTrue {
		t.Errorf("an MCPGateway in the namespace should enable gateway wiring, got %q", byName["KUBEMOOT_GATEWAY_ENABLED"])
	}
	if byName["KUBEMOOT_GATEWAY_ENDPOINT"] != "http://gw.ns:8080" {
		t.Errorf("gateway endpoint = %q, want http://gw.ns:8080", byName["KUBEMOOT_GATEWAY_ENDPOINT"])
	}
	if byName["KUBEMOOT_MEMORY_ENABLED"] == "" {
		t.Error("memoryEnvVars should emit KUBEMOOT_MEMORY_ENABLED")
	}
}

// markUnschedulable stamps the Agent status and requeues. With an error the
// message is the error text; without one it is the default explanation.
func TestMarkUnschedulable(t *testing.T) {
	scheme := agentReconcileScheme(t)
	mk := func(name string, mullingErr error) *kubemootv1alpha1.Agent {
		agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"}}
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(agent).WithStatusSubresource(agent).Build()
		r := &AgentReconciler{Client: cli, Scheme: scheme}
		res, err := r.markUnschedulable(context.Background(), agent, mullingErr)
		if err != nil {
			t.Fatalf("markUnschedulable: %v", err)
		}
		if res.RequeueAfter == 0 {
			t.Error("markUnschedulable should requeue")
		}
		got := &kubemootv1alpha1.Agent{}
		if err := cli.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "ns"}, got); err != nil {
			t.Fatalf("get: %v", err)
		}
		return got
	}
	withErr := mk("a1", fmt.Errorf("no GPU"))
	if withErr.Status.Phase != testPhaseUnschedulable || withErr.Status.Ready {
		t.Errorf("status not stamped: phase=%q ready=%v", withErr.Status.Phase, withErr.Status.Ready)
	}
	if withErr.Status.Message != "no GPU" {
		t.Errorf("message should carry the error, got %q", withErr.Status.Message)
	}
	noErr := mk("a2", nil)
	if noErr.Status.Message == "" || noErr.Status.Message == "no GPU" {
		t.Errorf("nil error should use the default message, got %q", noErr.Status.Message)
	}
}

// pickModel drives the full feasibility + scoring chain: list Models, gate each
// (Ready, require, ready provider, VRAM fit), score, rank, and resolve the
// winning (Model, Provider) into a modelPick.
func TestPickModel(t *testing.T) {
	scheme := agentReconcileScheme(t)
	mdl := func(name, id, provRef string, ready bool, vram int32) *kubemootv1alpha1.Model {
		return &kubemootv1alpha1.Model{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
			Spec:       kubemootv1alpha1.ModelSpec{Model: id, ProviderRef: provRef, VRAMMib: vram},
			Status:     kubemootv1alpha1.ModelStatus{Ready: ready},
		}
	}
	prv := func(name string, ready bool, vramTotal int64, loaded string) *kubemootv1alpha1.ModelProvider {
		p := &kubemootv1alpha1.ModelProvider{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
			Spec:       kubemootv1alpha1.ModelProviderSpec{Endpoint: "http://" + name + ":11434"},
			Status:     kubemootv1alpha1.ModelProviderStatus{Ready: ready},
		}
		if vramTotal > 0 || loaded != "" {
			p.Status.Capacity = &kubemootv1alpha1.DiscoveredCapacity{VRAMTotalMiB: vramTotal}
			if loaded != "" {
				p.Status.Capacity.LoadedModels = []kubemootv1alpha1.LoadedModel{{Name: loaded}}
			}
		}
		return p
	}

	// winner: provider already has it loaded -> +locality, the highest score.
	winner := mdl("winner", testModelID, "provA", true, 0)
	// runnerUp: feasible but unloaded -> lower score (forces the sort comparator).
	runnerUp := mdl("runnerup", "llama3:8b", "provB", true, 0)
	// notReady: filtered at the readiness gate.
	notReady := mdl("cold", "llama3:70b", "provB", false, 0)
	// provDown points at a not-ready provider -> rejected at the provider gate.
	provDown := mdl(testOrphan, "mistral", "provC", true, 0)
	// tooBig declares more VRAM than its provider has -> rejected at the VRAM gate.
	tooBig := mdl("huge", "qwen3:235b", "provD", true, 100000)

	objs := []client.Object{
		winner, runnerUp, notReady, provDown, tooBig,
		prv("provA", true, 24000, testModelID),
		prv("provB", true, 24000, ""),
		prv("provC", false, 24000, ""),
		prv("provD", true, 8000, ""),
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: testK8s, Namespace: "ns"}}

	pick, err := r.pickModel(context.Background(), agent, testMulling, "")
	if err != nil {
		t.Fatalf("pickModel: %v", err)
	}
	if pick.ModelID != testModelID {
		t.Errorf("picked ModelID = %q, want qwen3:8b (the loaded model wins on locality)", pick.ModelID)
	}
	if pick.Endpoint != "http://provA:11434" {
		t.Errorf("pick endpoint = %q", pick.Endpoint)
	}

	// No Ready Model -> an error, not a panic.
	cli2 := fake.NewClientBuilder().WithScheme(scheme).WithObjects(notReady, prv("provB", true, 0, "")).Build()
	r2 := &AgentReconciler{Client: cli2, Scheme: scheme, ConfigCache: NewConfigCache()}
	if _, err := r2.pickModel(context.Background(), agent, testMulling, ""); err == nil {
		t.Error("expected an error when no Ready Model is feasible")
	}
}

// pickModel under a CrewSchedulingPolicy that has a Require selector and a
// QualityBias map (no Prefer) exercises findPolicyAndRule, the candidateFeasible
// require gate, and the auto-derived quality-bias scoring path.
func TestPickModel_WithPolicy(t *testing.T) {
	scheme := agentReconcileScheme(t)
	model := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: testFast, Namespace: "ns",
			Labels: map[string]string{testTier: "prod", latencyClassLabel: testHigh}},
		Spec:   kubemootv1alpha1.ModelSpec{Model: testModelID, ProviderRef: testOllama},
		Status: kubemootv1alpha1.ModelStatus{Ready: true},
	}
	// A model that fails the Require selector must be filtered at the require gate.
	wrongTier := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "ns",
			Labels: map[string]string{testTier: "dev"}},
		Spec:   kubemootv1alpha1.ModelSpec{Model: "llama3:8b", ProviderRef: testOllama},
		Status: kubemootv1alpha1.ModelStatus{Ready: true},
	}
	prov := &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: testOllama, Namespace: "ns"},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Endpoint: testOllamaURL},
		Status:     kubemootv1alpha1.ModelProviderStatus{Ready: true},
	}
	policy := &kubemootv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pol", Namespace: "ns"},
		Spec: kubemootv1alpha1.CrewSchedulingPolicySpec{
			CrewRef: testCrewA,
			Rules: []kubemootv1alpha1.SchedulingRule{{
				Phase:   testMulling,
				Require: &metav1.LabelSelector{MatchLabels: map[string]string{testTier: "prod"}},
			}},
			QualityBias: map[string]string{testReasoning: "0.8"},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model, wrongTier, prov, policy).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testK8s, Namespace: "ns", Labels: map[string]string{labelCrew: testCrewA}},
		Spec:       kubemootv1alpha1.AgentSpec{Capabilities: []string{testReasoning}},
	}
	pick, err := r.pickModel(context.Background(), agent, testMulling, "")
	if err != nil {
		t.Fatalf("pickModel: %v", err)
	}
	if pick.ModelID != testModelID {
		t.Errorf("the require-matching, bias-favoured model should win, got %q", pick.ModelID)
	}
}

func TestScoreCandidate_NoMatchNoBonus(t *testing.T) {
	rule := &kubemootv1alpha1.SchedulingRule{
		Phase: testMulling,
		Prefer: []kubemootv1alpha1.PreferenceTerm{{
			Weight:   25,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{testTier: testFast}},
		}},
	}
	// Model lacks the label and the provider has nothing loaded -> zero score.
	m := &kubemootv1alpha1.Model{Spec: kubemootv1alpha1.ModelSpec{Model: "llama3:70b"}}
	prov := &kubemootv1alpha1.ModelProvider{}
	score, _ := scoreCandidate(testMulling, rule, m, prov, true)
	if score != 0 {
		t.Errorf("a non-matching, non-local candidate should score 0, got %d", score)
	}
}
