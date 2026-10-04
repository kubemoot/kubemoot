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
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/crewmemory"
	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

// crewMemoryBucket is the NATS KV bucket holding crew working memory; keys are
// <ns>.<crew>.<topic>.<key>. Must match the agent-runtime CrewMemoryClient and the
// operator nats-streams-job. Purged per-crew on Crew deletion.
const crewMemoryBucket = crewmemory.Bucket

const (
	crewFinalizer       = "kubemoot.ai/crew-finalizer"
	crewGatewayPort     = int32(8080)
	crewLabelKey        = labelCrew
	manageNamespaceAnno = "kubemoot.ai/manage-namespace"
)

// CrewReconciler reconciles a Crew object
type CrewReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ConfigCache   *ConfigCache
	NATSPublisher *kubemootnats.Publisher
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=crews,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crews/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crews/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;create
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles Crew reconciliation
func (r *CrewReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	crew := &kubemootv1alpha1.Crew{}
	if err := r.Get(ctx, req.NamespacedName, crew); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !crew.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, crew)
	}

	if !controllerutil.ContainsFinalizer(crew, crewFinalizer) {
		if err := addFinalizer(ctx, r.Client, crew, crewFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		return requeueNow(), nil
	}

	log.Info("Reconciling Crew", "name", crew.Name)

	if _, err := crewscope.New(crew.Namespace, crew.Name); err != nil {
		return r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Crew name cannot scope NATS subjects: %v", err))
	}

	r.reconcileNamespaceLabel(ctx, crew)
	provenance := crewProvenance(crew)
	r.reconcileNamespaceCrews(ctx, crew, provenance)
	recordCrewRevision(crew, provenance, metav1.Now())
	r.replicateImagePullSecrets(ctx, crew)

	agents, coordinatorName, err := r.discoverAgents(ctx, crew)
	if err != nil {
		return r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Failed to list agents: %v", err))
	}

	crew.Status.AgentCount = int32(len(agents.Items))
	crew.Status.CoordinatorRef = coordinatorName

	if result, err, handled := r.reconcileDiscussion(ctx, crew); handled {
		return result, err
	}

	a := assessCrew(agents.Items, coordinatorName)
	return r.updateStatus(ctx, crew, a.phase, a.ready, a.message)
}

// reconcileNamespaceLabel records the Crew on its namespace when the Crew CR
// requests namespace management via the kubemoot.ai/manage-namespace annotation.
// The label is bookkeeping: deletion also requires the namespace to carry
// kubemoot.ai/managed-namespace=true, which only a Namespace-level author can set.
// Protected namespaces are never labeled. Crews in shared namespaces (e.g.,
// kubemoot) omit this annotation and are unaffected.
func (r *CrewReconciler) reconcileNamespaceLabel(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	if !crewRequestsNamespaceManagement(crew) || isProtectedNamespace(crew.Namespace) {
		return
	}

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: crew.Namespace}, ns); err != nil {
		return
	}
	if ns.Labels != nil && ns.Labels[crewLabelKey] == crew.Name {
		return
	}

	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	ns.Labels[crewLabelKey] = crew.Name
	ns.Labels[labelManagedBy] = managedByValue
	if err := r.Update(ctx, ns); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to label namespace")
	}
}

// replicateImagePullSecrets ensures image pull secrets exist in the crew namespace.
// The operator references these on pods, but the secrets must exist in each namespace.
func (r *CrewReconciler) replicateImagePullSecrets(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	for _, ref := range r.ConfigCache.GetImagePullSecrets() {
		replicateSecret(ctx, r.Client, ref.Name, crew.Namespace)
	}
}

// discoverAgents lists agents belonging to this crew and identifies the coordinator.
func (r *CrewReconciler) discoverAgents(ctx context.Context, crew *kubemootv1alpha1.Crew) (kubemootv1alpha1.AgentList, string, error) {
	var agents kubemootv1alpha1.AgentList
	if err := r.List(ctx, &agents, client.InNamespace(crew.Namespace), client.MatchingLabels{crewLabelKey: crew.Name}); err != nil {
		return agents, "", err
	}

	var coordinatorName string
	for i := range agents.Items {
		if isCoordinator(&agents.Items[i]) {
			coordinatorName = agents.Items[i].Name
			break
		}
	}
	return agents, coordinatorName, nil
}

