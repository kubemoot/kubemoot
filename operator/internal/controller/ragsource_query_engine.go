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

// ragsource_query_engine.go chooses between the two query service engines (KubemootConfig
// spec.queryService.engine) for a RAGSource and moves a namespace from one to the other.
// Python, the default, behaves as it always has: one Deployment and Service per RAGSource. Go
// runs one per namespace (ragsource_query_group.go). A switch changes the endpoints at once and is gentle with the old objects in both directions: the
// old engine's objects are removed only after the new engine's are available, so searches keep
// being answered while the pods of the new engine start.
package controller

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// readersPollInterval is how often a RAGSource whose old collection waits to be dropped asks
// the Go query pods again whether they serve the new one. A mounted ConfigMap changes with no
// event the operator could watch, so the reconcile asks.
const readersPollInterval = 15 * time.Second

// readyBodyLimit caps how much of a pod's /ready answer is read.
const readyBodyLimit = 1 << 20

// queryEngine is the engine the cluster runs its query services on.
func (r *RAGSourceReconciler) queryEngine() kubemootv1alpha1.QueryServiceEngine {
	return r.ConfigCache.GetQueryServiceEngine()
}

// reconcileQueryEngine brings the RAGSource's query service to the engine the cluster chose and
// takes the other engine's objects away.
func (r *RAGSourceReconciler) reconcileQueryEngine(ctx context.Context, rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) error {
	if r.queryEngine() == kubemootv1alpha1.QueryServiceEngineGo {
		return r.reconcileGoEngine(ctx, rs)
	}
	return r.reconcilePythonEngine(ctx, rs, em)
}

// reconcilePythonEngine runs the RAGSource's own query service, then retires the Go group of the
// namespace once every RAGSource there has its own again.
func (r *RAGSourceReconciler) reconcilePythonEngine(ctx context.Context, rs *kubemootv1alpha1.RAGSource, em *kubemootv1alpha1.EmbeddingModel) error {
	if queryServiceEnabled(rs) {
		if err := r.ensureQueryService(ctx, rs, em); err != nil {
			return err
		}
	}
	return r.retireQueryGroup(ctx, rs.Namespace)
}

// reconcileGoEngine brings the namespace's group to what its RAGSources ask for, points the
// RAGSource at it, and retires the RAGSource's own Deployment and Service once the group is up.
func (r *RAGSourceReconciler) reconcileGoEngine(ctx context.Context, rs *kubemootv1alpha1.RAGSource) error {
	if err := r.reconcileQueryGroup(ctx, rs.Namespace); err != nil {
		return err
	}
	if queryServiceEnabled(rs) {
		r.recordGoEndpoint(ctx, rs)
		if !r.groupAvailable(ctx, rs.Namespace) {
			return nil
		}
	}
	return r.retireLegacyQueryService(ctx, rs)
}

// recordGoEndpoint sets status.queryEndpoint to the RAGSource's path under the group's Service.
func (r *RAGSourceReconciler) recordGoEndpoint(ctx context.Context, rs *kubemootv1alpha1.RAGSource) {
	sources, err := r.liveQuerySources(ctx, rs.Namespace)
	if err != nil {
		return
	}
	endpoint := ragQueryEndpoint(kubemootv1alpha1.QueryServiceEngineGo, rs.Name, rs.Namespace, resolveQueryGroupSettings(sources).Port)
	if rs.Status.QueryEndpoint != endpoint {
		rs.Status.QueryEndpoint = endpoint
		logf.FromContext(ctx).Info("Updated query endpoint", "endpoint", endpoint)
	}
}

// groupAvailable reports whether the namespace's Go query service has finished rolling out.
func (r *RAGSourceReconciler) groupAvailable(ctx context.Context, namespace string) bool {
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: namespace, Name: queryGroupName(namespace)}
	if err := r.Get(ctx, key, dep); err != nil {
		return false
	}
	return dep.Labels[labelQueryGroup] != "" && dep.Status.AvailableReplicas > 0 && rolloutComplete(dep)
}

