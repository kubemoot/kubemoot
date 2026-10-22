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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// labelEmbeddingServer names the embedding server an indexing Job embeds through.
	// The operator counts Jobs by it to give each server a limited number of builds.
	labelEmbeddingServer = "kubemoot.ai/embedding-server"
	// indexSlotPoll is how often a Queued RAGSource looks again. Whether a slot is free is
	// decided from the Jobs that exist; the poll only makes sure it looks.
	indexSlotPoll = 10 * time.Second
	// maxLabelValue is the longest value a Kubernetes label holds.
	maxLabelValue = 63
)

// embeddingServerKey identifies the server an EmbeddingModel is embedded through, as a
// valid label value; "" when no server is bound, which has nothing to share.
func embeddingServerKey(em *kubemootv1alpha1.EmbeddingModel) string {
	name := em.BoundServerName()
	if name == "" {
		return ""
	}
	key := em.Namespace + "." + name
	if len(key) <= maxLabelValue {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:maxLabelValue]
}

// indexingJobLabels are the labels of an indexing Job: its RAGSource and the embedding
// server it embeds through.
func indexingJobLabels(rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel, jobName string) map[string]string {
	labels := map[string]string{
		labelName:      rs.Name,
		labelInstance:  jobName,
		labelManagedBy: managedByValue,
		labelComponent: componentIndexer,
		labelRAGSource: rs.Name,
	}
	if key := embeddingServerKey(em); key != "" {
		labels[labelEmbeddingServer] = key
	}
	return labels
}

// activeBuildsOnServer counts the indexing Jobs now running against the server.
func (r *RAGSourceReconciler) activeBuildsOnServer(ctx context.Context, key string) (int, error) {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.MatchingLabels{
		labelComponent:       componentIndexer,
		labelEmbeddingServer: key,
	}); err != nil {
		return 0, err
	}
	return countActiveJobs(jobs), nil
}

// countActiveJobs counts the Jobs that are neither complete nor failed.
func countActiveJobs(jobs *batchv1.JobList) int {
	active := 0
	for i := range jobs.Items {
		complete, failed := jobConditions(&jobs.Items[i])
		if !complete && !failed && jobs.Items[i].DeletionTimestamp == nil {
			active++
		}
	}
	return active
}

// queueForSlot holds the RAGSource in the Queued phase while the embedding server runs
// as many index builds as it may. It reports queued when the RAGSource has no slot yet.
func (r *RAGSourceReconciler) queueForSlot(ctx context.Context, rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) (bool, error) {
	key := embeddingServerKey(em)
	if key == "" {
		return false, nil
	}
	active, err := r.activeBuildsOnServer(ctx, key)
	if err != nil {
		return false, err
	}
	limit := int(r.ConfigCache.GetMaxIndexBuildsPerServer())
	if active < limit {
		return false, nil
	}
	logf.FromContext(ctx).Info("Embedding server is at its index build limit; queueing",
		"server", key, "running", active, "limit", limit)
	return true, r.recordQueued(ctx, rs, em, active, limit)
}

// recordQueued writes the Queued status. A RAGSource that already serves an index stays
// Ready while its new index waits.
func (r *RAGSourceReconciler) recordQueued(ctx context.Context, rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel, active, limit int) error {
	message := fmt.Sprintf("Queued: embedding server %s is running %d of %d index builds; this one starts when a build finishes",
		em.BoundServerName(), active, limit)
	if rs.Status.Phase == phaseQueued && rs.Status.Message == message {
		return nil
	}
	rs.Status.Phase = phaseQueued
	rs.Status.Ready = rebuilding(rs)
	rs.Status.Message = message
	status := metav1.ConditionFalse
	if rs.Status.Ready {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&rs.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             status,
		Reason:             phaseQueued,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
	return r.Status().Update(ctx, rs)
}
