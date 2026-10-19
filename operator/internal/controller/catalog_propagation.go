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

// catalog_propagation.go wires catalog changes to the objects that depend on them.
// A provider's catalog change reaches every crew that can use the provider, in any
// namespace; a change to the Models in a namespace reaches the policies there, whose
// CatalogMode condition follows them.
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// catalogDigest summarizes what a provider offers a crew: each candidate model with
// its labels and declared memory. It leaves out everything that moves on every probe
// (loaded models, agent counts) and the provider's readiness, which a single failed
// probe flips, so reacting to it never rolls an agent pod while models load and
// unload or a probe blips. A provider that goes away is a delete event, not a digest.
func catalogDigest(p *aiv1alpha1.ModelProvider) string {
	candidates := providerCandidates(p)
	parts := make([]string, 0, len(candidates))
	for _, m := range candidates {
		keys := make([]string, 0, len(m.Labels))
		for k, v := range m.Labels {
			keys = append(keys, k+"="+v)
		}
		sort.Strings(keys)
		parts = append(parts, fmt.Sprintf("%s{%s}%d", m.Spec.Model, strings.Join(keys, ","), m.Spec.VRAMMib))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// catalogChanged passes the ModelProvider events that can change a binding: a new
// or removed provider, and an update that changes its catalogDigest.
func catalogChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldP, okOld := e.ObjectOld.(*aiv1alpha1.ModelProvider)
			newP, okNew := e.ObjectNew.(*aiv1alpha1.ModelProvider)
			return okOld && okNew && catalogDigest(oldP) != catalogDigest(newP)
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// enqueueAgentsOnCatalogChange enqueues the Agents that can select from a changed
// provider: every Agent when the provider is in the operator's namespace, the
// Agents of its own namespace otherwise. An Agent of a crew that ships its own Models
// is reconciled too and finds nothing to change; reconciling is idempotent and rolls
// no pod unless the pick changed.
func enqueueAgentsOnCatalogChange(cli client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return catalogAgentRequests(ctx, cli, obj)
	})
}

// catalogAgentRequests lists the reconcile requests for a changed provider's users.
func catalogAgentRequests(ctx context.Context, cli client.Client, obj client.Object) []reconcile.Request {
	p, ok := obj.(*aiv1alpha1.ModelProvider)
	if !ok {
		return nil
	}
	var opts []client.ListOption
	if p.Namespace != operatorNamespace() {
		opts = append(opts, client.InNamespace(p.Namespace))
	}
	agents := &aiv1alpha1.AgentList{}
	if err := cli.List(ctx, agents, opts...); err != nil {
		logf.FromContext(ctx).Error(err, "catalog propagation: list agents failed", "provider", p.Name)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(agents.Items))
	for i := range agents.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&agents.Items[i])})
	}
	if len(reqs) > 0 {
		logf.FromContext(ctx).Info("model catalog changed; enqueueing agent reconciles", "provider", p.Name, "count", len(reqs))
	}
	return reqs
}

// enqueuePoliciesOnModelChange enqueues the CrewSchedulingPolicies in the namespace of
// a changed Model: the Models a namespace holds decide its CatalogMode.
func enqueuePoliciesOnModelChange(cli client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return modelPolicyRequests(ctx, cli, obj)
	})
}

// modelPolicyRequests lists the reconcile requests for the policies of a changed
// Model's namespace; a non-Model maps to none.
func modelPolicyRequests(ctx context.Context, cli client.Client, obj client.Object) []reconcile.Request {
	if _, ok := obj.(*aiv1alpha1.Model); !ok {
		return nil
	}
	policies := &aiv1alpha1.CrewSchedulingPolicyList{}
	if err := cli.List(ctx, policies, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "catalog propagation: list policies failed", "namespace", obj.GetNamespace())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(policies.Items))
	for i := range policies.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[i])})
	}
	return reqs
}

// ---- policy conditions -----------------------------------------------------

// setCatalogConditions records where the policy's crew gets its models and whether a
// rule selects on the retired latencyClass label against the catalog.
func setCatalogConditions(policy *aiv1alpha1.CrewSchedulingPolicy, mode string) {
	mc := metav1.Condition{
		Type: conditionTypeCatalogMode, Status: metav1.ConditionTrue, Reason: mode, ObservedGeneration: policy.Generation,
		Message: "The crew selects from the cluster's model catalog",
	}
	if mode == catalogModeCrewLocal {
		mc.Status = metav1.ConditionFalse
		mc.Message = "The crew ships Models of its own and uses only those; delete them to select from the cluster's model catalog"
	}
	meta.SetStatusCondition(&policy.Status.Conditions, mc)

	ll := metav1.Condition{
		Type: conditionTypeLegacyLatencyClass, Status: metav1.ConditionFalse, Reason: "NotUsed", ObservedGeneration: policy.Generation,
		Message: "No rule selects on latencyClass against the catalog",
	}
	if mode == catalogModeCluster && selectsOnLabel(policy, latencyClassLabel) {
		ll.Status, ll.Reason = metav1.ConditionTrue, "LatencyClassSelected"
		ll.Message = "A rule selects on latencyClass, which the cluster catalog does not carry, so it matches nothing; select on kubemoot.ai/size instead"
	}
	meta.SetStatusCondition(&policy.Status.Conditions, ll)
}

// selectsOnLabel reports whether any rule's require or prefer refers to the label key.
func selectsOnLabel(policy *aiv1alpha1.CrewSchedulingPolicy, key string) bool {
	for i := range policy.Spec.Rules {
		rule := &policy.Spec.Rules[i]
		if selectorUsesKey(rule.Require, key) || requirementsUseKey(rule.RequireExpressions, key) {
			return true
		}
		for _, term := range rule.Prefer {
			if selectorUsesKey(term.Selector, key) || requirementsUseKey(term.Expressions, key) {
				return true
			}
		}
	}
	return false
}

// selectorUsesKey reports whether a label selector names the key.
func selectorUsesKey(sel *metav1.LabelSelector, key string) bool {
	if sel == nil {
		return false
	}
	if _, ok := sel.MatchLabels[key]; ok {
		return true
	}
	for _, e := range sel.MatchExpressions {
		if e.Key == key {
			return true
		}
	}
	return false
}

// requirementsUseKey reports whether a requirement list names the key.
func requirementsUseKey(reqs []corev1.NodeSelectorRequirement, key string) bool {
	for _, r := range reqs {
		if r.Key == key {
			return true
		}
	}
	return false
}

// catalogModeOf is the mode a crew in the namespace is in.
func catalogModeOf(ctx context.Context, c client.Reader, namespace string) (string, error) {
	local, err := crewLocalModels(ctx, c, namespace)
	if err != nil {
		return "", err
	}
	return catalogModeFor(local), nil
}
