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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/gomem"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/httporigin"
)

// crewMemoryBucket is the NATS KV bucket holding crew working memory; keys are
// <ns>.<crew>.<topic>.<key>. Must match the agent-runtime CrewMemoryClient and the
// operator nats-streams-job. Purged per-crew on Crew deletion.
const crewMemoryBucket = crewmemory.Bucket

const (
	crewFinalizer       = "kubemoot.ai/crew-finalizer"
	crewGatewayPort     = int32(8080)
	manageNamespaceAnno = "kubemoot.ai/manage-namespace"
)

// CrewReconciler reconciles a Crew object
type CrewReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ConfigCache   *ConfigCache
	NATSPublisher *kubemootnats.Publisher
	// Recorder emits events on a Crew whose deletion cleanup fails. Nil emits none.
	Recorder events.EventRecorder
	// NetPol applies the network policy of the crew's discussion gateway. Nil generates none.
	NetPol *NetPolicySync
	// RegisteredAccounts are the connected-account names this install has registered a client
	// for; a crew declaring any other name reports UnregisteredAccount. Nil registers none.
	RegisteredAccounts RegisteredAccounts
	// GatewayTLS issues the gateways' serving certificates. Nil serves the gateways plain.
	GatewayTLS *GatewayTLS
	// AccountBrokerURL is the liaison's account broker, given to the gateway of a crew whose askers
	// rule is connectedAccount. Empty gives none.
	AccountBrokerURL string
	// bus replaces NATSPublisher for discussion-state cleanup in tests.
	bus crewDiscussionBus
}

// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crews,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crews/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crews/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=agents,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;create;update
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

	if err := r.reconcileNamespaceLabel(ctx, crew); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileNetworkPolicies(ctx, crew); err != nil {
		return r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Failed to reconcile network policies: %v", err))
	}
	provenance := crewProvenance(crew)
	r.reconcileNamespaceCrews(ctx, crew, provenance)
	recordCrewRevision(crew, provenance, metav1.Now())
	r.replicateImagePullSecrets(ctx, crew)

	agents, coordinatorName, err := r.discoverAgents(ctx, crew)
	if err != nil {
		return r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Failed to list agents: %v", err))
	}

	r.adoptCrewAgents(ctx, crew, agents.Items)
	crew.Status.AgentCount = int32(len(agents.Items))
	crew.Status.CoordinatorRef = coordinatorName

	if result, err, handled := r.reconcileDiscussion(ctx, crew); handled {
		return result, err
	}

	r.reconcileRetention(crew)
	r.reconcileAccessConditions(ctx, crew)
	result, err := r.updateAssessedStatus(ctx, crew, assessCrew(agents.Items, coordinatorName))
	return r.withCertCheck(result, crew), err
}

// reconcileNetworkPolicies applies the policy that admits callers from outside the
// namespace to the crew's discussion gateway, or removes it when the crew runs no gateway.
func (r *CrewReconciler) reconcileNetworkPolicies(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	if r.NetPol == nil {
		return nil
	}
	var objects []client.Object
	if crew.Spec.Discussion != nil && crew.Spec.Discussion.IsEnabled() {
		g := netpol.GatewayProxy{Crew: crew.Name, Namespace: crew.Namespace}
		if crew.Spec.Network != nil {
			g.Allowed = crew.Spec.Network.AllowedIngress
		}
		objects = netpol.DiscussionGatewayIngress(r.NetPol.Resolved(), g)
		// The lease policy exists from the same reconcile that labels the pod, and the network
		// policies are applied before the Deployment, so the serving port is never closed.
		if crewKeepsTokens(crew) {
			objects = append(objects, netpol.DiscussionGatewayLeaseIngress(r.NetPol.Resolved(), crew.Name, crew.Namespace)...)
		}
	}
	return r.NetPol.Apply(ctx, crew, crew.Namespace, objects)
}

