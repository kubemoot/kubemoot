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
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// NotificationSinkReconciler validates NotificationSink CRs and surfaces the
// result on the Ready condition. The actual webhook dispatch happens in the
// notifications package's Dispatcher runnable — this reconciler does not
// listen to NATS or call webhooks.
type NotificationSinkReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=notificationsinks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=notificationsinks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=notificationsinks/finalizers,verbs=update

// Reconcile validates the sink's webhook config and reports Ready accordingly.
// The dispatcher is the authoritative writer for LastFiredAt / TotalDispatched
// / LastError; this reconciler only owns the Ready condition.
func (r *NotificationSinkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	sink := &kubemootv1alpha1.NotificationSink{}
	if err := r.Get(ctx, req.NamespacedName, sink); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	reason, message := validateSinkSpec(&sink.Spec)
	ready := reason == ""
	sink.Status.Ready = ready

	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Validated",
		Message:            "webhook config valid",
		LastTransitionTime: metav1.Now(),
	}
	if !ready {
		cond.Status = metav1.ConditionFalse
		cond.Reason = reason
		cond.Message = message
		log.Info("NotificationSink invalid", "reason", reason, "message", message)
	}
	meta.SetStatusCondition(&sink.Status.Conditions, cond)

	if err := r.Status().Update(ctx, sink); err != nil {
		log.Error(err, "Failed to update NotificationSink status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// validateSinkSpec returns ("", "") on valid specs, or a (reason, message)
// pair suitable for a NotReady condition.
func validateSinkSpec(spec *kubemootv1alpha1.NotificationSinkSpec) (string, string) {
	if spec.Webhook.URL == "" {
		return "MissingURL", "spec.webhook.url is required"
	}
	if reason, msg := validateWebhookURL(spec.Webhook.URL); reason != "" {
		return reason, msg
	}
	for i := range spec.Webhook.Headers {
		h := &spec.Webhook.Headers[i]
		if h.Name == "" {
			return "InvalidHeader", fmt.Sprintf("headers[%d]: name is required", i)
		}
		if h.Value == "" && h.ValueFrom == nil {
			return "InvalidHeader", fmt.Sprintf("headers[%d]: one of value or valueFrom is required", i)
		}
		if h.Value != "" && h.ValueFrom != nil {
			return "InvalidHeader", fmt.Sprintf("headers[%d]: value and valueFrom are mutually exclusive", i)
		}
	}
	return "", ""
}

func validateWebhookURL(raw string) (string, string) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "InvalidURL", err.Error()
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "InvalidURL", fmt.Sprintf("scheme must be http or https; got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "InvalidURL", "host is required"
	}
	return "", ""
}

// SetupWithManager wires the controller into the manager.
func (r *NotificationSinkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.NotificationSink{}).
		Named("notificationsink").
		Complete(r)
}
