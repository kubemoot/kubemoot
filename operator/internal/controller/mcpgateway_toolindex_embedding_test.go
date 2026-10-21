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
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	gwEmbedPilotModel = "pilot-embed"
	gwEmbedNS         = "crew-x"
)

func toolIndexGateway(ref, model string) *kubemootv1alpha1.MCPGateway {
	return &kubemootv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: gwEmbedNS},
		Spec: kubemootv1alpha1.MCPGatewaySpec{
			Registries: &kubemootv1alpha1.MCPRegistriesConfig{
				Sources: []kubemootv1alpha1.MCPRegistrySource{{Name: "reg", URL: "https://registry.test", Type: "mcp-run"}},
			},
			ToolIndex: &kubemootv1alpha1.ToolIndexConfig{
				VectorStore:       kubemootv1alpha1.GatewayVectorStoreConfig{Host: "pg", Port: 5432, Database: "toolsdb", SecretRef: "pg"},
				EmbeddingModelRef: ref,
				EmbeddingModel:    kubemootv1alpha1.GatewayEmbeddingModelConfig{Model: model},
			},
		},
	}
}

func gatewayEmbeddingReconciler(t *testing.T, objs ...client.Object) *MCPGatewayReconciler {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithObjects(objs...).Build()
	return &MCPGatewayReconciler{Client: cl, Scheme: gatewayScheme(t), ConfigCache: NewConfigCache()}
}

func pilotEmbedding() *kubemootv1alpha1.EmbeddingModel {
	return &kubemootv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: gwEmbedPilotModel, Namespace: gwEmbedNS},
		Spec:       kubemootv1alpha1.EmbeddingModelSpec{Model: embedTestModel},
	}
}

func TestToolIndexEmbeddingRef_RefBeatsModelBeatsDefault(t *testing.T) {
	const fallback = "config-default"
	for name, tc := range map[string]struct {
		gw   *kubemootv1alpha1.MCPGateway
		want string
	}{
		"explicit ref":                  {toolIndexGateway(gwEmbedPilotModel, embedTestModel), gwEmbedPilotModel},
		"model name is the fallback":    {toolIndexGateway("", embedTestModel), embedTestModel},
		"the config default is last":    {toolIndexGateway("", ""), fallback},
		"no tool index uses the config": {&kubemootv1alpha1.MCPGateway{}, fallback},
	} {
		if got := toolIndexEmbeddingRef(tc.gw, fallback); got != tc.want {
			t.Errorf("%s: ref = %q, want %q", name, got, tc.want)
		}
	}
}

func TestBuildToolIndexRAGSource_UsesTheEmbeddingModelRef(t *testing.T) {
	r := gatewayEmbeddingReconciler(t)
	rag := r.buildToolIndexRAGSource(toolIndexGateway(gwEmbedPilotModel, embedTestModel))
	if rag.Spec.EmbeddingModelRef != gwEmbedPilotModel {
		t.Errorf("RAGSource embedding model ref = %q, want %q (the model name must not override the ref)", rag.Spec.EmbeddingModelRef, gwEmbedPilotModel)
	}
	old := r.buildToolIndexRAGSource(toolIndexGateway("", embedTestModel))
	if old.Spec.EmbeddingModelRef != embedTestModel {
		t.Errorf("without a ref the model name still names the resource: %q", old.Spec.EmbeddingModelRef)
	}
}

func gatewayCondition(t *testing.T, gw *kubemootv1alpha1.MCPGateway) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(gw.Status.Conditions, conditionTypeToolIndexEmbedding)
	if c == nil {
		t.Fatal("the gateway has no ToolIndexEmbeddingResolved condition")
	}
	return c
}

func TestRecordToolIndexEmbedding_RefResolves(t *testing.T) {
	r := gatewayEmbeddingReconciler(t, pilotEmbedding())
	gw := toolIndexGateway(gwEmbedPilotModel, embedTestModel) // the model name names no resource
	r.recordToolIndexEmbedding(context.Background(), gw)
	if c := gatewayCondition(t, gw); c.Status != metav1.ConditionTrue || !strings.Contains(c.Message, gwEmbedPilotModel) {
		t.Errorf("condition = %+v, want resolved to %s", c, gwEmbedPilotModel)
	}
}

