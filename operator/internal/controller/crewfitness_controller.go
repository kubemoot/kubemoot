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
	"os"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	fitnessResultAnnotation = "kubemoot.ai/fitness-result"

	// envNATSURL is the env var carrying the NATS address to the runner.
	envNATSURL = "NATS_URL"
	// dropAllCapability drops every Linux capability from the runner container.
	dropAllCapability corev1.Capability = "ALL"
)

// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewfitnesses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewfitnesses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewfitnesses/finalizers,verbs=update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch

// CrewFitnessReconciler reconciles a CrewFitness object
type CrewFitnessReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ConfigCache *ConfigCache
}

func (r *CrewFitnessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	cf := &kubemootv1alpha1.CrewFitness{}
	if err := r.Get(ctx, req.NamespacedName, cf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if isFitnessTerminal(cf.Status.Phase) {
		return r.handleTTL(ctx, cf)
	}
	switch cf.Status.Phase {
	case kubemootv1alpha1.CrewFitnessPhaseRunning:
		return r.checkJob(ctx, cf)

	default:
		// Pending or empty — start the test
		log.Info("Starting fitness test", "name", cf.Name, "crew", cf.Spec.CrewRef, "test", cf.Spec.TestRef)
		return r.startTest(ctx, cf)
	}
}

// endpointWaitDeadline bounds how long a fitness run waits for its crew's
// discussion endpoint to appear before giving up with an Error. Generous: a cold
// crew's gateway is typically up within ~15s, but KEDA pod startup can stretch it.
const endpointWaitDeadline = 3 * time.Minute

// endpointWaitRequeue is how often to re-check while waiting for the endpoint.
const endpointWaitRequeue = 5 * time.Second

// shouldWaitForEndpoint reports whether a run whose crew endpoint is not yet
// ready should keep waiting (true) or give up (false), based on how long it has
// waited since creation. Pure so the deadline logic is unit-testable.
func shouldWaitForEndpoint(created, now time.Time) bool {
	return now.Sub(created) < endpointWaitDeadline
}

func (r *CrewFitnessReconciler) startTest(ctx context.Context, cf *kubemootv1alpha1.CrewFitness) (ctrl.Result, error) {
	endpoint, done, res, err := r.resolveEndpointOrRequeue(ctx, cf)
	if done {
		return res, err
	}

	testKey := cf.Spec.TestRef + ".adl"
	configMapName, err := r.ensureTestConfigMap(ctx, cf, testKey)
	if err != nil {
		return r.setError(ctx, cf, err.Error())
	}

	if err := ensureJobRBAC(ctx, r.Client, cf.Namespace, componentFitnessRunner, fitnessRunnerJobVerbs); err != nil {
		return r.setError(ctx, cf, fmt.Sprintf("failed to ensure RBAC: %v", err))
	}

	jobName := fmt.Sprintf("cf-%s", cf.Name)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}

	if done, res, err := r.createFitnessJob(ctx, cf, jobName, endpoint, testKey, configMapName); done {
		return res, err
	}

	now := metav1.Now()
	cf.Status.Phase = kubemootv1alpha1.CrewFitnessPhaseRunning
	cf.Status.StartedAt = &now
	cf.Status.JobRef = jobName
	if err := r.Status().Update(ctx, cf); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// resolveEndpointOrRequeue resolves the crew's discussion endpoint and decides
// how to proceed when it is not yet available. It returns done=true when the
// caller must return the accompanying result/error immediately (a wait requeue
// or a terminal Error); when done=false the endpoint is ready and startTest
// continues. Only WAIT for a transient not-ready endpoint (the crew is still
// coming up: when a suite is applied together with its crew, the discussion
// endpoint is unpopulated for the first ~15s). Erroring here made a suite
// "Complete" instantly with every iteration errored, no duration, no
// transcripts. A missing crew (notReady=false) is permanent: fail now. The wait
// is bounded so a stuck crew still settles to Error.
// See [[Fitness Suite Errors Instantly Against a Not-Ready Crew]].
func (r *CrewFitnessReconciler) resolveEndpointOrRequeue(ctx context.Context, cf *kubemootv1alpha1.CrewFitness) (endpoint string, done bool, res ctrl.Result, err error) {
	endpoint, notReady, resolveErr := r.resolveEndpoint(ctx, cf)
	if resolveErr == nil {
		return endpoint, false, ctrl.Result{}, nil
	}

	if notReady && shouldWaitForEndpoint(cf.CreationTimestamp.Time, time.Now()) {
		logf.FromContext(ctx).Info("Crew endpoint not ready yet, waiting before starting fitness run",
			"crew", cf.Spec.CrewRef, "reason", resolveErr.Error())
		return "", true, ctrl.Result{RequeueAfter: endpointWaitRequeue}, nil
	}
	if notReady {
		res, err = r.setError(ctx, cf, fmt.Sprintf("%s (waited %s for the crew to become ready)",
			resolveErr.Error(), endpointWaitDeadline))
		return "", true, res, err
	}
	res, err = r.setError(ctx, cf, resolveErr.Error())
	return "", true, res, err
}

// createFitnessJob builds the fitness Job, sets its owner reference, and creates
// it. It returns done=true when the caller must return the accompanying
// result/error immediately (a terminal Error from owner-ref or create failure);
// an AlreadyExists create is treated as success (done=false) so the run still
// transitions to Running.
func (r *CrewFitnessReconciler) createFitnessJob(ctx context.Context, cf *kubemootv1alpha1.CrewFitness, jobName, endpoint, testKey, configMapName string) (done bool, res ctrl.Result, err error) {
	log := logf.FromContext(ctx)

	job := r.buildJob(cf, jobName, endpoint, testKey, configMapName)
	if refErr := ctrl.SetControllerReference(cf, job, r.Scheme); refErr != nil {
		res, err = r.setError(ctx, cf, fmt.Sprintf("failed to set owner reference: %v", refErr))
		return true, res, err
	}

	createErr := r.Create(ctx, job)
	if createErr == nil {
		log.Info("Created fitness test Job", "job", jobName)
		return false, ctrl.Result{}, nil
	}
	if errors.IsAlreadyExists(createErr) {
		log.Info("Job already exists", "job", jobName)
		return false, ctrl.Result{}, nil
	}
	res, err = r.setError(ctx, cf, fmt.Sprintf("failed to create Job: %v", createErr))
	return true, res, err
}

// resolveEndpoint returns the crew's discussion endpoint. The notReady return
// distinguishes a TRANSIENT failure (the crew exists but its endpoint is not
// populated yet - worth waiting for) from a PERMANENT one (the crew CR is
// missing - error immediately, no point waiting).
func (r *CrewFitnessReconciler) resolveEndpoint(ctx context.Context, cf *kubemootv1alpha1.CrewFitness) (endpoint string, notReady bool, err error) {
	crew := &kubemootv1alpha1.Crew{}
	if getErr := r.Get(ctx, types.NamespacedName{Name: cf.Spec.CrewRef, Namespace: cf.Namespace}, crew); getErr != nil {
		// Crew CR absent (deleted or misspelled crewRef): permanent, fail now.
		return "", false, fmt.Errorf("crew %q not found: %v", cf.Spec.CrewRef, getErr)
	}
	if crew.Status.DiscussionEndpoint == "" {
		// Crew exists but the gateway is still coming up: transient, wait.
		return "", true, fmt.Errorf("crew %q has no discussion endpoint yet", cf.Spec.CrewRef)
	}
	return fmt.Sprintf("http://%s-discussion.%s.svc:80/api/v1/discussions/%s",
		cf.Spec.CrewRef, cf.Namespace, cf.Spec.CrewRef), false, nil
}

func (r *CrewFitnessReconciler) ensureTestConfigMap(ctx context.Context, cf *kubemootv1alpha1.CrewFitness, testKey string) (string, error) {
	log := logf.FromContext(ctx)

	if cf.Spec.TestContent != "" {
		configMapName := cf.Name + "-test"
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: cf.Namespace,
			},
		}
		cm.Data = map[string]string{testKey: cf.Spec.TestContent}
		if err := ctrl.SetControllerReference(cf, cm, r.Scheme); err != nil {
			return "", fmt.Errorf("failed to set ConfigMap owner: %v", err)
		}
		if err := r.Create(ctx, cm); err != nil {
			if !errors.IsAlreadyExists(err) {
				return "", fmt.Errorf("failed to create ConfigMap: %v", err)
			}
		} else {
			log.Info("Created fitness test ConfigMap", "configmap", configMapName)
		}
		return configMapName, nil
	}

	configMapName := cf.Spec.ConfigMapRef
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: cf.Namespace}, cm); err != nil {
		return "", fmt.Errorf("ConfigMap %q not found: %v", configMapName, err)
	}
	if _, ok := cm.Data[testKey]; !ok {
		return "", fmt.Errorf("test %q not found in ConfigMap %q (looked for key %q)", cf.Spec.TestRef, configMapName, testKey)
	}
	return configMapName, nil
}