// retireLegacyQueryService deletes the Deployment and Service the Python engine ran for the
// RAGSource, found by name and by being owned by the RAGSource. The group's own objects, which
// can share the name when a RAGSource is named like its namespace, carry the group label and
// are never touched.
func (r *RAGSourceReconciler) retireLegacyQueryService(ctx context.Context, rs *kubemootv1alpha1.RAGSource) error {
	name := queryServiceDeploymentName(rs)
	for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
		if err := r.Get(ctx, types.NamespacedName{Namespace: rs.Namespace, Name: name}, obj); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return err
		}
		if obj.GetLabels()[labelQueryGroup] != "" || !metav1.IsControlledBy(obj, rs) {
			continue
		}
		logf.FromContext(ctx).Info("Removing the per-RAGSource query service; the namespace's query service serves it", "name", name)
		if err := r.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// retireQueryGroup deletes the namespace's Go query service once every RAGSource of the
// namespace that has a query service has its own Deployment rolled out.
func (r *RAGSourceReconciler) retireQueryGroup(ctx context.Context, namespace string) error {
	group := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: queryGroupName(namespace)}, group); err != nil {
		return client.IgnoreNotFound(err)
	}
	if group.Labels[labelQueryGroup] == "" {
		return nil
	}
	sources, err := r.liveQuerySources(ctx, namespace)
	if err != nil {
		return err
	}
	for _, rs := range sources {
		if !r.ownQueryServiceReady(ctx, rs) {
			return nil
		}
	}
	return r.deleteQueryGroup(ctx, namespace)
}

// ownQueryServiceReady reports whether the RAGSource's own Python Deployment has rolled out.
func (r *RAGSourceReconciler) ownQueryServiceReady(ctx context.Context, rs *kubemootv1alpha1.RAGSource) bool {
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: rs.Namespace, Name: queryServiceDeploymentName(rs)}, dep); err != nil {
		return false
	}
	return dep.Labels[labelQueryGroup] == "" && rolloutComplete(dep)
}

// pollWhileReadersMove makes the reconcile run again soon while a retired collection waits for
// the Go query pods to move to the new one.
func (r *RAGSourceReconciler) pollWhileReadersMove(rs *kubemootv1alpha1.RAGSource, res ctrl.Result) ctrl.Result {
	if r.queryEngine() != kubemootv1alpha1.QueryServiceEngineGo || len(rs.Status.RetiredCollections) == 0 {
		return res
	}
	if res.RequeueAfter == 0 || res.RequeueAfter > readersPollInterval {
		res.RequeueAfter = readersPollInterval
	}
	return res
}

// groupReadersMoved reports whether every pod of the namespace's Go query service serves the
// collection for the RAGSource, on the registry the operator last wrote. Each pod's /ready
// names the registry version it loaded and the collection of each source, so a pod that has
// not yet picked up the mounted ConfigMap still reads as not moved, and an index swap drops the
// old collection only after every pod has left it. A namespace with no group has no readers.
func (r *RAGSourceReconciler) groupReadersMoved(ctx context.Context, rs *kubemootv1alpha1.RAGSource, collection string) bool {
	dep := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: rs.Namespace, Name: queryGroupName(rs.Namespace)}
	if err := r.Get(ctx, key, dep); err != nil {
		return errors.IsNotFound(err)
	}
	if dep.Labels[labelQueryGroup] == "" || !rolloutComplete(dep) {
		return false
	}
	version, ok := r.registryVersionOf(ctx, rs.Namespace)
	if !ok {
		return false
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(rs.Namespace), client.MatchingLabels(queryGroupSelector(rs.Namespace))); err != nil {
		return false
	}
	port := groupContainerPort(dep)
	served := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.PodIP == "" || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if !r.podServes(ctx, pod.Status.PodIP, port, readyExpectation{version: version, source: rs.Name, collection: collection}) {
			return false
		}
		served++
	}
	return served > 0
}