// reconcileNamespaceLabel puts the crew label on the namespace of a Crew, so the
// namespace is selected by the network policy baseline and by the cross-namespace
// allows. While generated network policies are on it runs for every Crew, however it was
// created, and never overwrites a label that is there (the same rule the namespace
// reconciler follows), so Crews from several sources do not rewrite each other's value.
// With them off it runs only for a Crew that asks for namespace management (the
// kubemoot.ai/manage-namespace annotation) and records that Crew on the namespace. The
// label is bookkeeping: deletion also requires the namespace to carry
// kubemoot.ai/managed-namespace=true, which only a Namespace-level author can set.
// Protected namespaces are never labeled.
func (r *CrewReconciler) reconcileNamespaceLabel(ctx context.Context, crew *kubemootv1alpha1.Crew) error {
	if isProtectedNamespace(crew.Namespace) {
		return nil
	}
	generated := r.NetPol.GeneratesPolicies()
	if !generated && !crewRequestsNamespaceManagement(crew) {
		return nil
	}

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: crew.Namespace}, ns); err != nil {
		return client.IgnoreNotFound(err)
	}
	if generated {
		crewName, err := firstCrewIn(ctx, r, crew.Namespace)
		if err != nil {
			return err
		}
		return ensureCrewLabel(ctx, r.Client, ns, crewName)
	}
	return r.recordManagedCrew(ctx, ns, crew)
}

// recordManagedCrew sets the crew label on a namespace to the Crew that asked for its
// management, replacing another value.
func (r *CrewReconciler) recordManagedCrew(ctx context.Context, ns *corev1.Namespace, crew *kubemootv1alpha1.Crew) error {
	if ns.Labels[labelCrew] == crew.Name {
		return nil
	}
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	ns.Labels[labelCrew] = crew.Name
	ns.Labels[labelManagedBy] = managedByValue
	// A failed write (a conflict with the namespace reconciler, for one) requeues the Crew.
	return r.Update(ctx, ns)
}

// replicateImagePullSecrets ensures image pull secrets exist in the crew namespace.
// The operator references these on pods, but the secrets must exist in each namespace.
func (r *CrewReconciler) replicateImagePullSecrets(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	for _, ref := range r.ConfigCache.GetImagePullSecrets() {
		replicateSecret(ctx, r.Client, ref.Name, crew.Namespace)
	}
	replicateNATSSecrets(ctx, r.Client, crew.Namespace)
}

// discoverAgents lists agents belonging to this crew and identifies the coordinator.
func (r *CrewReconciler) discoverAgents(ctx context.Context, crew *kubemootv1alpha1.Crew) (kubemootv1alpha1.AgentList, string, error) {
	agents, err := r.listCrewAgents(ctx, crew)
	if err != nil {
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
	if err := r.reconcileGatewayCert(ctx, crew); err != nil {
		res, err := r.updateStatus(ctx, crew, "Error", false, fmt.Sprintf("Failed to issue the gateway's TLS certificate: %v", err))
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
		res, err := r.updateStatus(ctx, crew, phaseDeploying, false, "Discussion gateway starting")
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
	agentNames := r.crewAgentNames(ctx, crew)
	if err := r.deleteCrewAgents(ctx, crew); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to delete Crew agents; keeping finalizer", "crew", crew.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if err := r.purgeCrewDiscussions(ctx, crew, agentNames); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to purge Crew discussion state; keeping finalizer", "crew", crew.Name)
		r.recordDeletionFailure(crew, err)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
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
	return crewGatewayName(crew)
}

// crewGatewayName is the name of a crew's discussion gateway resources, which is also the
// host of the Service the tool gateway leases from.
func crewGatewayName(crew *kubemootv1alpha1.Crew) string {
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
		cr.Rules = append([]rbacv1.PolicyRule{
			{
				APIGroups: []string{"kubemoot.ai"},
				Resources: []string{"agents"},
				Verbs:     []string{"get", "list"},
			},
		}, gatewayAccessRules(discussionAccess(crew))...)
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
			Kind:     kindClusterRole,
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
	r.GatewayTLS.apply(&desired.Spec.Template.Spec, name)
	applyAccountBroker(&desired.Spec.Template.Spec, crew, r.AccountBrokerURL)
	if err := r.applyCredentialLease(ctx, crew, desired); err != nil {
		return nil, err
	}

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

// gatewayAllowedOriginsEnv holds the browser origins whose pages may read a discussion
// gateway's streams. The chart sets it from discussionGateway.allowedOrigins and the
// operator passes it to every gateway as ALLOWED_ORIGINS; empty sets nothing.
const gatewayAllowedOriginsEnv = "DISCUSSION_GATEWAY_ALLOWED_ORIGINS"

// gatewayEnv is the environment of a crew's discussion gateway.
func gatewayEnv(natsURL string, access *kubemootv1alpha1.DiscussionAccess) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: envPort, Value: fmt.Sprintf("%d", crewGatewayPort)},
		{Name: "NATS_URL", Value: natsURL},
		{Name: "COORDINATOR_CACHE_TTL", Value: "30s"},
		namespaceEnvVar(),
	}
	origins, rejected := httporigin.Parse(os.Getenv(gatewayAllowedOriginsEnv))
	if len(rejected) > 0 {
		logf.Log.WithName("crew").Info("Ignoring discussionGateway.allowedOrigins entries that are not an exact origin",
			"entries", rejected)
	}
	if len(origins) > 0 {
		env = append(env, corev1.EnvVar{Name: "ALLOWED_ORIGINS", Value: strings.Join(origins, ",")})
	}
	return append(env, gatewayAccessEnv(access)...)
}