func (r *CrewFitnessReconciler) checkJob(ctx context.Context, cf *kubemootv1alpha1.CrewFitness) (ctrl.Result, error) {
	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: cf.Status.JobRef, Namespace: cf.Namespace}, job); err != nil {
		if errors.IsNotFound(err) {
			return r.setError(ctx, cf, "Job disappeared")
		}
		return ctrl.Result{}, err
	}

	isComplete, isFailed := jobConditions(job)
	if !isComplete && !isFailed {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	r.recordCompletion(cf)
	r.loadAssertionResults(cf, job)
	r.determinePhase(cf, job, isComplete, isFailed)

	if err := r.Status().Update(ctx, cf); err != nil {
		return ctrl.Result{}, err
	}

	if cf.Spec.TTL != nil {
		return ctrl.Result{RequeueAfter: cf.Spec.TTL.Duration}, nil
	}
	return ctrl.Result{}, nil
}

func jobConditions(job *batchv1.Job) (isComplete, isFailed bool) {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			isComplete = true
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			isFailed = true
		}
	}
	return
}

func (r *CrewFitnessReconciler) recordCompletion(cf *kubemootv1alpha1.CrewFitness) {
	now := metav1.Now()
	cf.Status.CompletedAt = &now
	if cf.Status.StartedAt != nil {
		cf.Status.DurationMs = now.Sub(cf.Status.StartedAt.Time).Milliseconds()
	}
}