// registryVersionOf is the version of the registry in the namespace's ConfigMap.
func (r *RAGSourceReconciler) registryVersionOf(ctx context.Context, namespace string) (string, bool) {
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: queryRegistryName(namespace)}, cm); err != nil {
		return "", false
	}
	return registryVersion([]byte(cm.Data[registryKey])), true
}

// groupContainerPort is the port the Deployment's query container listens on.
func groupContainerPort(dep *appsv1.Deployment) int32 {
	for _, c := range dep.Spec.Template.Spec.Containers {
		if len(c.Ports) > 0 {
			return c.Ports[0].ContainerPort
		}
	}
	return defaultQueryServicePort
}

// readyExpectation is what a pod's /ready must say for a RAGSource's readers to have moved.
type readyExpectation struct {
	version, source, collection string
}

// readyAnswer is the part of a query pod's /ready answer the operator reads.
type readyAnswer struct {
	Registry struct {
		Version string            `json:"version"`
		Sources map[string]string `json:"sources"`
	} `json:"registry"`
}

// podServes asks one query pod what registry it serves.
func (r *RAGSourceReconciler) podServes(ctx context.Context, ip string, port int32, want readyExpectation) bool {
	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	url := "http://" + net.JoinHostPort(ip, strconv.Itoa(int(port))) + readyPath
	resp, err := httpGet(ctx, httpClient, url)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var answer readyAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, readyBodyLimit)).Decode(&answer); err != nil {
		return false
	}
	return answer.Registry.Version == want.version && answer.Registry.Sources[want.source] == want.collection
}

// queryGroupDeploymentPredicate passes the events of the Go engine's Deployments (the ones with
// the group label) that change what the operator decides on: the spec, or the rollout's progress.
func queryGroupDeploymentPredicate() predicate.Predicate {
	isGroup := func(o client.Object) bool { return o.GetLabels()[labelQueryGroup] != "" }
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isGroup(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isGroup(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return isGroup(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, ok1 := e.ObjectOld.(*appsv1.Deployment)
			n, ok2 := e.ObjectNew.(*appsv1.Deployment)
			if !ok1 || !ok2 || !isGroup(n) {
				return false
			}
			return o.Generation != n.Generation || o.Status.AvailableReplicas != n.Status.AvailableReplicas ||
				o.Status.UpdatedReplicas != n.Status.UpdatedReplicas || o.Status.Replicas != n.Status.Replicas
		},
	}
}

// ragSourcesOfQueryGroup maps a Go engine Deployment to the RAGSources of its namespace.
func (r *RAGSourceReconciler) ragSourcesOfQueryGroup(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.ragSourceRequests(ctx, obj.GetNamespace(), func(*kubemootv1alpha1.RAGSource) bool { return true })
}

// ragSourcesOfSecret maps a Secret to the RAGSources that name it as their vector store Secret.
func (r *RAGSourceReconciler) ragSourcesOfSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.ragSourceRequests(ctx, obj.GetNamespace(), func(rs *kubemootv1alpha1.RAGSource) bool {
		return rs.Spec.VectorStore.SecretRef == obj.GetName()
	})
}

// ragSourceRequests are the reconcile requests of the namespace's RAGSources that match.
func (r *RAGSourceReconciler) ragSourceRequests(ctx context.Context, namespace string, match func(*kubemootv1alpha1.RAGSource) bool) []reconcile.Request {
	var list kubemootv1alpha1.RAGSourceList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if match(&list.Items[i]) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}

// queryGroupObjectPredicate passes the events of the group's ConfigMap and Service (the ones with
// the group label) that create, delete or change them.
func queryGroupObjectPredicate() predicate.Predicate {
	isGroup := func(o client.Object) bool { return o.GetLabels()[labelQueryGroup] != "" }
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isGroup(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isGroup(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return isGroup(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isGroup(e.ObjectNew) && e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
		},
	}
}
