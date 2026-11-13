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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	searchTestNS       = "crew-a"
	searchTestSource   = "kubectl-reference"
	searchTestServer   = "kubectl-reference-search"
	searchTestInst     = "crew-a-release"
	searchTestEndpoint = "http://kubectl-reference-query.crew-a:8000/mcp"
	searchTestRepo     = "https://example.com/r"
	searchTestChart    = "crew"
)

func searchTestRAGSource(mutate func(*kubemootv1alpha1.RAGSource)) *kubemootv1alpha1.RAGSource {
	rs := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{
			Name: searchTestSource, Namespace: searchTestNS, UID: types.UID("uid-rag"),
			Labels: map[string]string{
				labelInstance:                 searchTestInst,
				labelManagedBy:                "Helm",
				"helm.sh/chart":               searchTestChart,
				"helm.toolkit.fluxcd.io/name": searchTestChart,
				"team":                        "platform",
			},
		},
	}
	if mutate != nil {
		mutate(rs)
	}
	return rs
}

func searchTestReconciler(t *testing.T, objs ...client.Object) (*RAGSourceReconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithObjects(objs...).Build()
	return &RAGSourceReconciler{Client: c, Scheme: c.Scheme(), ConfigCache: NewConfigCache()}, c
}

func getSearchServer(c client.Client) (*kubemootv1alpha1.MCPServer, error) {
	ms := &kubemootv1alpha1.MCPServer{}
	err := c.Get(context.Background(), types.NamespacedName{Name: searchTestServer, Namespace: searchTestNS}, ms)
	return ms, err
}

func TestRAGSearchEndpointUsesTheQueryServicePort(t *testing.T) {
	rs := searchTestRAGSource(nil)
	if got, want := ragSearchEndpoint(rs), searchTestEndpoint; got != want {
		t.Errorf("endpoint = %q, want %q", got, want)
	}
	rs.Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{Port: 9100}
	if got, want := ragSearchEndpoint(rs), "http://kubectl-reference-query.crew-a:9100/mcp"; got != want {
		t.Errorf("custom port endpoint = %q, want %q", got, want)
	}
}

func TestRAGSearchServerNameIsBounded(t *testing.T) {
	if got := ragSearchServerName(searchTestSource); got != searchTestServer {
		t.Errorf("name = %q", got)
	}
	long := ragSearchServerName("a-very-long-ragsource-name-that-goes-on-and-on-past-the-limit-of-labels")
	if len(long) > 63 {
		t.Errorf("name %q is %d characters, want at most 63", long, len(long))
	}
}

func TestRAGSearchOffered(t *testing.T) {
	cases := map[string]struct {
		mutate func(*kubemootv1alpha1.RAGSource)
		want   bool
	}{
		"a plain source": {nil, true},
		"query service switched off": {func(rs *kubemootv1alpha1.RAGSource) {
			rs.Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{Enabled: ptr.To(false)}
		}, false},
		"a source the operator made for itself": {func(rs *kubemootv1alpha1.RAGSource) {
			rs.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: "kubemoot.ai/v1alpha1", Kind: "Agent", Name: "coordinator", UID: "u", Controller: ptr.To(true),
			}}
		}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ragSearchOffered(searchTestRAGSource(tc.mutate)); got != tc.want {
				t.Errorf("ragSearchOffered = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildRAGSearchServerPointsTheGatewayAtTheQueryService(t *testing.T) {
	ms := buildRAGSearchServer(searchTestRAGSource(nil))
	if ms.Spec.ExternalEndpoint != searchTestEndpoint {
		t.Errorf("endpoint = %q", ms.Spec.ExternalEndpoint)
	}
	if ms.Spec.Transport != kubemootv1alpha1.TransportHTTP {
		t.Errorf("transport = %q, want http", ms.Spec.Transport)
	}
	if ms.Spec.Image != "" {
		t.Errorf("a search server runs no pod, image = %q", ms.Spec.Image)
	}
}

func TestRAGSearchLabelsCarryTheGatewaySelectorAndNotTheInstallersLabels(t *testing.T) {
	labels := ragSearchLabels(searchTestRAGSource(nil))
	want := map[string]string{
		labelInstance:    searchTestInst,
		"team":           "platform",
		labelManagedBy:   managedByValue,
		labelGeneratedBy: generatedByRAGSearch,
		labelRAGSource:   searchTestSource,
		labelName:        searchTestServer,
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}
	for _, k := range []string{"helm.sh/chart", "helm.toolkit.fluxcd.io/name"} {
		if _, ok := labels[k]; ok {
			t.Errorf("label %s belongs to the installer and must not be copied", k)
		}
	}
}

func TestReconcileSearchToolCreatesAnOwnedMCPServer(t *testing.T) {
	rs := searchTestRAGSource(nil)
	r, c := searchTestReconciler(t)
	if err := r.reconcileSearchTool(context.Background(), rs); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	ms, err := getSearchServer(c)
	if err != nil {
		t.Fatalf("search MCPServer not created: %v", err)
	}
	if !metav1.IsControlledBy(ms, rs) {
		t.Errorf("the MCPServer must be owned by the RAGSource, owners: %v", ms.OwnerReferences)
	}
	if ms.Labels[labelInstance] != searchTestInst {
		t.Errorf("instance label = %q", ms.Labels[labelInstance])
	}
}

func TestReconcileSearchToolRestoresADriftedMCPServer(t *testing.T) {
	rs := searchTestRAGSource(nil)
	r, c := searchTestReconciler(t)
	ctx := context.Background()
	if err := r.reconcileSearchTool(ctx, rs); err != nil {
		t.Fatalf("create: %v", err)
	}
	ms, _ := getSearchServer(c)
	ms.Spec.ExternalEndpoint = "http://elsewhere:1/mcp"
	ms.Labels[labelInstance] = testMismatchName
	if err := c.Update(ctx, ms); err != nil {
		t.Fatalf("drift: %v", err)
	}
	if err := r.reconcileSearchTool(ctx, rs); err != nil {
		t.Fatalf("restore: %v", err)
	}
	ms, _ = getSearchServer(c)
	if ms.Spec.ExternalEndpoint != searchTestEndpoint || ms.Labels[labelInstance] != searchTestInst {
		t.Errorf("not restored: endpoint %q, instance %q", ms.Spec.ExternalEndpoint, ms.Labels[labelInstance])
	}
}

func TestReconcileSearchToolMovesWithTheQueryServicePort(t *testing.T) {
	rs := searchTestRAGSource(nil)
	r, c := searchTestReconciler(t)
	ctx := context.Background()
	_ = r.reconcileSearchTool(ctx, rs)
	rs.Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{Port: 9100}
	if err := r.reconcileSearchTool(ctx, rs); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	ms, _ := getSearchServer(c)
	if ms.Spec.ExternalEndpoint != "http://kubectl-reference-query.crew-a:9100/mcp" {
		t.Errorf("endpoint = %q", ms.Spec.ExternalEndpoint)
	}
}

func TestReconcileSearchToolRemovesItWhenTheQueryServiceIsSwitchedOff(t *testing.T) {
	rs := searchTestRAGSource(nil)
	r, c := searchTestReconciler(t)
	ctx := context.Background()
	_ = r.reconcileSearchTool(ctx, rs)
	rs.Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{Enabled: ptr.To(false)}
	if err := r.reconcileSearchTool(ctx, rs); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := getSearchServer(c); err == nil {
		t.Error("the search MCPServer must be deleted when the query service is off")
	}
}

func TestReconcileSearchToolLeavesAPersonsMCPServerAlone(t *testing.T) {
	mine := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: searchTestServer, Namespace: searchTestNS},
		Spec:       kubemootv1alpha1.MCPServerSpec{ExternalEndpoint: "http://mine:1/mcp"},
	}
	rs := searchTestRAGSource(nil)
	r, c := searchTestReconciler(t, mine)
	ctx := context.Background()
	if err := r.reconcileSearchTool(ctx, rs); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	ms, _ := getSearchServer(c)
	if ms.Spec.ExternalEndpoint != "http://mine:1/mcp" {
		t.Errorf("a hand-written MCPServer was changed: %q", ms.Spec.ExternalEndpoint)
	}
	rs.Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{Enabled: ptr.To(false)}
	if err := r.reconcileSearchTool(ctx, rs); err != nil {
		t.Fatalf("reconcile off: %v", err)
	}
	if _, err := getSearchServer(c); err != nil {
		t.Errorf("a hand-written MCPServer was deleted: %v", err)
	}
}

