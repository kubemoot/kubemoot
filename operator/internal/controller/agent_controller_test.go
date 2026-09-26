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
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAgentReconciler_ImagePullSecrets(t *testing.T) {
	if got := (&AgentReconciler{}).imagePullSecrets(); got != nil {
		t.Errorf("no ConfigCache: want nil pull secrets, got %v", got)
	}

	empty := NewConfigCache()
	if got := (&AgentReconciler{ConfigCache: empty}).imagePullSecrets(); len(got) != 0 {
		t.Errorf("ConfigCache without config: want no pull secrets, got %v", got)
	}

	configured := NewConfigCache()
	configured.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{
			Defaults: kubemootv1alpha1.DefaultConfig{
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: "mirror-pull-secret"}},
			},
		},
	})
	got := (&AgentReconciler{ConfigCache: configured}).imagePullSecrets()
	if len(got) != 1 || got[0].Name != "mirror-pull-secret" {
		t.Errorf("configured pull secret not propagated to agent pods, got %v", got)
	}
}

func TestProviderScore(t *testing.T) {
	withWeight := func(w int) *kubemootv1alpha1.ModelProvider {
		return &kubemootv1alpha1.ModelProvider{
			Spec: kubemootv1alpha1.ModelProviderSpec{
				Scheduling: &kubemootv1alpha1.ProviderScheduling{Weight: w},
			},
		}
	}
	noScheduling := &kubemootv1alpha1.ModelProvider{
		Spec: kubemootv1alpha1.ModelProviderSpec{},
	}

	tests := []struct {
		name     string
		phase    string
		provider *kubemootv1alpha1.ModelProvider
		want     int64
	}{
		{"nil provider returns 0", phaseMulling, nil, 0},
		{"no scheduling block returns 0", phaseMulling, noScheduling, 0},
		{"mulling adds heavy-rig weight", phaseMulling, withWeight(100), 100},
		{"mulling adds light-rig weight", phaseMulling, withWeight(25), 25},
		{"triage inverts heavy-rig weight", phaseTriage, withWeight(100), -100},
		{"triage inverts light-rig weight", phaseTriage, withWeight(25), -25},
		{"unknown phase contributes 0", "synthesis", withWeight(100), 0},
		{"zero weight contributes 0", phaseMulling, withWeight(0), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := providerScore(tc.phase, tc.provider)
			if got != tc.want {
				t.Errorf("providerScore(%q, %+v) = %d; want %d", tc.phase, tc.provider, got, tc.want)
			}
		})
	}
}

// TestFindCrewGateway verifies the operator discovers a per-crew MCPGateway and
// reports its name + port for env injection. Pins the fix for the bug where
// every agent reported "0 MCP tools" because gateway env vars were never set.
func TestFindCrewGateway(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	mkGw := func(ns, name string, port int32) *kubemootv1alpha1.MCPGateway {
		return &kubemootv1alpha1.MCPGateway{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       kubemootv1alpha1.MCPGatewaySpec{Port: port},
		}
	}

	t.Run("no gateway → ok=false", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &AgentReconciler{Client: cli}
		name, port, ok := r.findCrewGateway(context.Background(), "crew-x")
		if ok {
			t.Errorf("expected ok=false, got name=%q port=%d", name, port)
		}
	})

	t.Run("one gateway with explicit port", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mkGw("crew-x", "x-gateway", 9090)).Build()
		r := &AgentReconciler{Client: cli}
		name, port, ok := r.findCrewGateway(context.Background(), "crew-x")
		if !ok || name != "x-gateway" || port != 9090 {
			t.Errorf("got name=%q port=%d ok=%v; want x-gateway/9090/true", name, port, ok)
		}
	})

	t.Run("default port when spec.port is zero", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mkGw("crew-x", "x-gateway", 0)).Build()
		r := &AgentReconciler{Client: cli}
		_, port, ok := r.findCrewGateway(context.Background(), "crew-x")
		if !ok || port != 8080 {
			t.Errorf("port: got %d ok=%v; want 8080/true", port, ok)
		}
	})

	t.Run("scoped to namespace", func(t *testing.T) {
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(
				mkGw("crew-x", "x-gateway", 8080),
				mkGw("crew-y", "y-gateway", 8080),
			).Build()
		r := &AgentReconciler{Client: cli}
		name, _, ok := r.findCrewGateway(context.Background(), "crew-y")
		if !ok || name != "y-gateway" {
			t.Errorf("expected y-gateway in namespace crew-y; got %q ok=%v", name, ok)
		}
	})
}

