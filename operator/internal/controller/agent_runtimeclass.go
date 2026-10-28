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

	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// ConditionRuntimeClassAvailable is the Agent and MCPServer condition that says whether the
	// RuntimeClass the workload's pod asks for exists in the cluster. It is absent when the pod
	// asks for none.
	ConditionRuntimeClassAvailable = "RuntimeClassAvailable"

	reasonRuntimeClassFound   = "RuntimeClassFound"
	reasonRuntimeClassMissing = "RuntimeClassMissing"

	phasePending = "Pending"
)

// agentRuntimeClassName resolves the RuntimeClass an Agent's pod asks for: the Agent's
// spec.deployment.runtimeClassName when set (an empty string turns the realm default off),
// else KubemootConfig spec.defaults.agentRuntimeClassName. Empty means no runtimeClassName.
func (r *AgentReconciler) agentRuntimeClassName(agent *kubemootv1alpha1.Agent) string {
	if agent.Spec.Deployment != nil && agent.Spec.Deployment.RuntimeClassName != nil {
		return *agent.Spec.Deployment.RuntimeClassName
	}
	if r.ConfigCache == nil {
		return ""
	}
	return r.ConfigCache.GetAgentRuntimeClassName()
}

// podRuntimeClass is the pod spec value for the Agent: nil when the Agent asks for no class.
func (r *AgentReconciler) podRuntimeClass(agent *kubemootv1alpha1.Agent) *string {
	return runtimeClassPtr(r.agentRuntimeClassName(agent))
}

// runtimeClassPtr is the pod spec value for a resolved class name: nil for the empty name.
func runtimeClassPtr(name string) *string {
	if name == "" {
		return nil
	}
	return &name
}

// syncRuntimeClassCondition keeps the Agent's RuntimeClassAvailable condition true to the
// cluster. ensureDeployment holds the Deployment while the condition is False. See
// syncRuntimeClassConditionFor.
func (r *AgentReconciler) syncRuntimeClassCondition(ctx context.Context, agent *kubemootv1alpha1.Agent) error {
	return syncRuntimeClassConditionFor(ctx, r.Client, agent, &agent.Status.Conditions, r.agentRuntimeClassName(agent))
}

// syncRuntimeClassConditionFor keeps the RuntimeClassAvailable condition of a workload (an
// Agent or an MCPServer) true to the cluster and writes it to the object's status when it
// changed, so the condition is recorded whatever the rest of the reconcile does. With no class
// asked for (name is empty) the condition is removed. With the class present it is True. With
// the class missing it is False, and the reconciler holds the Deployment (pods that name an
// unknown RuntimeClass are rejected at creation, so a Deployment would only fail with events).
// The RuntimeClass watch re-reconciles the workload when the class appears or goes.
func syncRuntimeClassConditionFor(ctx context.Context, c client.Client, obj client.Object, conditions *[]metav1.Condition, name string) error {
	var changed bool
	if name == "" {
		changed = meta.RemoveStatusCondition(conditions, ConditionRuntimeClassAvailable)
	} else {
		cond, err := runtimeClassCondition(ctx, c, obj, name)
		if err != nil {
			return err
		}
		changed = meta.SetStatusCondition(conditions, cond)
	}
	if !changed {
		return nil
	}
	if err := c.Status().Update(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("recording the RuntimeClass condition in the status: %w", err)
	}
	return nil
}

// runtimeClassCondition looks the class up and builds the condition that describes it.
func runtimeClassCondition(ctx context.Context, c client.Reader, obj client.Object, name string) (metav1.Condition, error) {
	cond := metav1.Condition{Type: ConditionRuntimeClassAvailable, ObservedGeneration: obj.GetGeneration()}
	err := c.Get(ctx, types.NamespacedName{Name: name}, &nodev1.RuntimeClass{})
	switch {
	case err == nil:
		cond.Status, cond.Reason = metav1.ConditionTrue, reasonRuntimeClassFound
		cond.Message = fmt.Sprintf("RuntimeClass %q exists", name)
	case apierrors.IsNotFound(err):
		cond.Status, cond.Reason = metav1.ConditionFalse, reasonRuntimeClassMissing
		cond.Message = fmt.Sprintf("RuntimeClass %q does not exist; the Deployment is held until it does. "+
			"Install the runtime on the nodes and create the RuntimeClass, or clear runtimeClassName.", name)
		logf.FromContext(ctx).Info("Workload asks for a RuntimeClass that does not exist; holding its Deployment",
			"name", obj.GetName(), "namespace", obj.GetNamespace(), "runtimeClass", name)
	default:
		return cond, err
	}
	return cond, nil
}

// runtimeClassHold reports whether a workload's Deployment is held for a missing RuntimeClass,
// from its status conditions, with the message that says why.
func runtimeClassHold(conditions []metav1.Condition) (message string, held bool) {
	cond := meta.FindStatusCondition(conditions, ConditionRuntimeClassAvailable)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		return "", false
	}
	return cond.Message, true
}

// runtimeClassRequests maps a changed RuntimeClass to the Agents whose pods ask for it.
func (r *AgentReconciler) runtimeClassRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	return runtimeClassRequestsFor(ctx, r.Client, obj, &kubemootv1alpha1.AgentList{}, func(o client.Object) string {
		agent, _ := o.(*kubemootv1alpha1.Agent)
		return r.agentRuntimeClassName(agent)
	})
}

// runtimeClassRequestsFor maps a changed RuntimeClass to the items of list whose pods ask for
// it; requested names the class an item asks for.
func runtimeClassRequestsFor(ctx context.Context, c client.Reader, obj client.Object, list client.ObjectList, requested func(client.Object) string) []reconcile.Request {
	rc, ok := obj.(*nodev1.RuntimeClass)
	if !ok {
		return nil
	}
	if err := c.List(ctx, list); err != nil {
		logf.FromContext(ctx).Error(err, "RuntimeClass propagation: list failed", "runtimeClass", rc.Name)
		return nil
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		logf.FromContext(ctx).Error(err, "RuntimeClass propagation: reading the list failed", "runtimeClass", rc.Name)
		return nil
	}
	var reqs []reconcile.Request
	for _, item := range items {
		if o, isObj := item.(client.Object); isObj && requested(o) == rc.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(o)})
		}
	}
	return reqs
}

// runtimeClassWatch is the SetupWithManager handler for RuntimeClass objects.
func (r *AgentReconciler) runtimeClassWatch() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(r.runtimeClassRequests)
}

// runtimeClassAppearsOrGoes passes only the events that change whether a class exists.
func runtimeClassAppearsOrGoes() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}
