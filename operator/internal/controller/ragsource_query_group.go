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

// ragsource_query_group.go runs the Go query service as ONE Deployment per namespace that
// has RAGSources, serving every one of them, instead of one pod per RAGSource.
//
// Ownership. The Deployment, the Service and the registry ConfigMap of a namespace are owned by
// every live RAGSource of the namespace that has a query service (plain owner references, none
// of them the controller). Kubernetes garbage collection deletes an object only when all its
// owners are gone, so the last RAGSource to go takes the group with it, while a namespace that
// keeps one RAGSource keeps the service. The operator also deletes the group itself when
// the last such RAGSource is deleted or switches its query service off, so the cleanup does not
// wait on garbage collection. Whichever RAGSource reconciles brings the whole group to what all
// of them ask for: the registry, the Deployment and the Service are computed from the list of the
// namespace's RAGSources, so the reconcilers of two RAGSources cannot disagree.
package controller

import (
	"context"
	"fmt"
	"maps"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// defaultQueryReplicas is the group's replica count when no RAGSource asks for more.
	defaultQueryReplicas int32 = 1
	// defaultGoQueryTopK is the result count of a query that names none, for a RAGSource that sets none.
	defaultGoQueryTopK = 5
	// goQueryDBUser is the database user when a vector store Secret names none, as the Python service had it.
	goQueryDBUser = "clusteragent"
	// registryVolume is the volume of the registry ConfigMap.
	registryVolume = "registry"
	// envDBUser is the variable that names the database user the service falls back to.
	envDBUser = "KUBEMOOT_DB_USER"
	// readyPath is the readiness route of both query services.
	readyPath = "/ready"
)

// liveQuerySources lists the RAGSources of the namespace the group serves, by name: the ones
// that have a query service and are not being deleted.
func (r *RAGSourceReconciler) liveQuerySources(ctx context.Context, namespace string) ([]*kubemootv1alpha1.RAGSource, error) {
	var list kubemootv1alpha1.RAGSourceList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	var out []*kubemootv1alpha1.RAGSource
	for i := range list.Items {
		rs := &list.Items[i]
		if rs.DeletionTimestamp.IsZero() && queryServiceEnabled(rs) {
			out = append(out, rs)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// reconcileQueryGroup brings the namespace's query service to what its RAGSources ask for, or
// removes it when none asks for one.
func (r *RAGSourceReconciler) reconcileQueryGroup(ctx context.Context, namespace string) error {
	sources, err := r.liveQuerySources(ctx, namespace)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return r.deleteQueryGroup(ctx, namespace)
	}
	registry, credentials, err := r.buildRegistry(ctx, namespace, sources)
	if err != nil {
		return err
	}
	owners := ownerReferencesOf(sources)
	if err := r.ensureRegistryConfigMap(ctx, namespace, registry, owners); err != nil {
		return err
	}
	if err := r.ensureGroupDeployment(ctx, namespace, sources, credentials, owners); err != nil {
		return err
	}
	return r.ensureGroupService(ctx, namespace, sources, owners)
}

// buildRegistry is the registry file of the namespace and the Secrets its sources read
// credentials from. A RAGSource that cannot be served now (its
// EmbeddingModel is not ready, its Secret is missing) keeps the entry it had, so a passing
// hiccup does not take its searches away; with no earlier entry it is left out until it can be.
func (r *RAGSourceReconciler) buildRegistry(ctx context.Context, namespace string, sources []*kubemootv1alpha1.RAGSource) ([]byte, []string, error) {
	previous, err := r.currentRegistry(ctx, namespace)
	if err != nil {
		return nil, nil, err
	}
	var entries []queryRegistrySource
	secretOf := map[string]string{}
	for _, rs := range sources {
		entry, reason := r.servableEntry(ctx, rs)
		if reason != "" {
			logf.FromContext(ctx).V(1).Info("RAGSource is not in the query registry yet", "ragSource", rs.Name, "reason", reason)
			old, had := previous[rs.Name]
			if !had {
				continue
			}
			entry = old
		}
		entries = append(entries, entry)
		secretOf[rs.Name] = rs.Spec.VectorStore.SecretRef
	}
	data, err := marshalRegistry(entries)
	return data, registryCredentials(entries, secretOf), err
}

// servableEntry is the registry entry of a RAGSource, or the reason it cannot be served yet.
func (r *RAGSourceReconciler) servableEntry(ctx context.Context, rs *kubemootv1alpha1.RAGSource) (queryRegistrySource, string) {
	em, err := r.resolveEmbeddingModel(ctx, rs)
	if err != nil {
		em = nil
	}
	entry, reason := registryEntry(rs, em)
	if reason != "" || rs.Spec.VectorStore.SecretRef == "" {
		return entry, reason
	}
	if !r.vectorStoreSecretUsable(ctx, rs.Namespace, rs.Spec.VectorStore.SecretRef) {
		return queryRegistrySource{}, fmt.Sprintf("its vector store Secret %q is missing or has no %s", rs.Spec.VectorStore.SecretRef, secretKeyPassword)
	}
	return entry, ""
}

// vectorStoreSecretUsable reports whether the Secret exists and holds the password the service reads.
func (r *RAGSourceReconciler) vectorStoreSecretUsable(ctx context.Context, namespace, name string) bool {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return false
	}
	return len(secret.Data[secretKeyPassword]) > 0
}

// currentRegistry is the entries of the registry the namespace has now, by name.
func (r *RAGSourceReconciler) currentRegistry(ctx context.Context, namespace string) (map[string]queryRegistrySource, error) {
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: queryRegistryName(namespace)}, cm)
	if errors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseRegistry(cm.Data[registryKey]), nil
}