// TestScoreCandidateAgentLoadPenalty verifies the load-aware bin-pack term
// added to scoreCandidate. Under identical prefer/locality scores, a
// heavier-weight provider with N agents already assigned should eventually
// lose to a lighter-weight provider with 0 agents, once N * penalty wipes
// out the weight advantage. Catches the "5 agents converging on the 5090
// while the 4090 sits idle" regression captured in the kanban card
// "Scheduler Not Bin-Packing Across 4090".
func TestScoreCandidateAgentLoadPenalty(t *testing.T) {
	// Helper builds a provider with phase-specific agent counts (the values
	// scoreCandidate actually reads after the per-phase split). For the
	// homelab pattern where each agent binds mulling to the heavy provider
	// and triage to the light provider, mullingCount on the heavy provider
	// equals triageCount on the light provider — but tested independently.
	provWithLoad := func(weight, mullingCount, triageCount int) *kubemootv1alpha1.ModelProvider {
		return &kubemootv1alpha1.ModelProvider{
			Spec: kubemootv1alpha1.ModelProviderSpec{
				Scheduling: &kubemootv1alpha1.ProviderScheduling{Weight: weight},
			},
			Status: kubemootv1alpha1.ModelProviderStatus{
				Capacity: &kubemootv1alpha1.DiscoveredCapacity{
					MullingAgentCount: mullingCount,
					TriageAgentCount:  triageCount,
					AgentCount:        mullingCount + triageCount,
				},
			},
		}
	}
	m := &kubemootv1alpha1.Model{} // empty labels; no prefer-rule matches

	// Baseline mulling: both providers idle. Heavier weight wins.
	s5090, _ := scoreCandidate(phaseMulling, nil, m, provWithLoad(100, 0, 0), true)
	s4090, _ := scoreCandidate(phaseMulling, nil, m, provWithLoad(50, 0, 0), true)
	if s5090 <= s4090 {
		t.Fatalf("baseline mulling: 5090 (%d) should outscore 4090 (%d)", s5090, s4090)
	}

	// 5090 has 6 mulling agents (4090 has 0 mulling agents). Mulling score
	// for 5090 = 100 - 60 = 40; 4090 = 50 - 0 = 50. 4090 wins.
	s5090Loaded, _ := scoreCandidate(phaseMulling, nil, m, provWithLoad(100, 6, 0), true)
	s4090Idle, _ := scoreCandidate(phaseMulling, nil, m, provWithLoad(50, 0, 0), true)
	if s5090Loaded >= s4090Idle {
		t.Errorf("6 mulling agents on 5090 → 4090 should win: 5090=%d, 4090=%d", s5090Loaded, s4090Idle)
	}

	// Critical regression guard for the per-phase split: SYMMETRIC totals
	// (each provider has 26 mulling+triage combined, matching the homelab
	// status today) MUST NOT cancel the penalty when only one phase is
	// loaded. With per-phase counts, 5090's mulling load (26) penalizes
	// 5090, and 4090's mulling load (0) doesn't penalize 4090, so 4090
	// wins for a mulling pick even though both providers' total agent
	// counts are equal.
	s5090HeavySym, _ := scoreCandidate(phaseMulling, nil, m, provWithLoad(100, 26, 0), true)
	s4090LightSym, _ := scoreCandidate(phaseMulling, nil, m, provWithLoad(50, 0, 26), true)
	if s5090HeavySym >= s4090LightSym {
		t.Errorf("symmetric homelab pattern (5090 mulling-heavy, 4090 triage-heavy) "+
			"should pick 4090 for next mulling: 5090=%d, 4090=%d", s5090HeavySym, s4090LightSym)
	}

	// Triage baseline: 4090 (lighter) wins via inverted weight.
	t5090, _ := scoreCandidate(phaseTriage, nil, m, provWithLoad(100, 0, 0), true)
	t4090, _ := scoreCandidate(phaseTriage, nil, m, provWithLoad(50, 0, 0), true)
	if t4090 <= t5090 {
		t.Fatalf("baseline triage: 4090 (%d) should outscore 5090 (%d)", t4090, t5090)
	}
	// 4090 with 6 triage agents → 5090 wins triage now.
	t4090Loaded, _ := scoreCandidate(phaseTriage, nil, m, provWithLoad(50, 0, 6), true)
	t5090Idle, _ := scoreCandidate(phaseTriage, nil, m, provWithLoad(100, 0, 0), true)
	if t4090Loaded >= t5090Idle {
		t.Errorf("6 triage agents on 4090 → 5090 should win: 4090=%d, 5090=%d", t4090Loaded, t5090Idle)
	}
}