func (r *CrewFitnessReconciler) loadAssertionResults(cf *kubemootv1alpha1.CrewFitness, job *batchv1.Job) {
	resultJSON, hasResults := job.Annotations[fitnessResultAnnotation]
	if !hasResults {
		return
	}
	var assertions []kubemootv1alpha1.AssertionResult
	if err := json.Unmarshal([]byte(resultJSON), &assertions); err == nil {
		cf.Status.Assertions = assertions
	}
}

func (r *CrewFitnessReconciler) determinePhase(cf *kubemootv1alpha1.CrewFitness, job *batchv1.Job, isComplete, isFailed bool) {
	if isFailed && len(cf.Status.Assertions) == 0 {
		cf.Status.Phase = kubemootv1alpha1.CrewFitnessPhaseError
		cf.Status.Error = extractFailureMessage(job)
		return
	}

	cf.Status.Phase = derivePhaseFromAssertions(cf.Status.Assertions, isComplete)
	if cf.Status.Phase == kubemootv1alpha1.CrewFitnessPhaseError {
		cf.Status.Error = "Job completed without results"
	}
}

func extractFailureMessage(job *batchv1.Job) string {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed {
			return condition.Message
		}
	}
	return "Job failed"
}

func derivePhaseFromAssertions(assertions []kubemootv1alpha1.AssertionResult, isComplete bool) kubemootv1alpha1.CrewFitnessPhase {
	if len(assertions) == 0 {
		if isComplete {
			return kubemootv1alpha1.CrewFitnessPhasePassed
		}
		return kubemootv1alpha1.CrewFitnessPhaseError
	}

	for _, a := range assertions {
		if !a.Passed {
			return kubemootv1alpha1.CrewFitnessPhaseFailed
		}
	}
	return kubemootv1alpha1.CrewFitnessPhasePassed
}

func (r *CrewFitnessReconciler) handleTTL(ctx context.Context, cf *kubemootv1alpha1.CrewFitness) (ctrl.Result, error) {
	if cf.Spec.TTL == nil || cf.Status.CompletedAt == nil {
		return ctrl.Result{}, nil
	}

	expiry := cf.Status.CompletedAt.Add(cf.Spec.TTL.Duration)
	remaining := time.Until(expiry)

	if remaining <= 0 {
		logf.FromContext(ctx).Info("TTL expired, deleting CrewFitness", "name", cf.Name)
		return ctrl.Result{}, r.Delete(ctx, cf)
	}

	return ctrl.Result{RequeueAfter: remaining}, nil
}