// defaultGatewayResources is what a discussion gateway asks for and may use when the Crew sets
// no spec.discussion.resources.
func defaultGatewayResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("5m"),
			corev1.ResourceMemory: resource.MustParse("48Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
}

func (r *CrewReconciler) buildDeployment(crew *kubemootv1alpha1.Crew) *appsv1.Deployment {
	name := r.gatewayName(crew)
	replicas := int32(1)
	image := r.ConfigCache.GetDiscussionGatewayImage()
	labels := r.buildLabels(crew)

	// NATS URL from operator's own environment (set by helm values nats.url)
	natsURL := os.Getenv("NATS_URL")

	// Default resources: measured use is a few MiB at rest and 43MiB at most. There is no CPU
	// limit: the gateway's start-up work is a short burst that a limit only slows.
	resources := defaultGatewayResources()
	if crew.Spec.Discussion != nil && crew.Spec.Discussion.Resources != nil {
		resources = *crew.Spec.Discussion.Resources
	}

	imagePullSecrets := r.ConfigCache.GetImagePullSecrets()
	nonRoot := true
	noEscalation := false
	user := int64(65534)

	d := &appsv1.Deployment{
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
							Name:            netpol.ComponentDiscussionGateway,
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{Name: portNameHTTP, ContainerPort: crewGatewayPort, Protocol: corev1.ProtocolTCP},
							},
							Env: gomem.With(append(append(gatewayEnv(natsURL, discussionAccess(crew)), gatewayIdentityEnv(crew)...), gatewayTrustedHopEnv(crew)...), resources),
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &noEscalation,
								ReadOnlyRootFilesystem:   &nonRoot,
								RunAsNonRoot:             &nonRoot,
								RunAsUser:                &user,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{dropAllCapability},
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: healthPath,
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
	applyNATSOptions(&d.Spec.Template.Spec, netpol.ComponentDiscussionGateway)
	return d
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
		ports := []corev1.ServicePort{
			{
				Name:       portNameHTTP,
				Port:       80,
				TargetPort: intstr.FromInt32(crewGatewayPort),
				Protocol:   corev1.ProtocolTCP,
			},
		}
		if r.GatewayTLS != nil {
			ports = append(ports, corev1.ServicePort{
				Name: portNameHTTPS, Port: gatewayTLSServicePort,
				TargetPort: intstr.FromInt32(gatewayTLSPort), Protocol: corev1.ProtocolTCP,
			})
		}
		if crewKeepsTokens(crew) {
			// The lease port, which the network policy keeps to the crew's tool gateway.
			ports = append(ports, corev1.ServicePort{
				Name: portNameInternal, Port: netpol.DiscussionGatewayLeasePort,
				TargetPort: intstr.FromInt32(netpol.DiscussionGatewayLeasePort), Protocol: corev1.ProtocolTCP,
			})
		}
		svc.Spec = corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: r.buildLabels(crew),
			Ports:    ports,
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
					jsonKeyName: gatewayName,
					"namespace": gatewayNamespace,
				},
			},
			"hostnames": []interface{}{gatewayHostname},
			"rules": []interface{}{
				map[string]interface{}{
					"matches": []interface{}{
						map[string]interface{}{
							"path": map[string]interface{}{
								jsonKeyType: "PathPrefix",
								"value":     pathPrefix,
							},
						},
					},
					// The gateway serves /api/v1/discussions/<crew>; the shared
					// route's namespaced prefix is rewritten to it.
					"filters": []interface{}{
						map[string]interface{}{
							jsonKeyType: "URLRewrite",
							"urlRewrite": map[string]interface{}{
								"path": map[string]interface{}{
									jsonKeyType:          "ReplacePrefixMatch",
									"replacePrefixMatch": scope.GatewayAPIPath(),
								},
							},
						},
					},
					"backendRefs": []interface{}{
						map[string]interface{}{
							jsonKeyName: name,
							"port":      int64(80),
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
		labelComponent: netpol.ComponentDiscussionGateway,
		labelCrew:      crew.Name,
	}
}

func (r *CrewReconciler) updateStatus(ctx context.Context, crew *kubemootv1alpha1.Crew, phase string, ready bool, message string) (ctrl.Result, error) {
	crew.Status.Phase = phase
	crew.Status.Ready = ready
	crew.Status.Message = message
	crew.Status.ObservedGeneration = crew.Generation

	// Ready says the crew is reconciled as declared. A crew that waits for capacity is
	// reconciled, so its reason is Reconciled, not its phase.
	reason := phase
	if ready {
		reason = reasonCrewReconciled
	}
	readyStatus := metav1.ConditionFalse
	if ready {
		readyStatus = metav1.ConditionTrue
	}
	setCrewCondition(crew, conditionTypeReady, readyStatus, reason, message)

	if err := r.Status().Update(ctx, crew); err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 10_000_000_000}, nil // 10s
	}
	return ctrl.Result{}, nil
}