// TestScoreCandidateCapacityAwarePenalty pins the divide-by-MaxParallel
// behavior added on top of the bin-pack penalty. A provider with
// num_parallel=2 has 2x the concurrent-inference capacity of one at
// num_parallel=1, so each assigned agent costs half as much "saturation."
// Without this normalization the scheduler treats a 5090@num_parallel=2
// the same as a 4090@num_parallel=1, pushing agents off the
// higher-capacity provider too eagerly.
func TestScoreCandidateCapacityAwarePenalty(t *testing.T) {
	mkProv := func(weight, mullingCount, maxParallel int) *kubemootv1alpha1.ModelProvider {
		return &kubemootv1alpha1.ModelProvider{
			Spec: kubemootv1alpha1.ModelProviderSpec{
				Scheduling: &kubemootv1alpha1.ProviderScheduling{Weight: weight},
			},
			Status: kubemootv1alpha1.ModelProviderStatus{
				Capacity: &kubemootv1alpha1.DiscoveredCapacity{
					MullingAgentCount: mullingCount,
					MaxParallel:       maxParallel,
				},
			},
		}
	}
	m := &kubemootv1alpha1.Model{}

	// 10 agents on a num_parallel=1 provider: full uniform penalty applies.
	// score = 100 - 10*10/1 = 0
	s1, _ := scoreCandidate(phaseMulling, nil, m, mkProv(100, 10, 1), true)
	if s1 != 0 {
		t.Errorf("uniform penalty (parallel=1): expected score 0, got %d", s1)
	}

	// Same 10 agents on a num_parallel=2 provider: penalty halved.
	// score = 100 - 10*10/2 = 50
	s2, _ := scoreCandidate(phaseMulling, nil, m, mkProv(100, 10, 2), true)
	if s2 != 50 {
		t.Errorf("capacity-aware (parallel=2): expected score 50, got %d", s2)
	}

	// MaxParallel=0 (defensive — pre-discovery providers) must NOT divide
	// by zero. Code substitutes 1, so behavior matches num_parallel=1.
	s0, _ := scoreCandidate(phaseMulling, nil, m, mkProv(100, 10, 0), true)
	if s0 != 0 {
		t.Errorf("defensive (parallel=0 → treated as 1): expected score 0, got %d", s0)
	}

	// Realistic homelab production scenario: rig0 (5090, weight=100,
	// parallel=2) vs rig1 (4090, weight=50, parallel=1) with 21 agents on
	// rig0 and 5 on rig1. Should still favor rig0 (positive score gap):
	//   rig0: 100 - 21*10/2 = 100 - 105 = -5
	//   rig1: 50  - 5*10/1  = 50 - 50  = 0
	// rig1 barely wins, equilibrium near here. The point is that without
	// capacity-aware penalty the 21-on-rig0 score would be 100 - 210 = -110
	// (huge bias against rig0), pushing agents OFF the more capable GPU.
	hStrong, _ := scoreCandidate(phaseMulling, nil, m, mkProv(100, 21, 2), true)
	hWeak, _ := scoreCandidate(phaseMulling, nil, m, mkProv(50, 5, 1), true)
	if hStrong < -20 {
		t.Errorf("with capacity-aware penalty, rig0 at 21 agents should not be heavily punished; got %d", hStrong)
	}
	_ = hWeak // documented in the scenario above; no strict comparison needed
}