// ownerReferencesOf makes every RAGSource an owner of the group's objects. None is the
// controller, so no one of them decides alone when the group goes.
func ownerReferencesOf(sources []*kubemootv1alpha1.RAGSource) []metav1.OwnerReference {
	out := make([]metav1.OwnerReference, 0, len(sources))
	for _, rs := range sources {
		out = append(out, metav1.OwnerReference{
			APIVersion: kubemootv1alpha1.GroupVersion.String(), Kind: "RAGSource", Name: rs.Name, UID: rs.UID,
		})
	}
	return out
}

// queryGroupLabels are the labels of the group's objects.
func queryGroupLabels(namespace string) map[string]string {
	name := queryGroupName(namespace)
	return map[string]string{
		labelName:       name,
		labelInstance:   name,
		labelManagedBy:  managedByValue,
		labelComponent:  componentQueryService,
		labelQueryGroup: name,
	}
}

// queryGroupSelector picks the group's pods. It leaves out the name labels, which a
// Deployment's selector may not change once set.
func queryGroupSelector(namespace string) map[string]string {
	return map[string]string{labelComponent: componentQueryService, labelQueryGroup: queryGroupName(namespace)}
}

// ensureRegistryConfigMap writes the registry. The pods read it from the mounted directory and
// swap to it themselves.
func (r *RAGSourceReconciler) ensureRegistryConfigMap(ctx context.Context, namespace string, registry []byte, owners []metav1.OwnerReference) error {
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: queryRegistryName(namespace), Namespace: namespace,
			Labels: queryGroupLabels(namespace), OwnerReferences: owners,
		},
		Data: map[string]string{registryKey: string(registry)},
	}
	current := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if maps.Equal(current.Data, desired.Data) && maps.Equal(current.Labels, desired.Labels) &&
		equality.Semantic.DeepEqual(current.OwnerReferences, owners) {
		return nil
	}
	current.Data, current.Labels, current.OwnerReferences = desired.Data, desired.Labels, owners
	return r.Update(ctx, current)
}

// queryGroupSettings are the values of the group's Deployment that the RAGSources spread
// over. They are per group, not per RAGSource, and a conflict resolves the same way every
// time: replicas is the largest any RAGSource asks for; resources come from the
// first RAGSource, by name, that sets them; the port is always 8000, so every endpoint a RAGSource
// or an agent builds before the Service exists is the endpoint the Service has. The image is KubemootConfig's: a RAGSource's
// spec.queryService.image names a Python image and is not read.
type queryGroupSettings struct {
	Replicas  int32
	Port      int32
	Resources corev1.ResourceRequirements
}

