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

// ragsource_index.go keeps a RAGSource's index in step with the embedding model that
// produced it. The status records what produced the index readers use. When the
// EmbeddingModel is served another way (another model, server or vector size) the
// operator builds a new index in a new collection beside the old one, keeps readers
// on the old one until the new one is complete, then points readers at the new one
// and deletes the old collection once the readers have moved. A rebuild that fails
// leaves the old index serving and is tried again.
package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	componentIndexer         = "indexer"
	envVectorstoreType       = "KUBEMOOT_VECTORSTORE_TYPE"
	envVectorstoreEndpoint   = "KUBEMOOT_VECTORSTORE_ENDPOINT"
	envVectorstoreCollection = "KUBEMOOT_VECTORSTORE_COLLECTION"
	envVectorstoreDimensions = "KUBEMOOT_VECTORSTORE_DIMENSIONS"
)

const (
	// labelIndexCleanup marks the Job that deletes retired collections. It is not the
	// ragsource label, which the indexing logic reads to find a running index build.
	labelIndexCleanup     = "kubemoot.ai/ragsource-cleanup"
	componentIndexCleanup = "rag-index-cleanup"
	// annotationDropped lists the collections a cleanup Job was created to delete.
	annotationDropped = "kubemoot.ai/dropped-collections"
	// retryAfterCleanupFailure is the safety net for a cleanup Job that failed.
	retryAfterCleanupFailure = 5 * time.Minute
	// retryAfterBuildFailure is the safety net for an index build that failed.
	retryAfterBuildFailure = 60 * time.Second
)

// embeddingDimensions is the vector size an EmbeddingModel reports, 0 when unknown.
func embeddingDimensions(em *kubemootv1alpha1.EmbeddingModel) int32 {
	if em.Status.ModelInfo == nil {
		return 0
	}
	return em.Status.ModelInfo.Dimensions
}

// indexDimensions is the vector size an index is built with: the size the
// EmbeddingModel reports, else the one the RAGSource names.
func indexDimensions(rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) int32 {
	if d := embeddingDimensions(em); d > 0 {
		return d
	}
	return rs.Spec.VectorStore.Dimensions
}

// desiredIndex is what an index built now is produced by. Its collection is the
// RAGSource's own; a rebuild gives it a new one.
func desiredIndex(rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) kubemootv1alpha1.IndexProvenance {
	return kubemootv1alpha1.IndexProvenance{
		Collection: rs.Spec.VectorStore.Collection,
		Base:       rs.Spec.VectorStore.Collection,
		Model:      em.Spec.Model,
		Server:     em.Status.BoundServer,
		Endpoint:   em.Status.Endpoint,
		Dimensions: indexDimensions(rs, em),
	}
}

// sameProducer reports whether two indexes come from the same model, on the same
// server, with the same vector size. The endpoint is not part of it: a server that
// moves keeps serving the same vectors.
func sameProducer(a, b kubemootv1alpha1.IndexProvenance) bool {
	return a.Model == b.Model && a.Server == b.Server && a.Dimensions == b.Dimensions
}

// buildCollectionName names the collection for a rebuild: the base collection, a short
// hash of what produces it, and the time, so a collection never reuses an old name.
func buildCollectionName(base string, want kubemootv1alpha1.IndexProvenance, now time.Time) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", want.Model, want.Server, want.Dimensions)))
	return fmt.Sprintf("%s_%s_%s", base, hex.EncodeToString(sum[:])[:6], strconv.FormatInt(now.Unix(), 36))
}

// adoptIndex records the index of a RAGSource indexed before the status carried it:
// it was produced by the embedding model the RAGSource uses now. A RAGSource whose
// spec now names another collection starts over in that collection, as it always has;
// the collection it left is not touched.
func adoptIndex(rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) {
	if rs.Status.Index != nil && rs.Status.Index.Base != rs.Spec.VectorStore.Collection {
		if rs.Status.Building != nil {
			retireCollection(rs, rs.Status.Building.Collection)
		}
		idx := desiredIndex(rs, em)
		rs.Status.Index, rs.Status.Building = &idx, nil
		return
	}
	if rs.Status.Index != nil || rs.Status.Building != nil {
		return
	}
	if rs.Status.IndexingStats == nil || rs.Status.IndexingStats.LastIndexed == nil {
		return
	}
	idx := desiredIndex(rs, em)
	rs.Status.Index = &idx
}

// recordFirstBuild notes the first index of a RAGSource as the one being built, with what
// produces it now.
func recordFirstBuild(rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) {
	if rs.Status.Index == nil {
		first := desiredIndex(rs, em)
		rs.Status.Building = &first
	}
}

