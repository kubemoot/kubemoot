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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const ragSourceFinalizer = "kubemoot.ai/ragsource-finalizer"

// RAGSourceReconciler reconciles a RAGSource object
type RAGSourceReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ConfigCache *ConfigCache
	HTTPClient  *http.Client
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=ragsources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=ragsources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=ragsources/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles RAGSource reconciliation
func (r *RAGSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the RAGSource instance
	ragSource := &kubemootv1alpha1.RAGSource{}
	if err := r.Get(ctx, req.NamespacedName, ragSource); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling RAGSource", "name", ragSource.Name)

	// Handle deletion
	if !ragSource.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, ragSource)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(ragSource, ragSourceFinalizer) {
		if err := addFinalizer(ctx, r.Client, ragSource, ragSourceFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate referenced EmbeddingModel exists and is ready
	embeddingModel, resolveErr := r.resolveEmbeddingModel(ctx, ragSource)
	if resolveErr != nil {
		return r.updateStatus(ctx, ragSource, "Error", false, resolveErr.Error())
	}
	if embeddingModel == nil {
		return r.updateStatus(ctx, ragSource, "Pending", false,
			fmt.Sprintf("Waiting for EmbeddingModel '%s' to be ready", ragSource.Spec.EmbeddingModelRef))
	}

	// Validate cron schedule if specified
	if ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.Schedule != "" {
		if _, err := r.calculateNextRunTime(ragSource.Spec.Indexer.Schedule, time.Now()); err != nil {
			return r.updateStatus(ctx, ragSource, "Error", false,
				fmt.Sprintf("Invalid cron schedule: %s", err.Error()))
		}
	}

	// Ensure query service is deployed (if enabled)
	if queryServiceEnabled(ragSource) {
		if err := r.ensureQueryService(ctx, ragSource, embeddingModel); err != nil {
			log.Error(err, "Failed to ensure query service")
			// Don't fail reconciliation for query service issues
		}
	}

	return r.reconcileIndexingState(ctx, ragSource, embeddingModel)
}

// reconcileIndexingState handles delay checking, indexing triggers, job monitoring, and vector store verification.
func (r *RAGSourceReconciler) reconcileIndexingState(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource, embeddingModel *kubemootv1alpha1.EmbeddingModel) (ctrl.Result, error) {
	// Check if we're in the initial delay period
	delayActive, remaining, err := r.checkDelayPeriod(ctx, ragSource)
	if err != nil {
		return r.updateStatus(ctx, ragSource, "Error", false, err.Error())
	}
	if delayActive {
		ragSource.Status.Phase = "Pending"
		ragSource.Status.Message = fmt.Sprintf("Waiting for initial delay (until %s)", ragSource.Status.DelayUntil.Time.Format(time.RFC3339))
		if err := r.Status().Update(ctx, ragSource); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: remaining}, nil
	}

	if r.needsIndexing(ragSource) {
		if ragSource.Generation != ragSource.Status.ObservedGeneration {
			forceFullIndex(ragSource)
		}
		return r.runIndexingJob(ctx, ragSource, embeddingModel)
	}

	if ragSource.Status.LastJobName != "" && ragSource.Status.Phase == "Indexing" {
		return r.checkJobStatus(ctx, ragSource)
	}

	if ragSource.Status.QueryEndpoint != "" && r.verifyVectorStore(ctx, ragSource) {
		forceFullIndex(ragSource)
		return r.runIndexingJob(ctx, ragSource, embeddingModel)
	}

	return r.updateStatus(ctx, ragSource, "Ready", true,
		fmt.Sprintf("Indexed %d documents", ragSource.Status.IndexingStats.DocumentCount))
}

// resolveEmbeddingModel finds the EmbeddingModel by name in the same namespace, falling back to cluster-wide.
// Returns (model, nil) when found and ready, (nil, nil) when found but not ready, or (nil, error) when not found.
func (r *RAGSourceReconciler) resolveEmbeddingModel(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource) (*kubemootv1alpha1.EmbeddingModel, error) {
	embeddingModel := &kubemootv1alpha1.EmbeddingModel{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      ragSource.Spec.EmbeddingModelRef,
		Namespace: ragSource.Namespace,
	}, embeddingModel)

	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, err
		}
		// Fall back: search all namespaces
		embeddingModel = r.findEmbeddingModelClusterWide(ragSource.Spec.EmbeddingModelRef)
		if embeddingModel == nil {
			return nil, fmt.Errorf("EmbeddingModel '%s' not found in namespace '%s' or cluster-wide",
				ragSource.Spec.EmbeddingModelRef, ragSource.Namespace)
		}
	}

	if !embeddingModel.Status.Ready {
		return nil, nil // found but not ready
	}
	return embeddingModel, nil
}

// findEmbeddingModelClusterWide searches all namespaces for an EmbeddingModel by name.
func (r *RAGSourceReconciler) findEmbeddingModelClusterWide(name string) *kubemootv1alpha1.EmbeddingModel {
	list := &kubemootv1alpha1.EmbeddingModelList{}
	if err := r.List(context.Background(), list); err != nil {
		return nil
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return &list.Items[i]
		}
	}
	return nil
}

// forceFullIndex makes the next indexing job index everything instead of
// skipping on an unchanged source checksum. A changed spec (a new collection or
// key) or a vector store that lost its data needs the documents written again
// even when the source content is the same.
func forceFullIndex(ragSource *kubemootv1alpha1.RAGSource) {
	ragSource.Status.LastIndexedChecksum = ""
}

// needsIndexing determines if indexing should run
func (r *RAGSourceReconciler) needsIndexing(ragSource *kubemootv1alpha1.RAGSource) bool {
	// Check if currently indexing
	if ragSource.Status.Phase == "Indexing" {
		return false
	}

	// Always index if never indexed (after delay check)
	if ragSource.Status.IndexingStats == nil || ragSource.Status.IndexingStats.LastIndexed == nil {
		return true
	}

	// Re-index when spec changes (generation incremented by K8s on spec update)
	if ragSource.Generation != ragSource.Status.ObservedGeneration {
		return true
	}

	// Check schedule if defined
	if ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.Schedule != "" && ragSource.Status.NextIndexTime != nil {
		return time.Now().After(ragSource.Status.NextIndexTime.Time)
	}

	return false
}

