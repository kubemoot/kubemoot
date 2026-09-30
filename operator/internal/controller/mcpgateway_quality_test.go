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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// quality evaluation order: allowing -> blocking -> tested -> considering.
// These tests drive EvaluateServerQuality (and the matcher helpers it calls)
// through each branch without needing the AI agent or live HTTP.

func TestBuildToolIndexVectorStore(t *testing.T) {
	// No ToolIndex -> zero-value config.
	if vs := buildToolIndexVectorStore(&kubemootv1alpha1.MCPGateway{}); vs.Type != "" || vs.Collection != "" {
		t.Errorf("no ToolIndex should yield a zero-value config, got %+v", vs)
	}
	gw := &kubemootv1alpha1.MCPGateway{Spec: kubemootv1alpha1.MCPGatewaySpec{
		ToolIndex: &kubemootv1alpha1.ToolIndexConfig{
			VectorStore:    kubemootv1alpha1.GatewayVectorStoreConfig{Host: "pg", Port: 5432, Database: "tools", SecretRef: "pg-creds"},
			EmbeddingModel: kubemootv1alpha1.GatewayEmbeddingModelConfig{Dimensions: 1024},
		},
	}}
	vs := buildToolIndexVectorStore(gw)
	if vs.Type != kubemootv1alpha1.VectorStorePgvector {
		t.Errorf("type = %q, want pgvector", vs.Type)
	}
	if vs.Endpoint != "postgres://pg:5432/tools" {
		t.Errorf("endpoint = %q", vs.Endpoint)
	}
	if vs.Dimensions != 1024 {
		t.Errorf("dimensions = %d, want 1024", vs.Dimensions)
	}
	if vs.Collection != "mcp_tools" {
		t.Errorf("collection should default to mcp_tools, got %q", vs.Collection)
	}
}

func TestBuildToolIndexIndexerConfig(t *testing.T) {
	// No Registries -> nil.
	if c := buildToolIndexIndexerConfig(&kubemootv1alpha1.MCPGateway{}); c != nil {
		t.Errorf("no Registries should yield nil, got %+v", c)
	}
	gw := &kubemootv1alpha1.MCPGateway{Spec: kubemootv1alpha1.MCPGatewaySpec{
		Registries: &kubemootv1alpha1.MCPRegistriesConfig{IndexerImage: "indexer:v1", SyncInterval: "1h"},
	}}
	c := buildToolIndexIndexerConfig(gw)
	if c == nil || c.Image != "indexer:v1" || c.Schedule != "1h" {
		t.Errorf("indexer config not built from registries: %+v", c)
	}
}

func TestBuildToolIndexRAGSource(t *testing.T) {
	r := &MCPGatewayReconciler{ConfigCache: NewConfigCache()}
	gw := &kubemootv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "ns"},
		Spec: kubemootv1alpha1.MCPGatewaySpec{
			Registries: &kubemootv1alpha1.MCPRegistriesConfig{
				IndexerImage: "indexer:v1",
				Sources:      []kubemootv1alpha1.MCPRegistrySource{{Name: "mcprun", URL: "https://reg", Type: "mcp-run"}},
			},
			ToolIndex: &kubemootv1alpha1.ToolIndexConfig{
				VectorStore:    kubemootv1alpha1.GatewayVectorStoreConfig{Host: "pg", Port: 5432, Database: "tools", SecretRef: "pg-creds"},
				EmbeddingModel: kubemootv1alpha1.GatewayEmbeddingModelConfig{Model: "nomic-embed", Dimensions: 768},
			},
		},
	}
	rag := r.buildToolIndexRAGSource(gw)
	if rag.Name != "gw-tools" || rag.Namespace != "ns" {
		t.Errorf("RAGSource identity = %s/%s, want ns/gw-tools", rag.Namespace, rag.Name)
	}
	if rag.Spec.Source.Type != kubemootv1alpha1.RAGSourceTypeMCPRegistry || rag.Spec.Source.MCPRegistry == nil {
		t.Fatalf("source should be an MCP registry: %+v", rag.Spec.Source)
	}
	if rag.Spec.Source.MCPRegistry.URL != "https://reg" {
		t.Errorf("registry URL = %q", rag.Spec.Source.MCPRegistry.URL)
	}
	// The gateway's explicit embedding model overrides the config-cache default.
	if rag.Spec.EmbeddingModelRef != "nomic-embed" {
		t.Errorf("embedding model ref = %q, want the gateway override", rag.Spec.EmbeddingModelRef)
	}
}