func TestReconcileSearchToolOffersNothingForAnOperatorMadeSource(t *testing.T) {
	rs := searchTestRAGSource(func(rs *kubemootv1alpha1.RAGSource) {
		rs.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "kubemoot.ai/v1alpha1", Kind: "MCPGateway", Name: "gw", UID: "u", Controller: ptr.To(true),
		}}
	})
	r, c := searchTestReconciler(t)
	if err := r.reconcileSearchTool(context.Background(), rs); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := getSearchServer(c); err == nil {
		t.Error("an operator-made RAGSource must get no search tool")
	}
}

// The real API server accepts the generated MCPServer (its CEL rules and defaults), and a
// gateway selecting by the crew's instance label lists it.
var _ = Describe("RAGSource search tool", func() {
	It("creates an MCPServer the crew gateway selects", func() {
		rs := &kubemootv1alpha1.RAGSource{
			ObjectMeta: metav1.ObjectMeta{
				Name: "envtest-reference", Namespace: celNamespace,
				Labels: map[string]string{labelInstance: "envtest-crew"},
			},
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					Type: kubemootv1alpha1.RAGSourceTypeGit, Git: &kubemootv1alpha1.GitSource{URL: searchTestRepo},
				},
				VectorStore:       kubemootv1alpha1.VectorStoreConfig{Type: FallbackVectorStoreType, Endpoint: "pg:5432", Collection: "c"},
				EmbeddingModelRef: "nomic",
			},
		}
		Expect(k8sClient.Create(ctx, rs)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, rs) })

		r := &RAGSourceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
		Expect(r.reconcileSearchTool(ctx, rs)).To(Succeed())

		list := &kubemootv1alpha1.MCPServerList{}
		Expect(k8sClient.List(ctx, list, client.InNamespace(celNamespace),
			client.MatchingLabels{labelInstance: "envtest-crew"})).To(Succeed())
		Expect(list.Items).To(HaveLen(1))
		ms := list.Items[0]
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, &ms) })
		Expect(ms.Name).To(Equal("envtest-reference-search"))
		Expect(ms.Spec.ExternalEndpoint).To(Equal("http://envtest-reference-query." + celNamespace + ":8000/mcp"))
		Expect(ms.Spec.Transport).To(Equal(kubemootv1alpha1.TransportHTTP))

		// Running it again changes nothing.
		Expect(r.reconcileSearchTool(ctx, rs)).To(Succeed())
		again := &kubemootv1alpha1.MCPServer{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&ms), again)).To(Succeed())
		Expect(again.ResourceVersion).To(Equal(ms.ResourceVersion))
	})
})