// reconcileDiscussion deploys the discussion gateway when enabled and returns
// whether the caller should return early. When handled is true, the caller must
// return the provided result and error.
func (r *CrewReconciler) reconcileDiscussion(ctx context.Context, crew *kubemootv1alpha1.Crew) (ctrl.Result, error, bool) {
	if crew.Spec.Discussion == nil || !crew.Spec.Discussion.IsEnabled() {
		crew.Status.DiscussionEndpoint = ""
		return ctrl.Result{}, nil, false
	}

	if err := r.reconcileServiceAccount(ctx, crew); err != nil {
		res, err := r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Failed to reconcile ServiceAccount: %v", err))
		return res, err, true
	}
	if err := r.reconcileRBAC(ctx, crew); err != nil {
		res, err := r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Failed to reconcile RBAC: %v", err))
		return res, err, true
	}
	deployment, err := r.reconcileDeployment(ctx, crew)
	if err != nil {
		res, err := r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Failed to reconcile deployment: %v", err))
		return res, err, true
	}
	if err := r.reconcileService(ctx, crew); err != nil {
		res, err := r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Failed to reconcile service: %v", err))
		return res, err, true
	}
	if err := r.reconcileHTTPRoute(ctx, crew); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to reconcile HTTPRoute — gateway may not be configured")
	}

	r.setDiscussionEndpoint(crew)

	if deployment.Status.ReadyReplicas == 0 {
		res, err := r.updateStatus(ctx, crew, "Deploying", false, "Discussion gateway starting")
		return res, err, true
	}

	return ctrl.Result{}, nil, false
}

// setDiscussionEndpoint sets the external endpoint (the crew's namespaced path on
// the shared Gateway) or falls back to the in-cluster service address.
func (r *CrewReconciler) setDiscussionEndpoint(crew *kubemootv1alpha1.Crew) {
	gatewayHostname := os.Getenv("GATEWAY_HOSTNAME")
	if gatewayHostname != "" {
		crew.Status.DiscussionEndpoint = "https://" + gatewayHostname + scopeOf(crew).RoutePathPrefix()
	} else {
		gwName := r.gatewayName(crew)
		crew.Status.DiscussionEndpoint = fmt.Sprintf("http://%s.%s.svc.cluster.local", gwName, crew.Namespace)
	}
}

func (r *CrewReconciler) handleDeletion(ctx context.Context, crew *kubemootv1alpha1.Crew) (ctrl.Result, error) {
	r.deleteClusterRBAC(ctx, crew)
	r.removeNamespaceCrew(ctx, crew)
	r.deleteManagedNamespace(ctx, crew)
	r.purgeCrewMemory(ctx, crew)

	return ctrl.Result{}, removeFinalizer(ctx, r.Client, crew, crewFinalizer)
}

// deleteClusterRBAC removes the cluster-scoped RBAC resources for a crew; these
// are not garbage-collected by owner refs since they live outside any namespace.
func (r *CrewReconciler) deleteClusterRBAC(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	r.deleteClusterRBACNamed(ctx, r.rbacName(crew))
	r.deleteLegacyClusterRBAC(ctx, crew)
}

// deleteClusterRBACNamed deletes the ClusterRoleBinding and ClusterRole of one name.
func (r *CrewReconciler) deleteClusterRBACNamed(ctx context.Context, name string) {
	log := logf.FromContext(ctx)
	if err := r.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to delete ClusterRoleBinding", "name", name)
	}
	if err := r.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to delete ClusterRole", "name", name)
	}
}

// deleteLegacyClusterRBAC deletes the unscoped crew-<crew>-discussion RBAC when
// its binding grants this crew's gateway service account, so a crew of the same
// name in another namespace keeps its own.
func (r *CrewReconciler) deleteLegacyClusterRBAC(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	name := crewscope.LegacyClusterRBACName(crew.Name)
	crb := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: name}, crb); err != nil {
		if !errors.IsNotFound(err) {
			logf.FromContext(ctx).V(1).Info("Legacy ClusterRoleBinding not read", "name", name, "error", err.Error())
		}
		return
	}
	if !bindsServiceAccount(crb, r.gatewayName(crew), crew.Namespace) {
		return
	}
	r.deleteClusterRBACNamed(ctx, name)
}

// bindsServiceAccount reports whether the binding names the service account.
func bindsServiceAccount(crb *rbacv1.ClusterRoleBinding, name, namespace string) bool {
	for _, s := range crb.Subjects {
		if s.Kind == rbacv1.ServiceAccountKind && s.Name == name && s.Namespace == namespace {
			return true
		}
	}
	return false
}

// deleteManagedNamespace deletes the Crew's namespace only when the Crew asked
// for it (kubemoot.ai/manage-namespace annotation) and the namespace itself opted
// in (kubemoot.ai/managed-namespace=true). Protected and shared namespaces are
// never deleted.
func (r *CrewReconciler) deleteManagedNamespace(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: crew.Namespace}, ns); err != nil {
		return
	}
	if !r.isManagedNamespace(ns, crew) {
		return
	}
	log := logf.FromContext(ctx)
	log.Info("Deleting crew namespace", "namespace", crew.Namespace)
	if err := r.Delete(ctx, ns); err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to delete namespace", "namespace", crew.Namespace)
	}
}