func resolveQueryGroupSettings(sources []*kubemootv1alpha1.RAGSource) queryGroupSettings {
	s := queryGroupSettings{Replicas: defaultQueryReplicas, Port: defaultQueryServicePort, Resources: defaultGoQueryResources()}
	resourcesSet := false
	for _, rs := range sources {
		qs := rs.Spec.QueryService
		if qs == nil {
			continue
		}
		s.Replicas = max(s.Replicas, qs.Replicas)
		if !resourcesSet && qs.Resources != nil {
			s.Resources, resourcesSet = *qs.Resources, true
		}
	}
	return s
}

// defaultGoQueryResources fit a service that holds a few database pools for a crew: far below the
// 64Mi to 512Mi of the Python pod that each RAGSource used to run.
func defaultGoQueryResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("20m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
}

// buildGroupDeployment is the Deployment of the namespace's Go query service.
func (r *RAGSourceReconciler) buildGroupDeployment(namespace string, settings queryGroupSettings, credentials []string, owners []metav1.OwnerReference) *appsv1.Deployment {
	name := queryGroupName(namespace)
	labels := queryGroupLabels(namespace)
	volumes, mounts := credentialVolumes(credentials)
	volumes = append([]corev1.Volume{{
		Name: registryVolume,
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: queryRegistryName(namespace)},
		}},
	}}, volumes...)
	mounts = append([]corev1.VolumeMount{{Name: registryVolume, MountPath: registryDir, ReadOnly: true}}, mounts...)
	runAsNonRoot := true
	seccomp := corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels, OwnerReferences: owners},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(settings.Replicas),
			Selector: &metav1.LabelSelector{MatchLabels: queryGroupSelector(namespace)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers:       []corev1.Container{r.groupContainer(settings, mounts)},
					Volumes:          volumes,
					ImagePullSecrets: r.ConfigCache.GetImagePullSecrets(),
					SecurityContext:  &corev1.PodSecurityContext{RunAsNonRoot: &runAsNonRoot, SeccompProfile: &seccomp},
				},
			},
		},
	}
}

// groupContainer is the Go query service container.
func (r *RAGSourceReconciler) groupContainer(settings queryGroupSettings, mounts []corev1.VolumeMount) corev1.Container {
	port := settings.Port
	runAsNonRoot, noEscalation := true, false
	uid := int64(1000)
	seccomp := corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	return corev1.Container{
		Name:  componentQueryService,
		Image: r.ConfigCache.GetQueryServiceGoImage(),
		Env: []corev1.EnvVar{
			{Name: "KUBEMOOT_REGISTRY_FILE", Value: registryDir + "/" + registryKey},
			{Name: "KUBEMOOT_QUERY_PORT", Value: fmt.Sprintf("%d", port)},
			{Name: "KUBEMOOT_DEFAULT_TOP_K", Value: fmt.Sprintf("%d", defaultGoQueryTopK)},
			{Name: envDBUser, Value: goQueryDBUser},
		},
		Resources:    settings.Resources,
		VolumeMounts: mounts,
		Ports:        []corev1.ContainerPort{{Name: portNameHTTP, ContainerPort: port, Protocol: corev1.ProtocolTCP}},
		LivenessProbe: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: healthPath, Port: intstr.FromInt32(port)}},
			InitialDelaySeconds: 5, PeriodSeconds: 30,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: readyPath, Port: intstr.FromInt32(port)}},
			InitialDelaySeconds: 2, PeriodSeconds: 10,
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot: &runAsNonRoot, AllowPrivilegeEscalation: &noEscalation, RunAsUser: &uid, RunAsGroup: &uid,
			SeccompProfile: &seccomp, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{dropAllCapability}},
		},
	}
}