// servingIndex is the index readers use: the recorded one, or, before any index is
// complete, what the RAGSource would build first. A recorded index made by the
// binding as it is now is reached at the server's current endpoint.
func servingIndex(rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) kubemootv1alpha1.IndexProvenance {
	if rs.Status.Index != nil {
		serving := *rs.Status.Index
		if want := desiredIndex(rs, em); sameProducer(serving, want) {
			serving.Endpoint = want.Endpoint
		}
		return serving
	}
	return desiredIndex(rs, em)
}

// jobTarget is the index an indexing job writes: the one being built, else the one
// readers use (a new run of the same index), else the first one. The endpoint is
// always where the EmbeddingModel is served now.
func jobTarget(rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) kubemootv1alpha1.IndexProvenance {
	want := desiredIndex(rs, em)
	var target kubemootv1alpha1.IndexProvenance
	switch {
	case rs.Status.Building != nil:
		target = *rs.Status.Building
	case rs.Status.Index != nil:
		target = *rs.Status.Index
	default:
		return want
	}
	target.Endpoint = want.Endpoint
	return target
}

// reconcileIndexBinding keeps the index in step with the EmbeddingModel's binding.
// It reports handled when it has done the reconcile's work (a rebuild is starting,
// running or being abandoned); otherwise the normal indexing flow goes on.
func (r *RAGSourceReconciler) reconcileIndexBinding(ctx context.Context, rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) (ctrl.Result, bool, error) {
	adoptIndex(rs, em)
	if rs.Status.Index == nil {
		return ctrl.Result{}, false, nil
	}
	want := desiredIndex(rs, em)
	var res ctrl.Result
	var err error
	switch {
	case rs.Status.Building != nil && !sameProducer(*rs.Status.Building, want):
		res, err = r.abandonBuild(ctx, rs)
	case rs.Status.Building != nil:
		res, err = r.continueBuild(ctx, rs, em)
	case !sameProducer(*rs.Status.Index, want):
		res, err = r.startRebuild(ctx, rs, em, want)
	default:
		rs.Status.Index.Endpoint = want.Endpoint
		res, err = r.reconcileRetired(ctx, rs)
		return res, res.RequeueAfter > 0, err
	}
	return res, true, err
}

// startRebuild begins a build of the index in a new collection. A job still writing
// the current index finishes first.
func (r *RAGSourceReconciler) startRebuild(ctx context.Context, rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel, want kubemootv1alpha1.IndexProvenance) (ctrl.Result, error) {
	if rs.Status.Phase == phaseIndexing && rs.Status.LastJobName != "" {
		return r.checkJobStatus(ctx, rs)
	}
	want.Collection = buildCollectionName(rs.Spec.VectorStore.Collection, want, time.Now())
	rs.Status.Building = &want
	forceFullIndex(rs)
	logf.FromContext(ctx).Info("Embedding binding changed; building a new index beside the current one",
		"current", rs.Status.Index.Collection, "new", want.Collection, "model", want.Model, "server", want.Server)
	return r.runIndexingJob(ctx, rs, em)
}

// continueBuild runs or watches the job that builds the new index.
func (r *RAGSourceReconciler) continueBuild(ctx context.Context, rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) (ctrl.Result, error) {
	if rs.Status.LastJobName != "" && rs.Status.Phase == phaseIndexing {
		return r.checkJobStatus(ctx, rs)
	}
	return r.runIndexingJob(ctx, rs, em)
}

// abandonBuild drops a half-built index whose binding has moved on again. Its
// collection is retired; the current index keeps serving.
func (r *RAGSourceReconciler) abandonBuild(ctx context.Context, rs *kubemootv1alpha1.RAGSource) (ctrl.Result, error) {
	r.deleteJobNamed(ctx, rs.Namespace, rs.Status.LastJobName)
	retireCollection(rs, rs.Status.Building.Collection)
	rs.Status.Building = nil
	rs.Status.LastJobName = ""
	if _, err := r.updateStatus(ctx, rs, phaseReady, true,
		"The embedding model changed again while an index was building; the half-built index is discarded"); err != nil {
		return ctrl.Result{}, err
	}
	return requeueNow(), nil
}

// deleteJobNamed deletes a Job and its pods; a Job that is already gone is fine.
func (r *RAGSourceReconciler) deleteJobNamed(ctx context.Context, namespace, name string) {
	if name == "" {
		return
	}
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, job); err != nil {
		return
	}
	policy := metav1.DeletePropagationBackground
	if err := r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil && !errors.IsNotFound(err) {
		logf.FromContext(ctx).Error(err, "Failed to delete indexing job", "job", name)
	}
}