// isManagedNamespace reports whether the operator may delete the namespace on
// behalf of the crew: the Crew requested it by annotation and the namespace
// consents through its own label (see namespaceDeletableForCrew).
func (r *CrewReconciler) isManagedNamespace(ns *corev1.Namespace, crew *kubemootv1alpha1.Crew) bool {
	return crewRequestsNamespaceManagement(crew) && namespaceDeletableForCrew(ns, crew.Name)
}

// purgeCrewMemory removes this crew's working memory — facts are crew-scoped
// (<ns>.<crew>.*) in a shared bucket, so they would otherwise orphan when the crew is
// deleted. Crew UPDATE keeps memory; only DELETE purges. See [[Crew Working Memory]].
func (r *CrewReconciler) purgeCrewMemory(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	if r.NATSPublisher == nil {
		return
	}
	log := logf.FromContext(ctx)
	if n, err := r.NATSPublisher.PurgeKVPrefix(crewMemoryBucket, scopeOf(crew).MemoryPrefix()); err != nil {
		log.Error(err, "Failed to purge crew working memory", "crew", crew.Name)
	} else if n > 0 {
		log.Info("Purged crew working memory on deletion", "crew", crew.Name, "facts", n)
	}
}

// gatewayName returns the discussion gateway resource name for a crew
func (r *CrewReconciler) gatewayName(crew *kubemootv1alpha1.Crew) string {
	return crew.Name + "-discussion"
}

// rbacName returns the RBAC resource name (cluster-scoped, unique per namespace and crew)
func (r *CrewReconciler) rbacName(crew *kubemootv1alpha1.Crew) string {
	return scopeOf(crew).ClusterRBACName()
}

// scopeOf returns the crew's namespace and name as a crewscope.Scope. Reconcile
// rejects a crew whose name is not a single NATS token before any name is built.
func scopeOf(crew *kubemootv1alpha1.Crew) crewscope.Scope {
	return crewscope.Scope{Namespace: crew.Namespace, Crew: crew.Name}
}

func (r *CrewReconciler) reconcileServiceAccount(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	name := r.gatewayName(crew)
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: crew.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = r.buildLabels(crew)
		return controllerutil.SetControllerReference(crew, sa, r.Scheme)
	})
	return err
}

func (r *CrewReconciler) reconcileRBAC(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	crName := r.rbacName(crew)
	saName := r.gatewayName(crew)

	// ClusterRole — read-only access to Agent CRs for coordinator discovery
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: crName},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cr, func() error {
		cr.Labels = r.buildLabels(crew)
		cr.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"kubemoot.ai"},
				Resources: []string{"agents"},
				Verbs:     []string{"get", "list"},
			},
		}
		return nil
	})
	if err != nil {
		return err
	}

	// ClusterRoleBinding
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: crName},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		crb.Labels = r.buildLabels(crew)
		crb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     crName,
		}
		crb.Subjects = []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: crew.Namespace,
			},
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.deleteLegacyClusterRBAC(ctx, crew)
	return nil
}

func (r *CrewReconciler) reconcileDeployment(ctx context.Context, crew *kubemootv1alpha1.Crew) (*appsv1.Deployment, error) {
	log := logf.FromContext(ctx)
	name := r.gatewayName(crew)

	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Name: name, Namespace: crew.Namespace}

	err := r.Get(ctx, key, deployment)
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	desired := r.buildDeployment(crew)

	if errors.IsNotFound(err) {
		log.Info("Creating discussion gateway Deployment", "crew", crew.Name)
		if err := controllerutil.SetControllerReference(crew, desired, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		return desired, nil
	}

	deployment.Spec = desired.Spec
	if err := r.Update(ctx, deployment); err != nil {
		if errors.IsConflict(err) {
			log.V(1).Info("Deployment update conflict, will retry")
			return deployment, nil
		}
		return nil, err
	}

	return deployment, nil
}