func TestRecordToolIndexEmbedding_ResolvesClusterWide(t *testing.T) {
	other := pilotEmbedding()
	other.Namespace = "spare-ns"
	r := gatewayEmbeddingReconciler(t, other)
	gw := toolIndexGateway(gwEmbedPilotModel, "")
	r.recordToolIndexEmbedding(context.Background(), gw)
	if c := gatewayCondition(t, gw); c.Status != metav1.ConditionTrue {
		t.Errorf("condition = %+v, want an EmbeddingModel in another namespace to resolve", c)
	}
}

func TestRecordToolIndexEmbedding_MissingRefIsAClearCondition(t *testing.T) {
	r := gatewayEmbeddingReconciler(t, pilotEmbedding())
	gw := toolIndexGateway("no-such-resource", "")
	r.recordToolIndexEmbedding(context.Background(), gw)
	c := gatewayCondition(t, gw)
	if c.Status != metav1.ConditionFalse || c.Reason != reasonEmbeddingNotFound ||
		!strings.Contains(c.Message, "no-such-resource") || !strings.Contains(c.Message, "spec.toolIndex.embeddingModelRef") {
		t.Errorf("condition = %+v, want NotFound naming the resource and the field", c)
	}
}

func TestRecordToolIndexEmbedding_ModelNameFallbackStillResolves(t *testing.T) {
	named := pilotEmbedding()
	named.Name = embedTestModel
	r := gatewayEmbeddingReconciler(t, named)
	gw := toolIndexGateway("", embedTestModel)
	r.recordToolIndexEmbedding(context.Background(), gw)
	if c := gatewayCondition(t, gw); c.Status != metav1.ConditionTrue {
		t.Errorf("condition = %+v, want the old model-name lookup to resolve", c)
	}
	missing := toolIndexGateway("", embedTestModel)
	gatewayEmbeddingReconciler(t, pilotEmbedding()).recordToolIndexEmbedding(context.Background(), missing)
	if c := gatewayCondition(t, missing); c.Status != metav1.ConditionFalse || !strings.Contains(c.Message, "spec.toolIndex.embeddingModel.model") {
		t.Errorf("condition = %+v, want the missing model-name lookup to name its field", c)
	}
}

func TestEnsureToolIndexRAGSource_FollowsTheRefOnAnExistingRAGSource(t *testing.T) {
	gw := toolIndexGateway(gwEmbedPilotModel, embedTestModel)
	r := gatewayEmbeddingReconciler(t, pilotEmbedding(), gw)
	existing := r.buildToolIndexRAGSource(toolIndexGateway("", embedTestModel))
	if err := r.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}

	if _, err := r.ensureToolIndexRAGSource(context.Background(), gw); err != nil {
		t.Fatal(err)
	}

	got := &kubemootv1alpha1.RAGSource{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(existing), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.EmbeddingModelRef != gwEmbedPilotModel {
		t.Errorf("RAGSource ref = %q, want %q", got.Spec.EmbeddingModelRef, gwEmbedPilotModel)
	}
}

func TestEnsureToolIndexRAGSource_LeavesAMatchingRAGSourceAlone(t *testing.T) {
	gw := toolIndexGateway(gwEmbedPilotModel, "")
	r := gatewayEmbeddingReconciler(t, pilotEmbedding(), gw)
	existing := r.buildToolIndexRAGSource(gw)
	if err := r.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	before := existing.ResourceVersion

	if _, err := r.ensureToolIndexRAGSource(context.Background(), gw); err != nil {
		t.Fatal(err)
	}

	got := &kubemootv1alpha1.RAGSource{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(existing), got); err != nil {
		t.Fatal(err)
	}
	if got.ResourceVersion != before {
		t.Error("a RAGSource that already names the right EmbeddingModel must not be written (a write could re-index)")
	}
}