// retireCollection queues a collection for deletion.
func retireCollection(rs *kubemootv1alpha1.RAGSource, collection string) {
	if collection == "" || slices.Contains(rs.Status.RetiredCollections, collection) {
		return
	}
	if rs.Status.Index != nil && rs.Status.Index.Collection == collection {
		return
	}
	rs.Status.RetiredCollections = append(rs.Status.RetiredCollections, collection)
}

// promoteBuild makes the finished index the one readers use and retires the old one.
func promoteBuild(rs *kubemootv1alpha1.RAGSource) {
	if rs.Status.Building == nil {
		return
	}
	built := *rs.Status.Building
	old := rs.Status.Index
	rs.Status.Index = &built
	rs.Status.Building = nil
	if old != nil {
		retireCollection(rs, old.Collection)
	}
}

// failBuild records that the index build failed. The current index keeps serving
// and the build is tried again.
func (r *RAGSourceReconciler) failBuild(ctx context.Context, rs *kubemootv1alpha1.RAGSource, reason string) (ctrl.Result, error) {
	rs.Status.LastJobName = ""
	msg := fmt.Sprintf("Building the new index failed: %s. Readers still use collection %s; the build is tried again",
		reason, rs.Status.Index.Collection)
	if _, err := r.updateStatus(ctx, rs, "Error", true, msg); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: retryAfterBuildFailure}, nil
}

// rebuilding is true while a new index is being built beside a serving one.
func rebuilding(rs *kubemootv1alpha1.RAGSource) bool {
	return rs.Status.Building != nil && rs.Status.Index != nil
}

// queryServiceDeploymentName is the name of the RAGSource's query service Deployment.
func queryServiceDeploymentName(rs *kubemootv1alpha1.RAGSource) string {
	return fmt.Sprintf("%s-query", rs.Name)
}

// readersMoved reports whether every reader has moved to the collection: the query
// service Deployment asks for it and has finished replacing its pods. With no query
// service there is no reader to wait for.
func (r *RAGSourceReconciler) readersMoved(ctx context.Context, rs *kubemootv1alpha1.RAGSource, collection string) bool {
	if !queryServiceEnabled(rs) {
		return true
	}
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: rs.Namespace, Name: queryServiceDeploymentName(rs)}
	if err := r.Get(ctx, key, dep); err != nil {
		return errors.IsNotFound(err)
	}
	return deploymentReadsCollection(dep, collection) && rolloutComplete(dep)
}

// deploymentReadsCollection reports whether the query service is set to read the collection.
func deploymentReadsCollection(dep *appsv1.Deployment, collection string) bool {
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == envVectorstoreCollection {
				return e.Value == collection
			}
		}
	}
	return false
}

// rolloutComplete reports whether a Deployment runs only pods of its latest spec.
func rolloutComplete(dep *appsv1.Deployment) bool {
	want := int32(1)
	if dep.Spec.Replicas != nil {
		want = *dep.Spec.Replicas
	}
	s := dep.Status
	return s.ObservedGeneration >= dep.Generation && s.UpdatedReplicas == want && s.Replicas == want && s.AvailableReplicas == want
}

// reconcileRetired deletes retired collections once readers have left them. A
// non-zero RequeueAfter in the result means the reconcile's work is done.
func (r *RAGSourceReconciler) reconcileRetired(ctx context.Context, rs *kubemootv1alpha1.RAGSource) (ctrl.Result, error) {
	if len(rs.Status.RetiredCollections) == 0 || rs.Status.Index == nil {
		return ctrl.Result{}, nil
	}
	if !r.readersMoved(ctx, rs, rs.Status.Index.Collection) {
		return ctrl.Result{}, nil
	}
	running, err := r.indexerPodsRunning(ctx, rs)
	if err != nil || running {
		return ctrl.Result{}, err
	}
	job, err := r.cleanupJob(ctx, rs)
	if err != nil {
		return ctrl.Result{}, err
	}
	if job == nil {
		return r.startCleanup(ctx, rs)
	}
	return r.finishCleanup(ctx, rs, job)
}

// indexerPodsRunning reports whether any indexing pod of the RAGSource still exists.
// A pod of a deleted job can recreate a collection, so none may remain before the
// retired collections are dropped.
func (r *RAGSourceReconciler) indexerPodsRunning(ctx context.Context, rs *kubemootv1alpha1.RAGSource) (bool, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(rs.Namespace),
		client.MatchingLabels{labelRAGSource: rs.Name, labelComponent: componentIndexer}); err != nil {
		return false, err
	}
	return len(pods.Items) > 0, nil
}