func (r *CrewReconciler) buildDeployment(crew *kubemootv1alpha1.Crew) *appsv1.Deployment {
	name := r.gatewayName(crew)
	replicas := int32(1)
	image := r.ConfigCache.GetDiscussionGatewayImage()
	labels := r.buildLabels(crew)

	// NATS URL from operator's own environment (set by helm values nats.url)
	natsURL := os.Getenv("NATS_URL")

	// Default resources
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("16Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
	}
	if crew.Spec.Discussion != nil && crew.Spec.Discussion.Resources != nil {
		resources = *crew.Spec.Discussion.Resources
	}

	imagePullSecrets := r.ConfigCache.GetImagePullSecrets()
	nonRoot := true
	noEscalation := false
	user := int64(65534)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: crew.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: name,
					ImagePullSecrets:   imagePullSecrets,
					SecurityContext: &corev1.PodSecurityContext{
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{
						{
							Name:            "discussion-gateway",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: crewGatewayPort, Protocol: corev1.ProtocolTCP},
							},
							Env: []corev1.EnvVar{
								{Name: "PORT", Value: fmt.Sprintf("%d", crewGatewayPort)},
								{Name: "NATS_URL", Value: natsURL},
								{Name: "COORDINATOR_CACHE_TTL", Value: "30s"},
								namespaceEnvVar(),
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &noEscalation,
								ReadOnlyRootFilesystem:   &nonRoot,
								RunAsNonRoot:             &nonRoot,
								RunAsUser:                &user,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: intstr.FromInt32(crewGatewayPort),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       30,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/ready",
										Port: intstr.FromInt32(crewGatewayPort),
									},
								},
								InitialDelaySeconds: 3,
								PeriodSeconds:       10,
							},
							Resources: resources,
						},
					},
				},
			},
		},
	}
}

func (r *CrewReconciler) reconcileService(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	name := r.gatewayName(crew)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: crew.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = r.buildLabels(crew)
		svc.Spec = corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: r.buildLabels(crew),
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt32(crewGatewayPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		}
		return controllerutil.SetControllerReference(crew, svc, r.Scheme)
	})
	return err
}

func (r *CrewReconciler) reconcileHTTPRoute(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	gatewayName := os.Getenv("GATEWAY_NAME")
	gatewayNamespace := os.Getenv("GATEWAY_NAMESPACE")
	gatewayHostname := os.Getenv("GATEWAY_HOSTNAME")

	if gatewayName == "" || gatewayHostname == "" {
		return nil // Gateway not configured — skip HTTPRoute
	}
	if gatewayNamespace == "" {
		gatewayNamespace = "default"
	}

	name := r.gatewayName(crew)
	scope := scopeOf(crew)
	pathPrefix := scope.RoutePathPrefix()

	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "gateway.networking.k8s.io",
		Version: "v1",
		Kind:    "HTTPRoute",
	})
	route.SetName(name)
	route.SetNamespace(crew.Namespace)
	route.SetLabels(r.buildLabels(crew))

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, route, func() error {
		route.Object["spec"] = map[string]interface{}{
			"parentRefs": []interface{}{
				map[string]interface{}{
					"name":      gatewayName,
					"namespace": gatewayNamespace,
				},
			},
			"hostnames": []interface{}{gatewayHostname},
			"rules": []interface{}{
				map[string]interface{}{
					"matches": []interface{}{
						map[string]interface{}{
							"path": map[string]interface{}{
								"type":  "PathPrefix",
								"value": pathPrefix,
							},
						},
					},
					// The gateway serves /api/v1/discussions/<crew>; the shared
					// route's namespaced prefix is rewritten to it.
					"filters": []interface{}{
						map[string]interface{}{
							"type": "URLRewrite",
							"urlRewrite": map[string]interface{}{
								"path": map[string]interface{}{
									"type":               "ReplacePrefixMatch",
									"replacePrefixMatch": scope.GatewayAPIPath(),
								},
							},
						},
					},
					"backendRefs": []interface{}{
						map[string]interface{}{
							"name": name,
							"port": int64(80),
						},
					},
				},
			},
		}
		return controllerutil.SetControllerReference(crew, route, r.Scheme)
	})
	return err
}

func (r *CrewReconciler) buildLabels(crew *kubemootv1alpha1.Crew) map[string]string {
	return map[string]string{
		labelName:      r.gatewayName(crew),
		labelInstance:  crew.Name,
		labelManagedBy: managedByValue,
		labelComponent: "discussion-gateway",
		crewLabelKey:   crew.Name,
	}
}

func (r *CrewReconciler) updateStatus(ctx context.Context, crew *kubemootv1alpha1.Crew, phase string, ready bool, message string) (ctrl.Result, error) {
	crew.Status.Phase = phase
	crew.Status.Ready = ready
	crew.Status.Message = message

	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             phase,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&crew.Status.Conditions, condition)

	if err := r.Status().Update(ctx, crew); err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 10_000_000_000}, nil // 10s
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *CrewReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.Crew{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		// A crew's readiness is derived from its agents' phases.
		Watches(&kubemootv1alpha1.Agent{}, enqueueCrewForAgent()).
		// Re-reconcile every Crew when KubemootConfig changes so a
		// discussionGateway image bump propagates to the Deployment
		// spec without per-CR annotation. See kubemootconfig_propagation.go.
		Watches(
			&kubemootv1alpha1.KubemootConfig{},
			enqueueAllOnKubemootConfigChange(mgr.GetClient(),
				func() client.ObjectList { return &kubemootv1alpha1.CrewList{} },
				"crew"),
		).
		Complete(r)
}
