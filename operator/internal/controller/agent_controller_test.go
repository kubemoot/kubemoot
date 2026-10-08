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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: testPullSecret}},
			},
		},
	})
	got := (&AgentReconciler{ConfigCache: configured}).imagePullSecrets()
	if len(got) != 1 || got[0].Name != testPullSecret {
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
		{"unknown phase contributes 0", testSynthesis, withWeight(100), 0},
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
	dying := func(name string, port int32) *kubemootv1alpha1.MCPGateway {
		gw := mkGw(testCrewNamespace, name, port)
		now := metav1.Now()
		gw.DeletionTimestamp = &now
		gw.Finalizers = []string{mcpGatewayFinalizer}
		return gw
	}

	cases := []struct {
		name      string
		objects   []client.Object
		namespace string
		wantOK    bool
		wantName  string
		checkPort bool
		wantPort  int32
	}{
		{name: "no gateway gives ok=false", namespace: testCrewNamespace},
		{
			name:      "one gateway with explicit port",
			objects:   []client.Object{mkGw(testCrewNamespace, "x-gateway", 9090)},
			namespace: testCrewNamespace, wantOK: true, wantName: "x-gateway", checkPort: true, wantPort: 9090,
		},
		{
			name:      "default port when spec.port is zero",
			objects:   []client.Object{mkGw(testCrewNamespace, "x-gateway", 0)},
			namespace: testCrewNamespace, wantOK: true, wantName: "x-gateway", checkPort: true, wantPort: 8080,
		},
		{
			name: "scoped to namespace",
			objects: []client.Object{
				mkGw(testCrewNamespace, "x-gateway", 8080),
				mkGw("crew-y", "y-gateway", 8080),
			},
			namespace: "crew-y", wantOK: true, wantName: "y-gateway",
		},
		{
			name: "several gateways: first by name, terminating skipped",
			objects: []client.Object{
				mkGw(testCrewNamespace, "c-gateway", 8080),
				dying("a-gateway", 8080),
				mkGw(testCrewNamespace, "b-gateway", 9090),
			},
			namespace: testCrewNamespace, wantOK: true, wantName: "b-gateway", checkPort: true, wantPort: 9090,
		},
		{
			name:      "only a terminating gateway gives ok=false",
			objects:   []client.Object{dying("x-gateway", 8080)},
			namespace: testCrewNamespace,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.objects...).Build()
			r := &AgentReconciler{Client: cli}
			name, port, ok := r.findCrewGateway(context.Background(), tc.namespace)
			assertCrewGateway(t, crewGatewayResult{name: name, port: port, ok: ok},
				crewGatewayWant{ok: tc.wantOK, name: tc.wantName, checkPort: tc.checkPort, port: tc.wantPort})
		})
	}
}

// crewGatewayResult is what findCrewGateway returned.
type crewGatewayResult struct {
	name string
	port int32
	ok   bool
}

// crewGatewayWant is the expected findCrewGateway outcome; the port is checked only when checkPort is set.
type crewGatewayWant struct {
	name      string
	port      int32
	ok        bool
	checkPort bool
}

// assertCrewGateway checks a findCrewGateway result against the expected outcome.
func assertCrewGateway(t *testing.T, got crewGatewayResult, want crewGatewayWant) {
	t.Helper()
	if got.ok != want.ok {
		t.Fatalf("ok: got %v (name=%q port=%d); want %v", got.ok, got.name, got.port, want.ok)
	}
	if !want.ok {
		return
	}
	if got.name != want.name {
		t.Errorf("name: got %q; want %q", got.name, want.name)
	}
	if want.checkPort && got.port != want.port {
		t.Errorf("port: got %d; want %d", got.port, want.port)
	}
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

// TestPreferRuleScore pins the prefer-selector scoring helper extracted from
// scoreCandidate: each matching Prefer selector contributes its weight; a nil
// rule or nil selector contributes nothing.
func TestPreferRuleScore(t *testing.T) {
	m := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{testTier: testReasoning}},
	}
	rule := &kubemootv1alpha1.SchedulingRule{
		Prefer: []kubemootv1alpha1.PreferenceTerm{
			{Weight: 30, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{testTier: testReasoning}}},
			{Weight: 10, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{testTier: testFast}}},
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