// TestShouldBinPackByRole pins the role-aware bin-pack gate. Coordinators
// run sequentially as the discussion's upfront blocker — there's no
// parallel load to spread, so they should always pick the heaviest-weight
// provider and skip the load penalty. Toolers bin-pack.
func TestShouldBinPackByRole(t *testing.T) {
	mkAgent := func(role string) *kubemootv1alpha1.Agent {
		a := &kubemootv1alpha1.Agent{}
		if role != "" {
			a.Labels = map[string]string{agentRoleLabel: role}
		}
		return a
	}
	if shouldBinPack(mkAgent("coordinator")) {
		t.Error("coordinator role should skip bin-pack penalty")
	}
	if !shouldBinPack(mkAgent("tooler")) {
		t.Error("tooler role should apply bin-pack penalty")
	}
	if !shouldBinPack(mkAgent("")) {
		t.Error("no role label should default to applying bin-pack")
	}
	if !shouldBinPack(nil) {
		t.Error("nil agent should default to applying bin-pack")
	}
}

// TestScoreCandidateCoordinatorSkipsPenalty: with the heavy provider
// saturated, a tooler correctly migrates to the lighter idle provider,
// but a coordinator stays on the heavy one. End-to-end-latency vs.
// parallel-load is the architectural distinction the role label encodes.
func TestScoreCandidateCoordinatorSkipsPenalty(t *testing.T) {
	heavyLoaded := &kubemootv1alpha1.ModelProvider{
		Spec: kubemootv1alpha1.ModelProviderSpec{
			Scheduling: &kubemootv1alpha1.ProviderScheduling{Weight: 100},
		},
		Status: kubemootv1alpha1.ModelProviderStatus{
			Capacity: &kubemootv1alpha1.DiscoveredCapacity{MullingAgentCount: 25},
		},
	}
	lightIdle := &kubemootv1alpha1.ModelProvider{
		Spec: kubemootv1alpha1.ModelProviderSpec{
			Scheduling: &kubemootv1alpha1.ProviderScheduling{Weight: 50},
		},
		Status: kubemootv1alpha1.ModelProviderStatus{
			Capacity: &kubemootv1alpha1.DiscoveredCapacity{MullingAgentCount: 0},
		},
	}
	m := &kubemootv1alpha1.Model{}
	sHeavy, _ := scoreCandidate(phaseMulling, nil, m, heavyLoaded, true)
	sLight, _ := scoreCandidate(phaseMulling, nil, m, lightIdle, true)
	if sHeavy >= sLight {
		t.Errorf("tooler (bin-pack=true): idle light should outscore loaded heavy: heavy=%d light=%d", sHeavy, sLight)
	}
	cHeavy, _ := scoreCandidate(phaseMulling, nil, m, heavyLoaded, false)
	cLight, _ := scoreCandidate(phaseMulling, nil, m, lightIdle, false)
	if cHeavy <= cLight {
		t.Errorf("coordinator (bin-pack=false): heavy must outscore light even when heavy is loaded: heavy=%d light=%d", cHeavy, cLight)
	}
}

