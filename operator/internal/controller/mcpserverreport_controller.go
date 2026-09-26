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
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/javajon/kubemoot/operator/internal/nats"
)

const maxTrials = 20

// MCPServerReportReconciler reconciles MCPServerReport objects.
// Lightweight controller that computes derived status fields from trial records.
type MCPServerReportReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	NATSPublisher *kubemootnats.Publisher
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpserverreports,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpserverreports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpserverreports/finalizers,verbs=update

// Reconcile computes derived status fields from trial records and admin overrides.
func (r *MCPServerReportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	report := &kubemootv1alpha1.MCPServerReport{}
	if err := r.Get(ctx, req.NamespacedName, report); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.V(1).Info("Reconciling MCPServerReport", "name", report.Name)

	// Trim trials to max entries (most recent)
	if len(report.Status.Trials) > maxTrials {
		report.Status.Trials = report.Status.Trials[len(report.Status.Trials)-maxTrials:]
	}

	// Compute derived status fields from trials
	computeTrialStats(&report.Status)

	// Compute verdict: admin override takes priority
	if report.Spec.AdminVerdict != "" {
		report.Status.Verdict = report.Spec.AdminVerdict
	} else {
		report.Status.Verdict = r.computeVerdict(report.Status.Trials)
	}

	// Set condition
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Computed",
		Message:            fmt.Sprintf("Verdict: %s, %d trials (%d success, %d failure)", report.Status.Verdict, report.Status.SuccessCount+report.Status.FailureCount, report.Status.SuccessCount, report.Status.FailureCount),
		LastTransitionTime: metav1.Now(),
	}
	meta.SetStatusCondition(&report.Status.Conditions, condition)

	if err := r.Status().Update(ctx, report); err != nil {
		log.Error(err, "Failed to update MCPServerReport status")
		return ctrl.Result{}, err
	}

	// Publish verdict to NATS
	if r.NATSPublisher != nil {
		_ = r.NATSPublisher.Publish(
			fmt.Sprintf("kubemoot.chronicle.%s.verdict", report.Name),
			map[string]interface{}{
				"server":               report.Spec.ServerName,
				"verdict":              report.Status.Verdict,
				"successRate":          report.Status.SuccessRate,
				"recommendedTransport": report.Status.RecommendedTransport,
				"recommendedVersion":   report.Status.RecommendedVersion,
			},
		)
	}

	return ctrl.Result{}, nil
}

// computeTrialStats computes success/failure counts, rates, timestamps, and recommendations from trials.
func computeTrialStats(status *kubemootv1alpha1.MCPServerReportStatus) {
	var successCount, failureCount int64
	for _, trial := range status.Trials {
		if trial.Success {
			successCount++
		} else {
			failureCount++
		}
	}
	status.SuccessCount = successCount
	status.FailureCount = failureCount

	total := successCount + failureCount
	if total > 0 {
		rate := float64(successCount) / float64(total)
		status.SuccessRate = fmt.Sprintf("%.0f%%", rate*100)
	} else {
		status.SuccessRate = "N/A"
	}

	findLastTimestamps(status)
	findRecommendedTrial(status)
}

// findLastTimestamps scans trials in reverse for last tested and last successful timestamps.
func findLastTimestamps(status *kubemootv1alpha1.MCPServerReportStatus) {
	status.LastTested = nil
	status.LastSuccessful = nil
	for i := len(status.Trials) - 1; i >= 0; i-- {
		trial := status.Trials[i]
		if status.LastTested == nil && trial.TestedAt != nil {
			status.LastTested = trial.TestedAt
		}
		if status.LastSuccessful == nil && trial.Success && trial.TestedAt != nil {
			status.LastSuccessful = trial.TestedAt
		}
		if status.LastTested != nil && status.LastSuccessful != nil {
			return
		}
	}
}

// findRecommendedTrial finds the recommended version and transport from the most recent successful trial.
func findRecommendedTrial(status *kubemootv1alpha1.MCPServerReportStatus) {
	for i := len(status.Trials) - 1; i >= 0; i-- {
		if status.Trials[i].Success {
			status.RecommendedVersion = status.Trials[i].Version
			status.RecommendedTransport = status.Trials[i].Transport
			return
		}
	}
}

// computeVerdict determines the automated verdict from trial history.
func (r *MCPServerReportReconciler) computeVerdict(trials []kubemootv1alpha1.TrialRecord) string {
	if len(trials) == 0 {
		return string(kubemootv1alpha1.VerdictUntested)
	}

	// Look at the last 3 trials
	recentCount := 3
	if len(trials) < recentCount {
		recentCount = len(trials)
	}
	recent := trials[len(trials)-recentCount:]

	var recentSuccesses, recentFailures int
	for _, trial := range recent {
		if trial.Success {
			recentSuccesses++
		} else {
			recentFailures++
		}
	}

	// All recent trials failed -> avoid
	if recentSuccesses == 0 {
		return string(kubemootv1alpha1.VerdictAvoid)
	}

	// At least one recent success -> use
	if recentFailures == 0 {
		return string(kubemootv1alpha1.VerdictUse)
	}

	// Mix of success and failure in recent trials -> caution
	return string(kubemootv1alpha1.VerdictCaution)
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPServerReportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.MCPServerReport{}).
		Named("mcpserverreport").
		Complete(r)
}