// ensureGroupDeployment creates the Deployment or brings it to the desired one. Only a change
// of the pod template or the replica count rolls the pods: a registry change reaches the
// running pods through the mounted ConfigMap.
func (r *RAGSourceReconciler) ensureGroupDeployment(ctx context.Context, namespace string, sources []*kubemootv1alpha1.RAGSource, credentials []string, owners []metav1.OwnerReference) error {
	desired := r.buildGroupDeployment(namespace, resolveQueryGroupSettings(sources), credentials, owners)
	desired.Annotations = map[string]string{deploymentHashAnnotation: computeDeploymentHash(desired)}

	current := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if errors.IsNotFound(err) {
		logf.FromContext(ctx).Info("Creating the query service Deployment", "deployment", desired.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if current.Labels[labelQueryGroup] == "" {
		return r.replaceLegacy(ctx, current, desired)
	}
	if current.Annotations[deploymentHashAnnotation] == desired.Annotations[deploymentHashAnnotation] &&
		maps.Equal(current.Labels, desired.Labels) && equality.Semantic.DeepEqual(current.OwnerReferences, owners) {
		return nil
	}
	logf.FromContext(ctx).Info("Updating the query service Deployment", "deployment", desired.Name)
	current.Spec, current.Labels, current.OwnerReferences = desired.Spec, desired.Labels, owners
	current.Annotations = mergeAnnotation(current.Annotations, deploymentHashAnnotation, desired.Annotations[deploymentHashAnnotation])
	return r.Update(ctx, current)
}

// mergeAnnotation sets one annotation without dropping the others.
func mergeAnnotation(annotations map[string]string, key, value string) map[string]string {
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[key] = value
	return annotations
}

// ensureGroupService creates the Service or brings it to the desired one.
func (r *RAGSourceReconciler) ensureGroupService(ctx context.Context, namespace string, sources []*kubemootv1alpha1.RAGSource, owners []metav1.OwnerReference) error {
	port := resolveQueryGroupSettings(sources).Port
	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: queryGroupName(namespace), Namespace: namespace, Labels: queryGroupLabels(namespace), OwnerReferences: owners,
		},
		Spec: corev1.ServiceSpec{
			Selector: queryGroupSelector(namespace),
			Ports:    []corev1.ServicePort{{Name: portNameHTTP, Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP}},
		},
	}
	current := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if current.Labels[labelQueryGroup] == "" {
		return r.replaceLegacy(ctx, current, desired)
	}
	if maps.Equal(current.Spec.Selector, desired.Spec.Selector) && servicePortsMatch(current.Spec.Ports, desired.Spec.Ports) &&
		maps.Equal(current.Labels, desired.Labels) && equality.Semantic.DeepEqual(current.OwnerReferences, owners) {
		return nil
	}
	current.Spec.Selector, current.Spec.Ports = desired.Spec.Selector, desired.Spec.Ports
	current.Labels, current.OwnerReferences = desired.Labels, owners
	return r.Update(ctx, current)
}

// servicePortsMatch compares the ports a Service was asked for with the ones it has, ignoring
// what the API server fills in.
func servicePortsMatch(current, desired []corev1.ServicePort) bool {
	if len(current) != len(desired) {
		return false
	}
	for i := range desired {
		if current[i].Name != desired[i].Name || current[i].Port != desired[i].Port ||
			current[i].TargetPort != desired[i].TargetPort || current[i].Protocol != desired[i].Protocol {
			return false
		}
	}
	return true
}

// deleteQueryGroup removes the Deployment, the Service and the registry of the namespace's Go
// query service, the ones that carry the group label. An object of the same name that is not the
// group's (a Python Deployment of a RAGSource named like the namespace) is left alone.
func (r *RAGSourceReconciler) deleteQueryGroup(ctx context.Context, namespace string) error {
	key := func(name string) types.NamespacedName { return types.NamespacedName{Namespace: namespace, Name: name} }
	objects := []client.Object{&appsv1.Deployment{}, &corev1.Service{}, &corev1.ConfigMap{}}
	names := []string{queryGroupName(namespace), queryGroupName(namespace), queryRegistryName(namespace)}
	for i, obj := range objects {
		if err := r.Get(ctx, key(names[i]), obj); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return err
		}
		if obj.GetLabels()[labelQueryGroup] == "" {
			continue
		}
		if err := r.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// replaceLegacy removes a per-RAGSource object of the Python engine that has the name the
// group needs (a RAGSource named like its namespace) and creates the group's object in its place.
func (r *RAGSourceReconciler) replaceLegacy(ctx context.Context, legacy, desired client.Object) error {
	logf.FromContext(ctx).Info("Replacing a per-RAGSource query service object that has the group's name",
		"kind", fmt.Sprintf("%T", legacy), "name", legacy.GetName())
	if err := r.Delete(ctx, legacy); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return r.Create(ctx, desired)
}
