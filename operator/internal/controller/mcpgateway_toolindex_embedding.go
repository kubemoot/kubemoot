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
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The gateway condition that says whether the tool index found its EmbeddingModel.
const (
	conditionTypeToolIndexEmbedding = "ToolIndexEmbeddingResolved"
	reasonEmbeddingResolved         = "Resolved"
	reasonEmbeddingNotFound         = "EmbeddingModelNotFound"
	reasonEmbeddingLookupFailed     = "LookupFailed"
)

// toolIndexEmbedding is the name of the EmbeddingModel resource the gateway's tool
// index embeds with and the field that named it: toolIndex.embeddingModelRef, else
// (the earlier form) toolIndex.embeddingModel.model, else the KubemootConfig default.
func toolIndexEmbedding(gateway *kubemootv1alpha1.MCPGateway, configDefault string) (name, source string) {
	ti := gateway.Spec.ToolIndex
	switch {
	case ti != nil && ti.EmbeddingModelRef != "":
		return ti.EmbeddingModelRef, "spec.toolIndex.embeddingModelRef"
	case ti != nil && ti.EmbeddingModel.Model != "":
		return ti.EmbeddingModel.Model, "spec.toolIndex.embeddingModel.model"
	}
	return configDefault, "the KubemootConfig default embedding model"
}

// toolIndexEmbeddingRef is the name from toolIndexEmbedding.
func toolIndexEmbeddingRef(gateway *kubemootv1alpha1.MCPGateway, configDefault string) string {
	name, _ := toolIndexEmbedding(gateway, configDefault)
	return name
}

// recordToolIndexEmbedding sets the ToolIndexEmbeddingResolved condition in memory:
// True when the EmbeddingModel exists (in the gateway's namespace, else anywhere),
// False naming the missing resource and the field that named it, Unknown when the
// lookup failed.
func (r *MCPGatewayReconciler) recordToolIndexEmbedding(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) {
	name, source := toolIndexEmbedding(gateway, r.ConfigCache.GetEmbeddingModel())
	cond := metav1.Condition{Type: conditionTypeToolIndexEmbedding, Status: metav1.ConditionTrue, Reason: reasonEmbeddingResolved,
		Message: fmt.Sprintf("The tool index embeds with EmbeddingModel %s", name)}
	em, err := findEmbeddingModel(ctx, r.Client, gateway.Namespace, name)
	switch {
	case err != nil:
		logf.FromContext(ctx).Error(err, "could not look up the tool index EmbeddingModel", "name", name)
		cond.Status, cond.Reason = metav1.ConditionUnknown, reasonEmbeddingLookupFailed
		cond.Message = fmt.Sprintf("Could not look up EmbeddingModel %q: %v", name, err)
	case em == nil:
		cond.Status, cond.Reason = metav1.ConditionFalse, reasonEmbeddingNotFound
		cond.Message = fmt.Sprintf("EmbeddingModel %q (named by %s) was not found in namespace %s or cluster-wide; set spec.toolIndex.embeddingModelRef to an existing EmbeddingModel",
			name, source, gateway.Namespace)
	}
	meta.SetStatusCondition(&gateway.Status.Conditions, cond)
}

// syncToolIndexEmbeddingRef points an existing tool index RAGSource at the
// EmbeddingModel the gateway names. It patches only that field, so a concurrent
// write cannot conflict, and writes nothing when the RAGSource already names it.
func (r *MCPGatewayReconciler) syncToolIndexEmbeddingRef(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, ragSource *kubemootv1alpha1.RAGSource) error {
	want := toolIndexEmbeddingRef(gateway, r.ConfigCache.GetEmbeddingModel())
	if ragSource.Spec.EmbeddingModelRef == want {
		return nil
	}
	patch := client.MergeFrom(ragSource.DeepCopy())
	ragSource.Spec.EmbeddingModelRef = want
	return r.Patch(ctx, ragSource, patch)
}