// updateAssessedStatus writes the status the agents' assessment calls for: the Ready
// condition and, once the agents are assessed, the CapacityAvailable condition.
func (r *CrewReconciler) updateAssessedStatus(ctx context.Context, crew *kubemootv1alpha1.Crew, a crewAssessment) (ctrl.Result, error) {
	if a.capacity != nil {
		setCrewCapacity(crew, *a.capacity)
	}
	return r.updateStatus(ctx, crew, a.phase, a.ready, a.message)
}

// setCrewCapacity records the CapacityAvailable condition, which reports whether a model in
// the cluster catalog can run the crew's agents now, apart from the Ready condition.
func setCrewCapacity(crew *kubemootv1alpha1.Crew, c crewCapacity) {
	setCrewCondition(crew, conditionTypeCapacityAvailable, c.status, c.reason, c.message)
}

// setCrewCondition records one status condition at the Crew's current generation.
func setCrewCondition(crew *kubemootv1alpha1.Crew, conditionType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&crew.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: crew.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

// SetupWithManager sets up the controller with the Manager
func (r *CrewReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.Crew{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		// A gateway's serving certificate is made again when it is deleted or edited.
		Owns(&corev1.Secret{}, builder.WithPredicates(labelledInstallCALeaf())).
		Owns(&networkingv1.NetworkPolicy{}).
		// A crew's readiness is derived from its agents' phases.
		Watches(&kubemootv1alpha1.Agent{}, enqueueCrewForAgent()).
		// A tool server that takes the asking person's token, and the tool gateway that
		// leases it, change what a crew's gateway is told and what its condition says.
		Watches(&kubemootv1alpha1.MCPServer{}, enqueueCrewsInNamespace(mgr.GetClient())).
		Watches(&kubemootv1alpha1.MCPGateway{}, enqueueCrewsInNamespace(mgr.GetClient())).
		// Two crews that declare one account name for different services conflict, and the
		// condition of each follows the other.
		Watches(&kubemootv1alpha1.Crew{}, enqueueCrewsSharingAccounts(mgr.GetClient()),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// Re-reconcile every Crew when KubemootConfig changes so a
		// discussionGateway image bump propagates to the Deployment
		// spec without per-CR annotation. See kubemootconfig_propagation.go.
		Watches(
			&kubemootv1alpha1.KubemootConfig{},
			enqueueAllOnKubemootConfigChange(mgr.GetClient(), r.ConfigCache,
				func() client.ObjectList { return &kubemootv1alpha1.CrewList{} },
				"crew"),
		).
		// A rotated image pull secret reaches the crew namespace copies at once.
		Watches(&corev1.Secret{}, enqueueCrewsForSecret(mgr.GetClient(), r.ConfigCache),
			builder.WithPredicates(sourceSecretPredicate())).
		// A new gateway CA means every crew's certificate is made again.
		Watches(&corev1.Secret{}, enqueueCrewsOnCA(mgr.GetClient()), builder.WithPredicates(caSecretPredicate(r.installCA()))).
		Complete(r)
}