// verifyVectorStore probes the query service /info endpoint to detect data loss.
// Returns true if data loss was detected and re-indexing should be triggered.
// On any error (connection refused, timeout, etc.), returns false to avoid false re-indexes.
// The query service is the source of truth — status.indexingStats.documentCount may be 0
// if the indexer didn't annotate the job, so we always probe when a query endpoint exists.
func (r *RAGSourceReconciler) verifyVectorStore(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource) bool {
	log := logf.FromContext(ctx)

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}

	infoURL := ragSource.Status.QueryEndpoint + "/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL, nil)
	if err != nil {
		log.V(1).Info("Failed to create info request", "url", infoURL, "error", err)
		return false
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.V(1).Info("Query service unreachable, skipping vector store verification", "url", infoURL, "error", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.V(1).Info("Query service returned non-200, skipping verification", "status", resp.StatusCode)
		return false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.V(1).Info("Failed to read info response", "error", err)
		return false
	}

	var infoResp struct {
		Stats struct {
			DocumentCount *int32 `json:"document_count"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(body, &infoResp); err != nil {
		log.V(1).Info("Failed to parse info response", "error", err)
		return false
	}

	actualCount := int32(0)
	if infoResp.Stats.DocumentCount != nil {
		actualCount = *infoResp.Stats.DocumentCount
	}

	// If query service confirms 0 documents, data was lost — trigger re-index.
	// Compare against status count if available, otherwise any 0 after a
	// successful indexing run (Phase=Ready) indicates data loss.
	expectedCount := int32(0)
	if ragSource.Status.IndexingStats != nil {
		expectedCount = ragSource.Status.IndexingStats.DocumentCount
	}

	if actualCount == 0 {
		log.Info("Vector store data loss detected, triggering re-index",
			"name", ragSource.Name,
			"expectedDocuments", expectedCount,
			"actualDocuments", actualCount)

		// Reset indexing state to trigger re-indexing
		if ragSource.Status.IndexingStats != nil {
			ragSource.Status.IndexingStats.LastIndexed = nil
		}
		ragSource.Status.ObservedGeneration = 0
		return true
	}

	return false
}

// checkDelayPeriod checks if we're still in the initial delay period
// Returns true if delay is still active (should not index yet)
// Also sets DelayUntil in status if not already set
func (r *RAGSourceReconciler) checkDelayPeriod(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource) (bool, time.Duration, error) {
	log := logf.FromContext(ctx)

	// No delay configured
	if ragSource.Spec.Indexer == nil || ragSource.Spec.Indexer.Delay == "" {
		return false, 0, nil
	}

	// If we've already indexed once, delay doesn't apply anymore
	if ragSource.Status.IndexingStats != nil && ragSource.Status.IndexingStats.LastIndexed != nil {
		return false, 0, nil
	}

	// Parse the delay duration
	delay, err := parseDelay(ragSource.Spec.Indexer.Delay)
	if err != nil {
		return false, 0, fmt.Errorf("invalid delay format: %w", err)
	}

	if delay == 0 {
		return false, 0, nil
	}

	// Calculate DelayUntil if not already set
	if ragSource.Status.DelayUntil == nil {
		delayUntil := metav1.NewTime(ragSource.CreationTimestamp.Add(delay))
		ragSource.Status.DelayUntil = &delayUntil
		log.Info("Set delay period", "delayUntil", delayUntil.Time)
	}

	// Check if delay period has elapsed
	remaining := time.Until(ragSource.Status.DelayUntil.Time)
	if remaining > 0 {
		log.Info("Delay period active", "remaining", remaining.Round(time.Second))
		return true, remaining, nil
	}

	return false, 0, nil
}

// parseDelay parses a duration string in Unix sleep format (e.g., "30s", "5m", "2h", "1d")
func parseDelay(delay string) (time.Duration, error) {
	if delay == "" {
		return 0, nil
	}

	// Match pattern: number followed by s/m/h/d
	re := regexp.MustCompile(`^(\d+)([smhd])$`)
	matches := re.FindStringSubmatch(delay)
	if matches == nil {
		return 0, fmt.Errorf("invalid duration format '%s', expected format like '30s', '5m', '2h', '1d'", delay)
	}

	value, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration value: %w", err)
	}

	unit := matches[2]
	switch unit {
	case "s":
		return time.Duration(value) * time.Second, nil
	case "m":
		return time.Duration(value) * time.Minute, nil
	case "h":
		return time.Duration(value) * time.Hour, nil
	case "d":
		return time.Duration(value) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown duration unit '%s'", unit)
	}
}

// getIndexerImage returns the indexer image to use
// Priority: RAGSource.Spec.Indexer.Image > KubemootConfig.Spec.Images.Indexer > fallback
func (r *RAGSourceReconciler) getIndexerImage(ragSource *kubemootv1alpha1.RAGSource) string {
	// Per-resource override takes priority
	if ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.Image != "" {
		return ragSource.Spec.Indexer.Image
	}
	// Fall back to KubemootConfig (or its fallback)
	return r.ConfigCache.GetIndexerImage()
}

// runIndexingJob creates and runs the indexing job
func (r *RAGSourceReconciler) runIndexingJob(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource, embeddingModel *kubemootv1alpha1.EmbeddingModel) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check if there's already an active job for this RAGSource
	existingJobs := &batchv1.JobList{}
	if err := r.List(ctx, existingJobs,
		client.InNamespace(ragSource.Namespace),
		client.MatchingLabels{labelRAGSource: ragSource.Name}); err != nil {
		return ctrl.Result{}, err
	}

	// Check for an active (running) job — don't create a new one if found
	if activeJob := findActiveJob(existingJobs); activeJob != nil {
		log.Info("Found active indexing job, waiting for completion", "job", activeJob.Name)
		ragSource.Status.LastJobName = activeJob.Name
		ragSource.Status.Phase = "Indexing"
		ragSource.Status.Message = fmt.Sprintf("Indexing job %s in progress", activeJob.Name)
		if err := r.Status().Update(ctx, ragSource); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Generate job name
	jobName := fmt.Sprintf("%s-indexer-%d", ragSource.Name, time.Now().Unix())

	// The indexer annotates its own Job with its results; the default ServiceAccount
	// may not, so it runs as one allowed to.
	if !hasIndexerServiceAccount(ragSource) {
		if err := ensureJobRBAC(ctx, r.Client, ragSource.Namespace, componentRAGIndexer, ragIndexerJobVerbs); err != nil {
			return r.updateStatus(ctx, ragSource, "Error", false, fmt.Sprintf("Failed to prepare the indexer's ServiceAccount: %v", err))
		}
	}

	// Build the job
	job := r.buildIndexingJob(ragSource, embeddingModel, jobName)

	// Set owner reference
	if err := controllerutil.SetControllerReference(ragSource, job, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	// Create the job
	log.Info("Creating indexing job", "job", jobName)
	if err := r.Create(ctx, job); err != nil {
		if errors.IsAlreadyExists(err) {
			// Job already exists, check its status
			ragSource.Status.LastJobName = jobName
			return r.checkJobStatus(ctx, ragSource)
		}
		return r.updateStatus(ctx, ragSource, "Error", false,
			fmt.Sprintf("Failed to create indexing job: %v", err))
	}

	// Update status to indexing
	ragSource.Status.Phase = "Indexing"
	ragSource.Status.Ready = false
	ragSource.Status.LastJobName = jobName
	ragSource.Status.Message = fmt.Sprintf("Indexing job %s started", jobName)

	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "Indexing",
		Message:            ragSource.Status.Message,
		LastTransitionTime: metav1.Now(),
	}
	meta.SetStatusCondition(&ragSource.Status.Conditions, condition)

	if err := r.Status().Update(ctx, ragSource); err != nil {
		log.Error(err, "Failed to update RAGSource status")
		return ctrl.Result{}, err
	}

	// Requeue to check job status
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// findActiveJob returns the first active (not complete, not failed) job from the list, or nil.
func findActiveJob(jobList *batchv1.JobList) *batchv1.Job {
	for i := range jobList.Items {
		job := &jobList.Items[i]
		isComplete, isFailed := jobConditions(job)
		if !isComplete && !isFailed {
			return job
		}
	}
	return nil
}

// hasIndexerServiceAccount reports whether the RAGSource names its own indexer ServiceAccount.
func hasIndexerServiceAccount(ragSource *kubemootv1alpha1.RAGSource) bool {
	return ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.ServiceAccountName != ""
}

// buildIndexingJob creates the Job spec for indexing
func (r *RAGSourceReconciler) buildIndexingJob(ragSource *kubemootv1alpha1.RAGSource, embeddingModel *kubemootv1alpha1.EmbeddingModel, jobName string) *batchv1.Job {
	labels := map[string]string{
		labelName:      ragSource.Name,
		labelInstance:  jobName,
		labelManagedBy: managedByValue,
		labelComponent: "indexer",
		labelRAGSource: ragSource.Name,
	}

	// Build environment variables for the indexer
	env := r.buildIndexerEnv(ragSource, embeddingModel)

	// Add JOB_NAME and POD_NAMESPACE for the indexer to annotate its own Job
	env = append(env,
		corev1.EnvVar{Name: "JOB_NAME", Value: jobName},
		corev1.EnvVar{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fieldPathNamespace,
				},
			},
		},
		corev1.EnvVar{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
	)

	// Add any user-specified env vars
	if ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.Env != nil {
		env = append(env, ragSource.Spec.Indexer.Env...)
	}

	// Default resource requirements
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}
	if ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.Resources != nil {
		resources = *ragSource.Spec.Indexer.Resources
	}

	backoffLimit := int32(3)
	ttlSeconds := int32(3600) // Clean up completed jobs after 1 hour

	if ragSource.Spec.Indexer != nil {
		if ragSource.Spec.Indexer.BackoffLimit != nil {
			backoffLimit = *ragSource.Spec.Indexer.BackoffLimit
		}
		if ragSource.Spec.Indexer.TTLSecondsAfterFinished != nil {
			ttlSeconds = *ragSource.Spec.Indexer.TTLSecondsAfterFinished
		}
	}

	// Build container spec
	container := corev1.Container{
		Name:      "indexer",
		Image:     r.getIndexerImage(ragSource),
		Env:       env,
		Resources: resources,
	}

	// Build volumes and volume mounts for script injection
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	scriptVolumes, scriptMounts, scriptEnv := buildIndexerScriptVolumes(ragSource)
	volumes = append(volumes, scriptVolumes...)
	volumeMounts = append(volumeMounts, scriptMounts...)
	env = append(env, scriptEnv...)

	// Add writable work directory for git clones and temp files
	volumes = append(volumes, corev1.Volume{
		Name: "work",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "work",
		MountPath: "/data",
	})

	// Set HOME to writable dir so JGit can create .config/jgit/config
	env = append(env, corev1.EnvVar{Name: "HOME", Value: "/data"})

	container.Env = env
	container.VolumeMounts = volumeMounts

	// Security context for restricted PSS compliance
	runAsNonRoot := true
	allowPrivilegeEscalation := false
	runAsUser := int64(1000)
	runAsGroup := int64(1000)
	seccompProfile := corev1.SeccompProfile{
		Type: corev1.SeccompProfileTypeRuntimeDefault,
	}
	container.SecurityContext = &corev1.SecurityContext{
		RunAsNonRoot:             &runAsNonRoot,
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		RunAsUser:                &runAsUser,
		RunAsGroup:               &runAsGroup,
		SeccompProfile:           &seccompProfile,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ragSource.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:    corev1.RestartPolicyNever,
					Containers:       []corev1.Container{container},
					Volumes:          volumes,
					ImagePullSecrets: r.ConfigCache.GetImagePullSecrets(),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						SeccompProfile: &seccompProfile,
					},
				},
			},
		},
	}

	job.Spec.Template.Spec.ServiceAccountName = componentRAGIndexer
	if hasIndexerServiceAccount(ragSource) {
		job.Spec.Template.Spec.ServiceAccountName = ragSource.Spec.Indexer.ServiceAccountName
	}

	// Inject docling-serve native sidecar for document source type
	if ragSource.Spec.Source.Type == kubemootv1alpha1.RAGSourceTypeDocument {
		restartAlways := corev1.ContainerRestartPolicyAlways
		doclingInit := corev1.Container{
			Name:          "docling-serve",
			Image:         r.ConfigCache.GetDoclingServeImage(),
			RestartPolicy: &restartAlways,
			Ports:         []corev1.ContainerPort{{ContainerPort: 5001}},
			Env: []corev1.EnvVar{
				{Name: "UVICORN_WORKERS", Value: "1"},
				{Name: "OMP_NUM_THREADS", Value: "2"},
			},
			StartupProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt32(5001),
					},
				},
				InitialDelaySeconds: 10,
				PeriodSeconds:       5,
				FailureThreshold:    30, // up to ~160s for model loading
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
			},
		}
		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, doclingInit)
		// Relax pod RunAsNonRoot since docling-serve needs root
		job.Spec.Template.Spec.SecurityContext.RunAsNonRoot = nil
	}

	return job
}

// buildIndexerEnv creates environment variables for the indexer container.
// Dispatches to source-specific builders for each RAGSource type.
func (r *RAGSourceReconciler) buildIndexerEnv(ragSource *kubemootv1alpha1.RAGSource, embeddingModel *kubemootv1alpha1.EmbeddingModel) []corev1.EnvVar {
	var env []corev1.EnvVar
	env = append(env, r.buildIndexerCoreEnv(ragSource, embeddingModel)...)
	env = append(env, buildSourceTypeEnv(ragSource)...)
	env = append(env, buildChunkingEnv(ragSource)...)
	env = append(env, buildVectorStoreSecretEnv(ragSource)...)
	env = append(env, buildSourceSecretEnv(ragSource)...)
	return env
}

// buildIndexerCoreEnv builds common env vars: source type, vector store, embedding, checksum, force-reindex.
func (r *RAGSourceReconciler) buildIndexerCoreEnv(ragSource *kubemootv1alpha1.RAGSource, embeddingModel *kubemootv1alpha1.EmbeddingModel) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "KUBEMOOT_SOURCE_TYPE", Value: string(ragSource.Spec.Source.Type)},
		{Name: "KUBEMOOT_VECTORSTORE_TYPE", Value: string(ragSource.Spec.VectorStore.Type)},
		{Name: "KUBEMOOT_VECTORSTORE_ENDPOINT", Value: r.buildJDBCUrl(ragSource.Spec.VectorStore.Endpoint)},
		{Name: "KUBEMOOT_VECTORSTORE_COLLECTION", Value: ragSource.Spec.VectorStore.Collection},
		{Name: "KUBEMOOT_VECTORSTORE_DIMENSIONS", Value: fmt.Sprintf("%d", ragSource.Spec.VectorStore.Dimensions)},
		{Name: "KUBEMOOT_EMBEDDING_MODEL", Value: embeddingModel.Spec.Model},
	}

	if ragSource.Status.LastIndexedChecksum != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_LAST_CHECKSUM", Value: ragSource.Status.LastIndexedChecksum})
	}

	forceReindex := "false"
	if ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.ForceReindex {
		forceReindex = "true"
	}
	env = append(env, corev1.EnvVar{Name: "KUBEMOOT_FORCE_REINDEX", Value: forceReindex})

	if embeddingModel.Status.Endpoint != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_EMBEDDING_ENDPOINT", Value: embeddingModel.Status.Endpoint})
	}

	return env
}

// buildSourceTypeEnv dispatches to the appropriate source-specific env builder.
func buildSourceTypeEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	switch ragSource.Spec.Source.Type {
	case kubemootv1alpha1.RAGSourceTypeGit:
		return buildGitSourceEnv(ragSource)
	case kubemootv1alpha1.RAGSourceTypeS3:
		return buildS3SourceEnv(ragSource)
	case kubemootv1alpha1.RAGSourceTypeURL:
		return buildURLSourceEnv(ragSource)
	case kubemootv1alpha1.RAGSourceTypeMCPRegistry:
		return buildMCPRegistrySourceEnv(ragSource)
	case kubemootv1alpha1.RAGSourceTypeDocument:
		return buildDocumentSourceEnv(ragSource)
	case kubemootv1alpha1.RAGSourceTypeNatsKV:
		return buildNatsKVSourceEnv(ragSource)
	default:
		return nil
	}
}

// buildIndexerScriptVolumes creates volumes, mounts, and env vars for script injection.
func buildIndexerScriptVolumes(ragSource *kubemootv1alpha1.RAGSource) ([]corev1.Volume, []corev1.VolumeMount, []corev1.EnvVar) {
	if ragSource.Spec.Indexer == nil || ragSource.Spec.Indexer.Script == nil {
		return nil, nil, nil
	}

	script := ragSource.Spec.Indexer.Script
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	var env []corev1.EnvVar

	if script.ConfigMapRef != nil {
		scriptKey := "indexer.py"
		if script.ScriptKey != "" {
			scriptKey = script.ScriptKey
		}

		volumes = append(volumes, corev1.Volume{
			Name: "script",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: *script.ConfigMapRef,
					Items: []corev1.KeyToPath{
						{Key: scriptKey, Path: scriptKey},
					},
					DefaultMode: func() *int32 { mode := int32(0755); return &mode }(),
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "script",
			MountPath: "/app/scripts",
			ReadOnly:  true,
		})
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_SCRIPT_KEY", Value: scriptKey})
	}

	if script.Inline != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_INLINE_SCRIPT", Value: script.Inline})
	}

	return volumes, mounts, env
}

// buildGitSourceEnv builds env vars for git source type.
func buildGitSourceEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	if ragSource.Spec.Source.Git == nil {
		return nil
	}
	env := []corev1.EnvVar{
		{Name: "KUBEMOOT_GIT_URL", Value: ragSource.Spec.Source.Git.URL},
		{Name: "KUBEMOOT_GIT_BRANCH", Value: ragSource.Spec.Source.Git.Branch},
	}
	if len(ragSource.Spec.Source.Git.Paths) > 0 {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_GIT_PATHS", Value: strings.Join(ragSource.Spec.Source.Git.Paths, ",")})
	}
	return env
}

// buildS3SourceEnv builds env vars for S3 source type.
func buildS3SourceEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	if ragSource.Spec.Source.S3 == nil {
		return nil
	}
	env := []corev1.EnvVar{
		{Name: "KUBEMOOT_S3_BUCKET", Value: ragSource.Spec.Source.S3.Bucket},
		{Name: "KUBEMOOT_S3_PREFIX", Value: ragSource.Spec.Source.S3.Prefix},
		{Name: "KUBEMOOT_S3_REGION", Value: ragSource.Spec.Source.S3.Region},
	}
	if ragSource.Spec.Source.S3.Endpoint != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_S3_ENDPOINT", Value: ragSource.Spec.Source.S3.Endpoint})
	}
	return env
}

// buildURLSourceEnv builds env vars for URL source type.
func buildURLSourceEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	if ragSource.Spec.Source.URL == nil || len(ragSource.Spec.Source.URL.URLs) == 0 {
		return nil
	}
	return []corev1.EnvVar{
		{Name: "KUBEMOOT_URL", Value: ragSource.Spec.Source.URL.URLs[0]},
	}
}

// buildMCPRegistrySourceEnv builds env vars for MCP registry source type.
func buildMCPRegistrySourceEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	if ragSource.Spec.Source.MCPRegistry == nil {
		return nil
	}
	registry := ragSource.Spec.Source.MCPRegistry

	env := []corev1.EnvVar{
		{Name: "KUBEMOOT_MCP_REGISTRY_URL", Value: registry.URL},
	}

	registryType := "mcp-run"
	if registry.Type != "" {
		registryType = registry.Type
	}
	env = append(env, corev1.EnvVar{Name: "KUBEMOOT_MCP_REGISTRY_TYPE", Value: registryType})

	if registry.Filter != nil {
		if len(registry.Filter.Categories) > 0 {
			env = append(env, corev1.EnvVar{Name: "KUBEMOOT_MCP_REGISTRY_CATEGORIES", Value: strings.Join(registry.Filter.Categories, ",")})
		}
		if registry.Filter.MinRating != "" {
			env = append(env, corev1.EnvVar{Name: "KUBEMOOT_MCP_REGISTRY_MIN_RATING", Value: registry.Filter.MinRating})
		}
	}

	if registry.AuthSecretRef != "" {
		env = append(env, corev1.EnvVar{
			Name: "KUBEMOOT_MCP_REGISTRY_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: registry.AuthSecretRef,
					},
					Key: "token",
				},
			},
		})
	}

	return env
}

// buildDocumentSourceEnv builds env vars for document source type.
func buildDocumentSourceEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	if ragSource.Spec.Source.Document == nil {
		return nil
	}
	env := []corev1.EnvVar{
		{Name: "KUBEMOOT_DOCUMENT_URLS", Value: strings.Join(ragSource.Spec.Source.Document.URLs, ",")},
		{Name: "KUBEMOOT_DOCLING_ENDPOINT", Value: "http://localhost:5001"},
	}
	if ragSource.Spec.Source.Document.DisableOCR {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_DOCUMENT_DISABLE_OCR", Value: "true"})
	}
	return env
}

// buildNatsKVSourceEnv builds env vars for NATS KV source type.
func buildNatsKVSourceEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	if ragSource.Spec.Source.NatsKV == nil {
		return nil
	}
	env := []corev1.EnvVar{
		{Name: "KUBEMOOT_NATS_KV_BUCKET", Value: ragSource.Spec.Source.NatsKV.Bucket},
		{Name: "KUBEMOOT_NATS_KV_KEY", Value: ragSource.Spec.Source.NatsKV.Key},
		{Name: "NATS_URL", Value: os.Getenv("NATS_URL")},
		// Resumes are a full replacement — truncate old embeddings before re-indexing
		{Name: "KUBEMOOT_TRUNCATE_BEFORE_INDEX", Value: "true"},
	}
	if ragSource.Spec.Source.NatsKV.ContentHash != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_NATS_KV_CONTENT_HASH", Value: ragSource.Spec.Source.NatsKV.ContentHash})
	}
	return env
}

// buildChunkingEnv builds env vars for chunking configuration.
func buildChunkingEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	if ragSource.Spec.Chunking == nil {
		return nil
	}
	return []corev1.EnvVar{
		{Name: "KUBEMOOT_CHUNK_SIZE", Value: fmt.Sprintf("%d", ragSource.Spec.Chunking.ChunkSize)},
		{Name: "KUBEMOOT_CHUNK_OVERLAP", Value: fmt.Sprintf("%d", ragSource.Spec.Chunking.ChunkOverlap)},
	}
}

// buildVectorStoreSecretEnv builds env vars for vector store credentials.
func buildVectorStoreSecretEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	if ragSource.Spec.VectorStore.SecretRef == "" {
		return nil
	}
	return []corev1.EnvVar{
		{
			Name: "KUBEMOOT_VECTORSTORE_USER",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ragSource.Spec.VectorStore.SecretRef,
					},
					Key:      "username",
					Optional: ptr.To(true),
				},
			},
		},
		{
			Name: "KUBEMOOT_VECTORSTORE_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ragSource.Spec.VectorStore.SecretRef,
					},
					Key: "password",
				},
			},
		},
	}
}

// buildSourceSecretEnv builds env vars for source-specific credentials (git token, S3 keys).
func buildSourceSecretEnv(ragSource *kubemootv1alpha1.RAGSource) []corev1.EnvVar {
	var env []corev1.EnvVar

	if ragSource.Spec.Source.Type == kubemootv1alpha1.RAGSourceTypeGit && ragSource.Spec.Source.Git != nil && ragSource.Spec.Source.Git.SecretRef != "" {
		env = append(env, corev1.EnvVar{
			Name: "KUBEMOOT_GIT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ragSource.Spec.Source.Git.SecretRef,
					},
					Key: "token",
				},
			},
		})
	}

	if ragSource.Spec.Source.Type == kubemootv1alpha1.RAGSourceTypeS3 && ragSource.Spec.Source.S3 != nil && ragSource.Spec.Source.S3.SecretRef != "" {
		env = append(env,
			corev1.EnvVar{
				Name: "AWS_ACCESS_KEY_ID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: ragSource.Spec.Source.S3.SecretRef,
						},
						Key: "access-key-id",
					},
				},
			},
			corev1.EnvVar{
				Name: "AWS_SECRET_ACCESS_KEY",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: ragSource.Spec.Source.S3.SecretRef,
						},
						Key: "secret-access-key",
					},
				},
			},
		)
	}

	return env
}

// checkJobStatus checks the status of the indexing job
func (r *RAGSourceReconciler) checkJobStatus(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	job := &batchv1.Job{}
	jobName := types.NamespacedName{
		Name:      ragSource.Status.LastJobName,
		Namespace: ragSource.Namespace,
	}

	if err := r.Get(ctx, jobName, job); err != nil {
		if errors.IsNotFound(err) {
			// Job was deleted, trigger re-indexing
			ragSource.Status.LastJobName = ""
			return r.updateStatus(ctx, ragSource, "Pending", false, "Previous indexing job not found")
		}
		return ctrl.Result{}, err
	}

	// Check job conditions
	for _, condition := range job.Status.Conditions {
		switch condition.Type {
		case batchv1.JobComplete:
			if condition.Status == corev1.ConditionTrue {
				log.Info("Indexing job completed successfully", "job", job.Name)
				return r.handleJobSuccess(ctx, ragSource, job)
			}
		case batchv1.JobFailed:
			if condition.Status == corev1.ConditionTrue {
				log.Info("Indexing job failed", "job", job.Name, "reason", condition.Reason)
				return r.updateStatus(ctx, ragSource, "Error", false,
					fmt.Sprintf("Indexing job failed: %s", condition.Message))
			}
		}
	}

	// Job still running
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleJobSuccess handles successful job completion
func (r *RAGSourceReconciler) handleJobSuccess(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource, job *batchv1.Job) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	now := metav1.Now()

	// Calculate duration
	var duration string
	if job.Status.StartTime != nil && job.Status.CompletionTime != nil {
		d := job.Status.CompletionTime.Sub(job.Status.StartTime.Time)
		duration = d.Round(time.Second).String()
	}

	jobStatus, checksum, documentCount, chunkCount := parseJobAnnotations(job)

	if topics := job.Annotations["kubemoot.ai/topics"]; topics != "" {
		ragSource.Status.Topics = strings.Split(topics, ",")
		log.Info("Read topics from indexer", "topics", ragSource.Status.Topics)
	}

	// Handle "unchanged" status - source hasn't changed, no indexing performed
	if jobStatus == "unchanged" {
		log.Info("Source unchanged, indexing skipped")
		return r.handleIndexingSkipped(ctx, ragSource, now)
	}

	// Update checksum if provided (for "indexed" status)
	if checksum != "" {
		ragSource.Status.LastIndexedChecksum = checksum
		log.Info("Updated source checksum", "checksum", checksum)
	}

	// Update indexing stats
	ragSource.Status.IndexingStats = &kubemootv1alpha1.IndexingStats{
		LastIndexed:   &now,
		Duration:      duration,
		DocumentCount: documentCount,
		ChunkCount:    chunkCount,
	}

	// Calculate next index time if schedule is set
	var statusMessage string
	if ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.Schedule != "" {
		nextTime, err := r.calculateNextRunTime(ragSource.Spec.Indexer.Schedule, now.Time)
		if err != nil {
			log.Error(err, "Failed to parse cron schedule", "schedule", ragSource.Spec.Indexer.Schedule)
			// Fall back to 24 hours if parsing fails
			nextTime = now.Add(24 * time.Hour)
		}
		nextMetaTime := metav1.NewTime(nextTime)
		ragSource.Status.NextIndexTime = &nextMetaTime
		log.Info("Scheduled next indexing run", "nextTime", nextTime)
		statusMessage = fmt.Sprintf("Indexed %d documents (%d chunks) in %s. Next run: %s", documentCount, chunkCount, duration, nextTime.Format(time.RFC3339))
	} else {
		statusMessage = fmt.Sprintf("Indexed %d documents (%d chunks) in %s", documentCount, chunkCount, duration)
	}

	// Record observed generation so spec changes trigger re-indexing
	ragSource.Status.ObservedGeneration = ragSource.Generation

	// Clear LastJobName so completed jobs aren't re-processed on next reconcile
	ragSource.Status.LastJobName = ""

	return r.updateStatus(ctx, ragSource, "Ready", true, statusMessage)
}

// parseJobAnnotations extracts indexing status, checksum, document count, and chunk count from job annotations.
func parseJobAnnotations(job *batchv1.Job) (string, string, int32, int32) {
	if job.Annotations == nil {
		return "", "", 0, 0
	}
	jobStatus := job.Annotations["kubemoot.ai/status"]
	checksum := job.Annotations["kubemoot.ai/checksum"]
	var documentCount, chunkCount int32
	if docStr := job.Annotations["kubemoot.ai/document-count"]; docStr != "" {
		if count, err := strconv.ParseInt(docStr, 10, 32); err == nil {
			documentCount = int32(count)
		}
	}
	if chunkStr := job.Annotations["kubemoot.ai/chunk-count"]; chunkStr != "" {
		if count, err := strconv.ParseInt(chunkStr, 10, 32); err == nil {
			chunkCount = int32(count)
		}
	}
	return jobStatus, checksum, documentCount, chunkCount
}

// handleIndexingSkipped handles the case where indexing was skipped due to unchanged source
func (r *RAGSourceReconciler) handleIndexingSkipped(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource, now metav1.Time) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Update indexing stats to reflect the skip (preserve previous counts)
	if ragSource.Status.IndexingStats == nil {
		ragSource.Status.IndexingStats = &kubemootv1alpha1.IndexingStats{}
	}
	ragSource.Status.IndexingStats.LastIndexed = &now
	ragSource.Status.IndexingStats.Duration = "0s (skipped)"

	// Calculate next index time if schedule is set
	var statusMessage string
	if ragSource.Spec.Indexer != nil && ragSource.Spec.Indexer.Schedule != "" {
		nextTime, err := r.calculateNextRunTime(ragSource.Spec.Indexer.Schedule, now.Time)
		if err != nil {
			log.Error(err, "Failed to parse cron schedule", "schedule", ragSource.Spec.Indexer.Schedule)
			nextTime = now.Add(24 * time.Hour)
		}
		nextMetaTime := metav1.NewTime(nextTime)
		ragSource.Status.NextIndexTime = &nextMetaTime
		statusMessage = fmt.Sprintf("Source unchanged, skipped indexing. Next check: %s", nextTime.Format(time.RFC3339))
	} else {
		statusMessage = "Source unchanged, skipped indexing"
	}

	// Record observed generation so spec changes trigger re-indexing
	ragSource.Status.ObservedGeneration = ragSource.Generation

	// Clear LastJobName so completed jobs aren't re-processed on next reconcile
	ragSource.Status.LastJobName = ""

	return r.updateStatus(ctx, ragSource, "Ready", true, statusMessage)
}

// calculateNextRunTime calculates the next run time based on cron schedule
func (r *RAGSourceReconciler) calculateNextRunTime(schedule string, now time.Time) (time.Time, error) {
	// Create a cron parser with standard cron format (minute, hour, day, month, weekday)
	// Also supports predefined schedules like @hourly, @daily, @weekly, @monthly
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

	sched, err := parser.Parse(schedule)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron schedule '%s': %w", schedule, err)
	}

	return sched.Next(now), nil
}

// handleDeletion handles RAGSource deletion
func (r *RAGSourceReconciler) handleDeletion(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(ragSource, ragSourceFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Handling RAGSource deletion", "name", ragSource.Name)

	// Delete any running indexing jobs
	if ragSource.Status.LastJobName != "" {
		job := &batchv1.Job{}
		jobName := types.NamespacedName{
			Name:      ragSource.Status.LastJobName,
			Namespace: ragSource.Namespace,
		}
		if err := r.Get(ctx, jobName, job); err == nil {
			propagationPolicy := metav1.DeletePropagationBackground
			if err := r.Delete(ctx, job, &client.DeleteOptions{
				PropagationPolicy: &propagationPolicy,
			}); err != nil && !errors.IsNotFound(err) {
				log.Error(err, "Failed to delete indexing job")
			}
		}
	}

	// Note: We don't delete vectors from the vector store
	// That's the responsibility of the admin

	// Remove finalizer
	if err := removeFinalizer(ctx, r.Client, ragSource, ragSourceFinalizer); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("RAGSource deleted successfully", "name", ragSource.Name)
	return ctrl.Result{}, nil
}

// updateStatus updates the RAGSource status
func (r *RAGSourceReconciler) updateStatus(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource, phase string, ready bool, message string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ragSource.Status.Phase = phase
	ragSource.Status.Ready = ready
	ragSource.Status.Message = message

	// Set condition
	conditionStatus := metav1.ConditionFalse
	if ready {
		conditionStatus = metav1.ConditionTrue
	}
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             conditionStatus,
		Reason:             phase,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	meta.SetStatusCondition(&ragSource.Status.Conditions, condition)

	if err := r.Status().Update(ctx, ragSource); err != nil {
		log.Error(err, "Failed to update RAGSource status")
		return ctrl.Result{}, err
	}

	// Requeue based on phase
	switch phase {
	case "Indexing":
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	case "Error":
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	case "Pending":
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	default:
		// Ready - requeue based on schedule or default
		if ragSource.Status.NextIndexTime != nil {
			return ctrl.Result{RequeueAfter: time.Until(ragSource.Status.NextIndexTime.Time)}, nil
		}
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RAGSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.RAGSource{}).
		Owns(&batchv1.Job{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("ragsource").
		// Re-reconcile every RAGSource when KubemootConfig changes so
		// indexer / queryService / doclingServe image bumps propagate
		// to the Job + Deployment specs without per-CR annotation.
		// See kubemootconfig_propagation.go.
		Watches(
			&kubemootv1alpha1.KubemootConfig{},
			enqueueAllOnKubemootConfigChange(mgr.GetClient(),
				func() client.ObjectList { return &kubemootv1alpha1.RAGSourceList{} },
				"ragsource"),
		).
		Complete(r)
}

// ============================================================================
// Query Service Management
// ============================================================================

// ensureQueryService creates or updates the query service Deployment and Service
func (r *RAGSourceReconciler) ensureQueryService(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource, embeddingModel *kubemootv1alpha1.EmbeddingModel) error {
	log := logf.FromContext(ctx)

	// Build names
	queryServiceName := fmt.Sprintf("%s-query", ragSource.Name)

	// Ensure Deployment
	if err := r.ensureQueryServiceDeployment(ctx, ragSource, embeddingModel, queryServiceName); err != nil {
		return err
	}

	// Ensure Service
	if err := r.ensureQueryServiceService(ctx, ragSource, queryServiceName); err != nil {
		return err
	}

	// Update status with query endpoint
	endpoint := ragQueryEndpoint(ragSource.Name, ragSource.Namespace, queryServicePort(ragSource))
	if ragSource.Status.QueryEndpoint != endpoint {
		ragSource.Status.QueryEndpoint = endpoint
		log.Info("Updated query endpoint", "endpoint", endpoint)
	}

	return nil
}

// ensureQueryServiceDeployment creates or updates the query service Deployment
func (r *RAGSourceReconciler) ensureQueryServiceDeployment(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource, embeddingModel *kubemootv1alpha1.EmbeddingModel, name string) error {
	log := logf.FromContext(ctx)

	deployment := &appsv1.Deployment{}
	deploymentName := types.NamespacedName{Name: name, Namespace: ragSource.Namespace}

	err := r.Get(ctx, deploymentName, deployment)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	desiredDeployment := r.buildQueryServiceDeployment(ragSource, embeddingModel, name)

	// Set owner reference
	if err := controllerutil.SetControllerReference(ragSource, desiredDeployment, r.Scheme); err != nil {
		return err
	}

	if errors.IsNotFound(err) {
		// Create new deployment with hash annotation
		log.Info("Creating query service Deployment", "deployment", name)
		if desiredDeployment.Annotations == nil {
			desiredDeployment.Annotations = make(map[string]string)
		}
		desiredDeployment.Annotations[deploymentHashAnnotation] = computeDeploymentHash(desiredDeployment)
		return r.Create(ctx, desiredDeployment)
	}

	// Update existing deployment only if spec changed (using hash annotation)
	desiredHash := computeDeploymentHash(desiredDeployment)
	currentHash := deployment.Annotations[deploymentHashAnnotation]

	if currentHash == "" {
		// Deployment exists but has no hash annotation (created before this fix or by another controller)
		// Just add the annotation without changing spec to avoid unnecessary ReplicaSet creation
		log.Info("Adding hash annotation to existing query service Deployment", "deployment", name)
		if deployment.Annotations == nil {
			deployment.Annotations = make(map[string]string)
		}
		deployment.Annotations[deploymentHashAnnotation] = desiredHash
		return r.Update(ctx, deployment)
	} else if currentHash != desiredHash {
		log.Info("Updating query service Deployment", "deployment", name, "reason", "spec changed")
		deployment.Spec = desiredDeployment.Spec
		if deployment.Annotations == nil {
			deployment.Annotations = make(map[string]string)
		}
		deployment.Annotations[deploymentHashAnnotation] = desiredHash
		return r.Update(ctx, deployment)
	}

	return nil
}

// buildQueryServiceDeployment creates the Deployment spec for the query service
func (r *RAGSourceReconciler) buildQueryServiceDeployment(ragSource *kubemootv1alpha1.RAGSource, embeddingModel *kubemootv1alpha1.EmbeddingModel, name string) *appsv1.Deployment {
	labels := map[string]string{
		labelName:      name,
		labelInstance:  name,
		labelManagedBy: managedByValue,
		labelComponent: componentQueryService,
		labelRAGSource: ragSource.Name,
	}

	replicas := r.getQueryServiceReplicas(ragSource)
	port := queryServicePort(ragSource)
	image := r.getQueryServiceImage(ragSource)
	topK := r.getQueryServiceTopK(ragSource)

	// Build environment variables
	env := []corev1.EnvVar{
		// New-style variables (preferred)
		{Name: "KUBEMOOT_RAGSOURCE_NAME", Value: ragSource.Name},
		{Name: "KUBEMOOT_RAGSOURCE_NAMESPACE", Value: ragSource.Namespace},
		{Name: "KUBEMOOT_VECTORSTORE_TYPE", Value: string(ragSource.Spec.VectorStore.Type)},
		{Name: "KUBEMOOT_VECTORSTORE_ENDPOINT", Value: ragSource.Spec.VectorStore.Endpoint},
		{Name: "KUBEMOOT_VECTORSTORE_COLLECTION", Value: ragSource.Spec.VectorStore.Collection},
		{Name: "KUBEMOOT_VECTORSTORE_DIMENSIONS", Value: fmt.Sprintf("%d", ragSource.Spec.VectorStore.Dimensions)},
		{Name: "KUBEMOOT_QUERY_PORT", Value: fmt.Sprintf("%d", port)},
		{Name: "KUBEMOOT_DEFAULT_TOP_K", Value: fmt.Sprintf("%d", topK)},
		// Legacy variables (for backwards compatibility)
		{Name: "KUBEMOOT_DB_HOST", Value: r.extractHost(ragSource.Spec.VectorStore.Endpoint)},
		{Name: "KUBEMOOT_DB_PORT", Value: r.extractPort(ragSource.Spec.VectorStore.Endpoint)},
		{Name: "KUBEMOOT_DB_NAME", Value: r.extractDBName(ragSource.Spec.VectorStore.Endpoint)},
		{Name: "KUBEMOOT_DB_USER", Value: r.extractUser(ragSource.Spec.VectorStore.Endpoint)},
		{Name: "KUBEMOOT_COLLECTION", Value: ragSource.Spec.VectorStore.Collection},
		{Name: "KUBEMOOT_TOP_K", Value: fmt.Sprintf("%d", topK)},
		{Name: "PORT", Value: fmt.Sprintf("%d", port)},
	}

	// Add embedding configuration from EmbeddingModel
	if embeddingModel.Status.Endpoint != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_EMBEDDING_ENDPOINT", Value: embeddingModel.Status.Endpoint})
	}
	env = append(env, corev1.EnvVar{Name: "KUBEMOOT_EMBEDDING_MODEL", Value: embeddingModel.Spec.Model})
	// Determine embedding type from provider (default to ollama)
	embeddingType := "ollama"
	env = append(env, corev1.EnvVar{Name: "KUBEMOOT_EMBEDDING_TYPE", Value: embeddingType})

	// Add database credentials from secret
	if ragSource.Spec.VectorStore.SecretRef != "" {
		// Override username from secret
		env = append(env, corev1.EnvVar{
			Name: "KUBEMOOT_DB_USER",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ragSource.Spec.VectorStore.SecretRef,
					},
					Key:      "username",
					Optional: ptr.To(true), // Fall back to default if not present
				},
			},
		})
		env = append(env, corev1.EnvVar{
			Name: "KUBEMOOT_DB_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ragSource.Spec.VectorStore.SecretRef,
					},
					Key: "password",
				},
			},
		})
	}

	// Default resources
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
	if ragSource.Spec.QueryService != nil && ragSource.Spec.QueryService.Resources != nil {
		resources = *ragSource.Spec.QueryService.Resources
	}

	// Security context
	runAsNonRoot := true
	allowPrivilegeEscalation := false
	runAsUser := int64(1000)
	runAsGroup := int64(1000)
	seccompProfile := corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}

	container := corev1.Container{
		Name:      componentQueryService,
		Image:     image,
		Env:       env,
		Resources: resources,
		Ports: []corev1.ContainerPort{
			{Name: "http", ContainerPort: port, Protocol: corev1.ProtocolTCP},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(port)},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       30,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt32(port)},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             &runAsNonRoot,
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			RunAsUser:                &runAsUser,
			RunAsGroup:               &runAsGroup,
			SeccompProfile:           &seccompProfile,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ragSource.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{labelRAGSource: ragSource.Name, labelComponent: componentQueryService},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers:       []corev1.Container{container},
					ImagePullSecrets: r.ConfigCache.GetImagePullSecrets(),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						SeccompProfile: &seccompProfile,
					},
				},
			},
		},
	}
}

// ensureQueryServiceService creates or updates the query service Service
func (r *RAGSourceReconciler) ensureQueryServiceService(ctx context.Context, ragSource *kubemootv1alpha1.RAGSource, name string) error {
	log := logf.FromContext(ctx)

	service := &corev1.Service{}
	serviceName := types.NamespacedName{Name: name, Namespace: ragSource.Namespace}

	err := r.Get(ctx, serviceName, service)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	port := queryServicePort(ragSource)

	desiredService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ragSource.Namespace,
			Labels: map[string]string{
				labelName:      name,
				labelManagedBy: managedByValue,
				labelComponent: componentQueryService,
				labelRAGSource: ragSource.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				labelRAGSource: ragSource.Name,
				labelComponent: componentQueryService,
			},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP},
			},
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(ragSource, desiredService, r.Scheme); err != nil {
		return err
	}

	if errors.IsNotFound(err) {
		log.Info("Creating query service Service", "service", name)
		return r.Create(ctx, desiredService)
	}

	// Update existing service
	service.Spec = desiredService.Spec
	return r.Update(ctx, service)
}

// Helper functions for query service configuration

// getQueryServiceImage returns the query service image to use
// Priority: RAGSource.Spec.QueryService.Image > KubemootConfig.Spec.Images.QueryService > fallback
func (r *RAGSourceReconciler) getQueryServiceImage(ragSource *kubemootv1alpha1.RAGSource) string {
	// Per-resource override takes priority
	if ragSource.Spec.QueryService != nil && ragSource.Spec.QueryService.Image != "" {
		return ragSource.Spec.QueryService.Image
	}
	// Fall back to KubemootConfig (or its fallback)
	return r.ConfigCache.GetQueryServiceImage()
}

func (r *RAGSourceReconciler) getQueryServiceReplicas(ragSource *kubemootv1alpha1.RAGSource) int32 {
	if ragSource.Spec.QueryService != nil && ragSource.Spec.QueryService.Replicas > 0 {
		return ragSource.Spec.QueryService.Replicas
	}
	return 1
}

func (r *RAGSourceReconciler) getQueryServiceTopK(ragSource *kubemootv1alpha1.RAGSource) int32 {
	if ragSource.Spec.QueryService != nil && ragSource.Spec.QueryService.TopK > 0 {
		return ragSource.Spec.QueryService.TopK
	}
	return 5
}

// buildJDBCUrl constructs a JDBC URL from a raw endpoint.
// If the endpoint already starts with "jdbc:", it is returned as-is.
// Handles postgres:// and postgresql:// URI schemes by converting to jdbc: format.
// Credentials embedded in postgres:// URIs (user:pass@host) are moved to
// JDBC query parameters since PostgreSQL JDBC doesn't parse userinfo from URLs.
func (r *RAGSourceReconciler) buildJDBCUrl(endpoint string) string {
	if strings.HasPrefix(endpoint, "jdbc:") {
		return endpoint
	}
	// Convert postgres:// or postgresql:// URI to jdbc:postgresql:// format
	var raw string
	if strings.HasPrefix(endpoint, "postgres://") {
		raw = strings.TrimPrefix(endpoint, "postgres://")
	} else if strings.HasPrefix(endpoint, "postgresql://") {
		raw = strings.TrimPrefix(endpoint, "postgresql://")
	} else {
		return jdbcPostgresPrefix + endpoint
	}

	// Check for embedded credentials: user:pass@host:port/db
	if atIdx := strings.Index(raw, "@"); atIdx > 0 {
		userInfo := raw[:atIdx]
		hostAndDB := raw[atIdx+1:]
		parts := strings.SplitN(userInfo, ":", 2)
		jdbcUrl := jdbcPostgresPrefix + hostAndDB
		if len(parts) == 2 {
			jdbcUrl += "?user=" + parts[0] + "&password=" + parts[1]
		} else {
			jdbcUrl += "?user=" + parts[0]
		}
		return jdbcUrl
	}

	return jdbcPostgresPrefix + raw
}

// Endpoint parsing helpers (basic parsing for host:port format)
func (r *RAGSourceReconciler) extractHost(endpoint string) string {
	// Handle format: host:port or host:port/dbname
	if idx := colonIndex(endpoint); idx > 0 {
		return endpoint[:idx]
	}
	return endpoint
}

func (r *RAGSourceReconciler) extractPort(endpoint string) string {
	// Handle format: host:port or host:port/dbname
	start := colonIndex(endpoint)
	if start < 0 {
		return "5432"
	}
	end := slashIndex(endpoint[start:])
	if end < 0 {
		return endpoint[start+1:]
	}
	return endpoint[start+1 : start+end]
}

func (r *RAGSourceReconciler) extractDBName(endpoint string) string {
	// Handle format: host:port/dbname
	if idx := slashIndex(endpoint); idx > 0 {
		return endpoint[idx+1:]
	}
	return "vectors"
}

func (r *RAGSourceReconciler) extractUser(endpoint string) string {
	// For now, default to postgres - could be extended to parse from endpoint
	return "clusteragent"
}

func colonIndex(s string) int {
	for i, c := range s {
		if c == ':' {
			return i
		}
	}
	return -1
}

func slashIndex(s string) int {
	for i, c := range s {
		if c == '/' {
			return i
		}
	}
	return -1
}
