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
	"reflect"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// hasReadyEndpoint reports whether any slice lists an endpoint that is ready. An
// endpoint that does not state readiness counts as ready, as Kubernetes defines it.
// A terminating endpoint reports not ready, so a pod that is shutting down does not
// keep the server available.
func hasReadyEndpoint(slices []discoveryv1.EndpointSlice) bool {
	for i := range slices {
		for _, ep := range slices[i].Endpoints {
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				return true
			}
		}
	}
	return false
}

// noReadyEndpoints reports, for a server with spec.serviceRef, whether the Service
// has no ready endpoint, with a message saying what Kubernetes shows behind it.
// It reports false when the Service cannot be read (discovery says so) or the
// endpoints cannot be listed, since an unread endpoint is not a missing one.
func (r *ModelServerReconciler) noReadyEndpoints(ctx context.Context, server *aiv1alpha1.ModelServer) (string, bool) {
	if server.Spec.ServiceRef == nil {
		return "", false
	}
	key := serviceKey(server)
	svc := &corev1.Service{}
	if err := r.Get(ctx, key, svc); err != nil {
		return "", false
	}
	slices := &discoveryv1.EndpointSliceList{}
	if err := r.List(ctx, slices, client.InNamespace(key.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: key.Name}); err != nil {
		return "", false
	}
	if hasReadyEndpoint(slices.Items) {
		return "", false
	}
	return fmt.Sprintf("Service %s has no ready endpoints%s", key, r.backingDetail(ctx, svc)), true
}

// backingDetail says what Kubernetes shows about the pods behind a Service:
// "; pod x is Pending", "; pod x is on node n, which is not ready". It is empty
// when the Service selects nothing or the pods cannot be listed.
func (r *ModelServerReconciler) backingDetail(ctx context.Context, svc *corev1.Service) string {
	if len(svc.Spec.Selector) == 0 {
		return ""
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(svc.Namespace), client.MatchingLabels(svc.Spec.Selector)); err != nil {
		return ""
	}
	if len(pods.Items) == 0 {
		return "; no pod matches its selector"
	}
	var notes []string
	for i := range pods.Items {
		notes = append(notes, r.describePod(ctx, &pods.Items[i]))
	}
	sort.Strings(notes)
	return "; " + strings.Join(notes, "; ")
}

// describePod is one line on a pod: its phase, and its node when that node is
// not ready or is cordoned.
func (r *ModelServerReconciler) describePod(ctx context.Context, pod *corev1.Pod) string {
	line := fmt.Sprintf("pod %s is %s", pod.Name, pod.Status.Phase)
	if pod.Spec.NodeName == "" {
		return line
	}
	node := &corev1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
		return line
	}
	switch {
	case !nodeIsReady(node):
		line += fmt.Sprintf(" on node %s, which is not ready", node.Name)
	case node.Spec.Unschedulable:
		line += fmt.Sprintf(" on node %s, which is cordoned", node.Name)
	}
	return line
}

func nodeIsReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// dropLiveCapacity removes what only a live server can state: the loaded models,
// the memory they hold, and the agent counts. Calibration and the hardware facts
// stay, since they remain true when the server returns.
func dropLiveCapacity(server *aiv1alpha1.ModelServer) {
	c := server.Status.Capacity
	if c == nil {
		return
	}
	c.LoadedModels = nil
	c.AcceleratorMemoryUsedMiB = 0
	c.VRAMUsedMiB = 0
	c.AgentCount = 0
	c.MullingAgentCount = 0
	c.TriageAgentCount = 0
}

// endpointSlicesToServers maps an EndpointSlice to the servers whose serviceRef
// names its Service, so a server is judged again the moment its pod becomes ready
// or stops being ready.
func (r *ModelServerReconciler) endpointSlicesToServers(ctx context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[discoveryv1.LabelServiceName]
	if name == "" {
		return nil
	}
	return r.serversForService(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: name})
}

// endpointReadinessChanged passes an EndpointSlice of a Service, and on update only
// when what an endpoint says about its readiness changed (ready, serving or
// terminating), not when its addresses or metadata did.
func endpointReadinessChanged() predicate.Predicate {
	ofService := func(o client.Object) bool { return o.GetLabels()[discoveryv1.LabelServiceName] != "" }
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return ofService(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return ofService(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return ofService(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldSlice, ok1 := e.ObjectOld.(*discoveryv1.EndpointSlice)
			newSlice, ok2 := e.ObjectNew.(*discoveryv1.EndpointSlice)
			if !ok1 || !ok2 || !ofService(newSlice) {
				return false
			}
			return !reflect.DeepEqual(endpointConditions(oldSlice), endpointConditions(newSlice))
		},
	}
}

// endpointConditions lists the readiness conditions of a slice's endpoints in order.
func endpointConditions(slice *discoveryv1.EndpointSlice) []discoveryv1.EndpointConditions {
	out := make([]discoveryv1.EndpointConditions, len(slice.Endpoints))
	for i := range slice.Endpoints {
		out[i] = slice.Endpoints[i].Conditions
	}
	return out
}