func (r *CrewFitnessReconciler) setError(ctx context.Context, cf *kubemootv1alpha1.CrewFitness, msg string) (ctrl.Result, error) {
	logf.FromContext(ctx).Error(fmt.Errorf("%s", msg), "Fitness test error")
	now := metav1.Now()
	cf.Status.Phase = kubemootv1alpha1.CrewFitnessPhaseError
	cf.Status.Error = msg
	cf.Status.CompletedAt = &now
	if cf.Status.StartedAt != nil {
		cf.Status.DurationMs = now.Sub(cf.Status.StartedAt.Time).Milliseconds()
	}
	if err := r.Status().Update(ctx, cf); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// buildRunnerEnv assembles the fitness-runner container env. Suite iterations
// (children carrying the suite labels) additionally get the NATS coordinates so
// the runner writes the per-iteration transcript JSON to Object Store at
// {ns}/{suite}/{runId}/s{scriptIdx}-i{iter}.json. Standalone CrewFitness tests
// (no suite labels) get only the base env and skip transcript capture.
func buildRunnerEnv(cf *kubemootv1alpha1.CrewFitness, endpoint, testKey, jobName string) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "DISCUSSION_ENDPOINT", Value: endpoint},
		{Name: "TEST_FILE", Value: fmt.Sprintf("/tests/%s", testKey)},
		{Name: "CREWFITNESS_NAME", Value: cf.Name},
		{Name: "CREWFITNESS_NAMESPACE", Value: cf.Namespace},
		{Name: "JOB_NAME", Value: jobName},
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: fieldPathNamespace},
			},
		},
	}
	suite := cf.Labels[suiteOwnerLabel]
	runID := cf.Labels[suiteRunIDLabel]
	if suite != "" {
		// Tells the runner its deferred assertions will be judged after the suite,
		// independent of whether a transcript can be captured.
		env = append(env, corev1.EnvVar{Name: "CREWFITNESS_SUITE", Value: suite})
	}
	natsURL := os.Getenv(envNATSURL)
	if suite != "" && runID != "" && natsURL != "" {
		key := fmt.Sprintf("%s/%s/%s/s%s-i%s.json",
			cf.Namespace, suite, runID,
			cf.Labels[suiteScriptIndexLabel], cf.Labels[suiteIterationIndexLabel])
		env = append(env,
			corev1.EnvVar{Name: envNATSURL, Value: natsURL},
			corev1.EnvVar{Name: "TRANSCRIPT_BUCKET", Value: FitnessArtifactsBucket},
			corev1.EnvVar{Name: "TRANSCRIPT_KEY", Value: key},
		)
	}
	return env
}

func (r *CrewFitnessReconciler) buildJob(cf *kubemootv1alpha1.CrewFitness, jobName, endpoint, testKey, configMapName string) *batchv1.Job {
	labels := map[string]string{
		labelName:                 cf.Name,
		labelInstance:             jobName,
		labelManagedBy:            managedByValue,
		labelComponent:            componentFitnessRunner,
		"kubemoot.ai/crewfitness": cf.Name,
		labelFitnessHarness:       "true",
	}

	// Retry transient/infra failures. The runner exits non-zero ONLY when it
	// could not obtain a crew answer (gateway unreachable, no synthesis before the
	// deadline); a real answer that fails its assertions exits 0 and is recorded.
	// So a pod failure here means the scenario never produced a measurement, and a
	// retry can yield a real result instead of freezing a spurious zero into the
	// suite. Bounded at 2 so a genuinely broken scenario still settles to Error.
	// See [[Rollout-Safety Fitness Job Fails BackoffLimitExceeded]].
	backoffLimit := int32(2)
	ttlSeconds := int32(3600)

	runAsNonRoot := true
	allowPrivilegeEscalation := false
	runAsUser := int64(1000)
	runAsGroup := int64(1000)
	seccompProfile := corev1.SeccompProfile{
		Type: corev1.SeccompProfileTypeRuntimeDefault,
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cf.Namespace,
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
					ServiceAccountName: componentFitnessRunner,
					RestartPolicy:      corev1.RestartPolicyNever,
					ImagePullSecrets:   r.ConfigCache.GetImagePullSecrets(),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						SeccompProfile: &seccompProfile,
					},
					Volumes: []corev1.Volume{
						{
							Name: "tests",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMapName,
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  componentFitnessRunner,
							Image: r.ConfigCache.GetFitnessRunnerImage(),
							Env:   buildRunnerEnv(cf, endpoint, testKey, jobName),
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "tests",
									MountPath: "/tests",
									ReadOnly:  true,
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             &runAsNonRoot,
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								RunAsUser:                &runAsUser,
								RunAsGroup:               &runAsGroup,
								SeccompProfile:           &seccompProfile,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{dropAllCapability},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *CrewFitnessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.CrewFitness{}).
		Owns(&batchv1.Job{}).
		// Re-reconcile every CrewFitness when KubemootConfig changes so
		// a fitnessRunner image bump propagates to the Job spec
		// without per-CR annotation. See kubemootconfig_propagation.go.
		Watches(
			&kubemootv1alpha1.KubemootConfig{},
			enqueueAllOnKubemootConfigChange(mgr.GetClient(),
				func() client.ObjectList { return &kubemootv1alpha1.CrewFitnessList{} },
				"crewfitness"),
		).
		Complete(r)
}