func TestEvaluateServerQuality_Allowing(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &MCPGatewayReconciler{Client: cli}
	policy := &kubemootv1alpha1.MCPQualityPolicy{
		Spec: kubemootv1alpha1.MCPQualityPolicySpec{
			Allowing: []kubemootv1alpha1.AllowingEntry{{Name: "github-mcp"}},
		},
	}
	d := r.EvaluateServerQuality(context.Background(), policy, MCPServerMetadata{Name: "GitHub-MCP"})
	if d.Action != "allow" {
		t.Errorf("an allowing-listed server (case-insensitive) should allow, got %q (%s)", d.Action, d.Reason)
	}
}

func TestEvaluateServerQuality_BlockingGlob(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &MCPGatewayReconciler{Client: cli}
	glob := kubemootv1alpha1.StringMatcher{Type: kubemootv1alpha1.MatcherTypeGlob, Value: "evil-*"}
	policy := &kubemootv1alpha1.MCPQualityPolicy{
		Spec: kubemootv1alpha1.MCPQualityPolicySpec{
			Blocking: []kubemootv1alpha1.BlockingEntry{{Name: &glob, Reason: "known bad actor"}},
		},
	}
	d := r.EvaluateServerQuality(context.Background(), policy, MCPServerMetadata{Name: "evil-miner"})
	if d.Action != "deny" {
		t.Fatalf("a glob-blocked server should deny, got %q", d.Action)
	}
	if d.Reason == "" {
		t.Error("a block decision should carry the blocking reason")
	}
}

func TestEvaluateServerQuality_ConsideringDisabledFallback(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &MCPGatewayReconciler{Client: cli}
	// No allowing/blocking match, no tested tier, considering disabled with an
	// explicit allow fallback -> the fallback action wins.
	policy := &kubemootv1alpha1.MCPQualityPolicy{
		Spec: kubemootv1alpha1.MCPQualityPolicySpec{
			Considering: &kubemootv1alpha1.ConsideringConfig{Enabled: false, FallbackAction: "allow"},
		},
	}
	d := r.EvaluateServerQuality(context.Background(), policy, MCPServerMetadata{Name: "unknown"})
	if d.Action != "allow" {
		t.Errorf("disabled considering should use the fallback action, got %q (%s)", d.Action, d.Reason)
	}
}

func TestEvaluateServerQuality_TestedAvoidBlocks(t *testing.T) {
	scheme := agentReconcileScheme(t)
	report := &kubemootv1alpha1.MCPServerReport{
		ObjectMeta: metav1.ObjectMeta{Name: sanitizeK8sName("broken-mcp"), Namespace: "ns"},
		Status:     kubemootv1alpha1.MCPServerReportStatus{Verdict: "avoid", FailureCount: 9, SuccessRate: "10%"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(report).Build()
	r := &MCPGatewayReconciler{Client: cli}
	policy := &kubemootv1alpha1.MCPQualityPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns"},
		Spec: kubemootv1alpha1.MCPQualityPolicySpec{
			Tested: &kubemootv1alpha1.TestedConfig{Enabled: true, BlockBroken: true},
		},
	}
	d := r.EvaluateServerQuality(context.Background(), policy, MCPServerMetadata{Name: "broken-mcp"})
	if d.Action != "deny" {
		t.Errorf("a tested verdict=avoid server should deny with BlockBroken, got %q (%s)", d.Action, d.Reason)
	}
}

func TestEvaluateServerQuality_TestedUseAllows(t *testing.T) {
	scheme := agentReconcileScheme(t)
	report := &kubemootv1alpha1.MCPServerReport{
		ObjectMeta: metav1.ObjectMeta{Name: sanitizeK8sName("good-mcp"), Namespace: "ns"},
		Status:     kubemootv1alpha1.MCPServerReportStatus{Verdict: "use", SuccessRate: "95%"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(report).Build()
	r := &MCPGatewayReconciler{Client: cli}
	policy := &kubemootv1alpha1.MCPQualityPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns"},
		Spec: kubemootv1alpha1.MCPQualityPolicySpec{
			Tested: &kubemootv1alpha1.TestedConfig{Enabled: true, MinSuccessRate: "0.8"},
		},
	}
	d := r.EvaluateServerQuality(context.Background(), policy, MCPServerMetadata{Name: "good-mcp"})
	if d.Action != "allow" {
		t.Errorf("a tested verdict=use server above the success floor should allow, got %q (%s)", d.Action, d.Reason)
	}
}