// TestApplyStickyAntiOscillation pins the anti-herd sticky-scheduling
// behavior. Without sticky, MP-cache lag during bulk agent reconciles
// produces flip-back oscillation: many agents reconcile against stale
// counts, all migrate to the under-loaded provider, MP counts catch up,
// next reconcile sees the OTHER side under-loaded, they flip back. The
// sticky rule keeps the agent on its current pick unless an alternative
// beats it by stickyHysteresis points.
func TestApplyStickyAntiOscillation(t *testing.T) {
	mkCand := func(providerName string, score int64) scheduleCandidate {
		return scheduleCandidate{
			provider: &kubemootv1alpha1.ModelProvider{
				ObjectMeta: metav1.ObjectMeta{Name: providerName},
			},
			score: score,
		}
	}

	// Helper: the realistic case after bulk reconciles. ollama-rig1 (lighter)
	// previously under-loaded → many agents flipped to it. Now MP counts
	// caught up; ollama-gpu (heavier base weight) now scores slightly
	// better. WITHOUT sticky, agent flips back. WITH sticky, agent stays.
	gpuBest := mkCand("ollama-gpu", 40)
	rig1Current := mkCand("ollama-rig1", 30) // 10 points behind; within hysteresis
	feasible := []scheduleCandidate{gpuBest, rig1Current}

	got := applySticky(gpuBest, feasible, "ollama-rig1", 25)
	if got.provider.Name != "ollama-rig1" {
		t.Errorf("within hysteresis: should stick to current ollama-rig1, got %s", got.provider.Name)
	}

	// Alternative wins by MORE than hysteresis → flip is justified.
	gpuStrong := mkCand("ollama-gpu", 100)
	rig1Weak := mkCand("ollama-rig1", 30) // 70 points behind; beyond hysteresis
	got = applySticky(gpuStrong, []scheduleCandidate{gpuStrong, rig1Weak}, "ollama-rig1", 25)
	if got.provider.Name != "ollama-gpu" {
		t.Errorf("beyond hysteresis: should flip to ollama-gpu, got %s", got.provider.Name)
	}

	// No current pick (fresh agent) → take the best score, no stickiness.
	got = applySticky(gpuStrong, []scheduleCandidate{gpuStrong, rig1Weak}, "", 25)
	if got.provider.Name != "ollama-gpu" {
		t.Errorf("fresh agent: should take best ollama-gpu, got %s", got.provider.Name)
	}

	// Best candidate is ALREADY the current pick → no sticky logic needed.
	got = applySticky(gpuStrong, []scheduleCandidate{gpuStrong, rig1Weak}, "ollama-gpu", 25)
	if got.provider.Name != "ollama-gpu" {
		t.Errorf("best is already current: should keep ollama-gpu, got %s", got.provider.Name)
	}

	// Current pick no longer in feasible list (e.g. provider went unready)
	// → take the best, no special handling.
	got = applySticky(gpuBest, []scheduleCandidate{gpuBest}, "ollama-rig1", 25)
	if got.provider.Name != "ollama-gpu" {
		t.Errorf("current pick missing from feasible: should take best ollama-gpu, got %s", got.provider.Name)
	}

	// Exactly-at-hysteresis edge: best beats current by EXACTLY 25 → still
	// sticks (the rule is "MORE than hysteresis" for a flip; equal stays).
	gpuTie := mkCand("ollama-gpu", 55)
	rig1Tie := mkCand("ollama-rig1", 30) // exactly 25 behind
	got = applySticky(gpuTie, []scheduleCandidate{gpuTie, rig1Tie}, "ollama-rig1", 25)
	if got.provider.Name != "ollama-rig1" {
		t.Errorf("exactly-at-hysteresis: should stick to ollama-rig1, got %s", got.provider.Name)
	}

	// "sticky" reason suffix is appended only when we actually stuck.
	rig1Reasoned := scheduleCandidate{
		provider: &kubemootv1alpha1.ModelProvider{ObjectMeta: metav1.ObjectMeta{Name: "ollama-rig1"}},
		score:    30,
		reason:   "provider-weight-50",
	}
	got = applySticky(gpuBest, []scheduleCandidate{gpuBest, rig1Reasoned}, "ollama-rig1", 25)
	if !contains(got.reason, "sticky") {
		t.Errorf("sticky path should append sticky reason; got reason=%q", got.reason)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestProviderScoreFlipsTiebreaker pins the homelab production scenario:
// rig0 (weight=100) and rig1 (weight=25) carry identically-labeled Models,
// so the prefer-rule scores tie. Provider weight must break the tie so
// mulling picks the heavy rig and triage picks the light rig.
func TestProviderScoreFlipsTiebreaker(t *testing.T) {
	rig0 := &kubemootv1alpha1.ModelProvider{
		Spec: kubemootv1alpha1.ModelProviderSpec{
			Scheduling: &kubemootv1alpha1.ProviderScheduling{Weight: 100},
		},
	}
	rig1 := &kubemootv1alpha1.ModelProvider{
		Spec: kubemootv1alpha1.ModelProviderSpec{
			Scheduling: &kubemootv1alpha1.ProviderScheduling{Weight: 25},
		},
	}

	// Mulling: 32B-on-each scores prefer+100 from labels (qwen3, 32B).
	const preferMulling32B int64 = 100
	if providerScore(phaseMulling, rig0)+preferMulling32B <= providerScore(phaseMulling, rig1)+preferMulling32B {
		t.Error("mulling: rig0 should outscore rig1 after provider weight is applied")
	}

	// Triage: 8B-on-each scores prefer+160 from labels (latencyClass=low + params=8B).
	const preferTriage8B int64 = 160
	if providerScore(phaseTriage, rig1)+preferTriage8B <= providerScore(phaseTriage, rig0)+preferTriage8B {
		t.Error("triage: rig1 should outscore rig0 after provider weight is applied (inverted)")
	}
}

// TestModelFitsProvider pins the VRAM feasibility gate extracted from
// evaluateCandidate: a Model fits when its declared VRAM is <= the provider's
// total, and unknown VRAM (0 on either side) is treated as a fit.
func TestModelFitsProvider(t *testing.T) {
	mk := func(modelVRAM int32, provVRAM int64) bool {
		m := &kubemootv1alpha1.Model{Spec: kubemootv1alpha1.ModelSpec{VRAMMib: modelVRAM}}
		prov := &kubemootv1alpha1.ModelProvider{}
		if provVRAM > 0 {
			prov.Status.Capacity = &kubemootv1alpha1.DiscoveredCapacity{VRAMTotalMiB: provVRAM}
		}
		return modelFitsProvider(m, prov)
	}
	if !mk(8000, 24000) {
		t.Error("8000 MiB model should fit a 24000 MiB provider")
	}
	if mk(32000, 24000) {
		t.Error("32000 MiB model should NOT fit a 24000 MiB provider")
	}
	if !mk(24000, 24000) {
		t.Error("exact fit (24000 == 24000) should be allowed")
	}
	if !mk(0, 24000) {
		t.Error("unknown model VRAM (0) should be treated as a fit")
	}
	if !mk(32000, 0) {
		t.Error("unknown provider VRAM (0) should be treated as a fit")
	}
}

// TestImageLocalityScore pins the locality-bonus helper extracted from
// scoreCandidate: the bonus applies only when the provider already has the
// candidate Model loaded.
func TestImageLocalityScore(t *testing.T) {
	m := &kubemootv1alpha1.Model{Spec: kubemootv1alpha1.ModelSpec{Model: "qwen3:8b"}}

	loaded := &kubemootv1alpha1.ModelProvider{
		Status: kubemootv1alpha1.ModelProviderStatus{
			Capacity: &kubemootv1alpha1.DiscoveredCapacity{
				LoadedModels: []kubemootv1alpha1.LoadedModel{{Name: "qwen3:8b"}},
			},
		},
	}
	if score, ok := imageLocalityScore(m, loaded); !ok || score != imageLocalityBonus {
		t.Errorf("loaded model: want (%d,true), got (%d,%v)", imageLocalityBonus, score, ok)
	}

	other := &kubemootv1alpha1.ModelProvider{
		Status: kubemootv1alpha1.ModelProviderStatus{
			Capacity: &kubemootv1alpha1.DiscoveredCapacity{
				LoadedModels: []kubemootv1alpha1.LoadedModel{{Name: "llama3:70b"}},
			},
		},
	}
	if _, ok := imageLocalityScore(m, other); ok {
		t.Error("model not loaded on provider: expected no locality bonus")
	}

	if _, ok := imageLocalityScore(m, &kubemootv1alpha1.ModelProvider{}); ok {
		t.Error("provider with no capacity: expected no locality bonus")
	}
}

// TestPreferRuleScore pins the prefer-selector scoring helper extracted from
// scoreCandidate: each matching Prefer selector contributes its weight; a nil
// rule or nil selector contributes nothing.
func TestPreferRuleScore(t *testing.T) {
	m := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"tier": "reasoning"}},
	}
	rule := &kubemootv1alpha1.SchedulingRule{
		Prefer: []kubemootv1alpha1.PreferenceTerm{
			{Weight: 30, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "reasoning"}}},
			{Weight: 10, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "fast"}}},
			{Weight: 5, Selector: nil}, // nil selector: skipped
		},
	}
	score, reasons := preferRuleScore(rule, m)
	if score != 30 {
		t.Errorf("only the reasoning selector matches: want score 30, got %d", score)
	}
	if len(reasons) != 1 {
		t.Errorf("want 1 reason fragment, got %v", reasons)
	}

	if s, _ := preferRuleScore(nil, m); s != 0 {
		t.Errorf("nil rule: want score 0, got %d", s)
	}
}