// cleanupJob is the RAGSource's cleanup Job, nil when there is none.
func (r *RAGSourceReconciler) cleanupJob(ctx context.Context, rs *kubemootv1alpha1.RAGSource) (*batchv1.Job, error) {
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(rs.Namespace), client.MatchingLabels{labelIndexCleanup: rs.Name}); err != nil {
		return nil, err
	}
	if len(jobs.Items) == 0 {
		return nil, nil
	}
	return &jobs.Items[0], nil
}

// startCleanup creates the Job that deletes the retired collections.
func (r *RAGSourceReconciler) startCleanup(ctx context.Context, rs *kubemootv1alpha1.RAGSource) (ctrl.Result, error) {
	job := r.buildCleanupJob(rs, fmt.Sprintf("%s-cleanup-%d", rs.Name, time.Now().Unix()))
	if err := controllerutil.SetControllerReference(rs, job, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, job); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// finishCleanup acts on the cleanup Job's state: a finished one clears the collections it
// deleted from the status, a failed one is removed and tried again later.
func (r *RAGSourceReconciler) finishCleanup(ctx context.Context, rs *kubemootv1alpha1.RAGSource, job *batchv1.Job) (ctrl.Result, error) {
	complete, failed := jobConditions(job)
	switch {
	case complete:
		dropped := strings.Split(job.Annotations[annotationDropped], ",")
		rs.Status.RetiredCollections = slices.DeleteFunc(rs.Status.RetiredCollections, func(c string) bool { return slices.Contains(dropped, c) })
		r.deleteJobNamed(ctx, rs.Namespace, job.Name)
		if err := r.Status().Update(ctx, rs); err != nil {
			return ctrl.Result{}, err
		}
		return requeueNow(), nil
	case failed:
		logf.FromContext(ctx).Info("Deleting retired collections failed; trying again later", "job", job.Name)
		r.deleteJobNamed(ctx, rs.Namespace, job.Name)
		return ctrl.Result{RequeueAfter: retryAfterCleanupFailure}, nil
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// buildCleanupJob is the Job that runs the indexer image to delete the retired
// collections. It connects to the collection readers use, which it never deletes.
func (r *RAGSourceReconciler) buildCleanupJob(rs *kubemootv1alpha1.RAGSource, name string) *batchv1.Job {
	labels := map[string]string{
		labelName:         rs.Name,
		labelInstance:     name,
		labelManagedBy:    managedByValue,
		labelComponent:    componentIndexCleanup,
		labelIndexCleanup: rs.Name,
	}
	secrets := buildVectorStoreSecretEnv(rs)
	env := make([]corev1.EnvVar, 0, 5+len(secrets))
	env = append(env,
		corev1.EnvVar{Name: envVectorstoreType, Value: string(rs.Spec.VectorStore.Type)},
		corev1.EnvVar{Name: envVectorstoreEndpoint, Value: r.buildJDBCUrl(rs.Spec.VectorStore.Endpoint)},
		corev1.EnvVar{Name: envVectorstoreCollection, Value: rs.Status.Index.Collection},
		corev1.EnvVar{Name: envVectorstoreDimensions, Value: fmt.Sprintf("%d", rs.Status.Index.Dimensions)},
		corev1.EnvVar{Name: "KUBEMOOT_DROP_COLLECTIONS", Value: strings.Join(rs.Status.RetiredCollections, ",")})
	env = append(env, secrets...)
	backoff := int32(2)
	ttl := int32(3600)
	podSpec := hardenedPodSpec(componentIndexer, r.getIndexerImage(rs), env, r.ConfigCache.GetImagePullSecrets())
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: rs.Namespace, Labels: labels,
			Annotations: map[string]string{annotationDropped: strings.Join(rs.Status.RetiredCollections, ",")},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// hardenedPodSpec is the restricted-PSS pod spec of a one-container indexer Job:
// non-root, no privilege escalation, all capabilities dropped, runtime-default seccomp.
func hardenedPodSpec(name, image string, env []corev1.EnvVar, pullSecrets []corev1.LocalObjectReference) corev1.PodSpec {
	nonRoot, noEscalate, uid := true, false, int64(1000)
	seccomp := corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	return corev1.PodSpec{
		RestartPolicy:    corev1.RestartPolicyNever,
		ImagePullSecrets: pullSecrets,
		SecurityContext:  &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, SeccompProfile: &seccomp},
		Containers: []corev1.Container{{
			Name: name, Image: image, Env: env,
			SecurityContext: &corev1.SecurityContext{
				RunAsNonRoot: &nonRoot, AllowPrivilegeEscalation: &noEscalate, RunAsUser: &uid, RunAsGroup: &uid,
				SeccompProfile: &seccomp,
				Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{dropAllCapability}},
			},
		}},
	}
}
