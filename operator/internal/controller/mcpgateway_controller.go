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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

const (
	mcpGatewayFinalizer = "kubemoot.ai/mcpgateway-finalizer"
	// Legacy ContextForge image (for backward compatibility)
	defaultContextForgeImage = "ghcr.io/ibm/mcp-context-forge:1.0.0-BETA-1"
	defaultGatewayPort       = int32(8080)
	defaultContextForgePort  = int32(4444)
	gatewayHashAnnotation    = "kubemoot.ai/gateway-spec-hash"
)

// MCPGatewayReconciler reconciles a MCPGateway object
type MCPGatewayReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	HTTPClient    *http.Client
	ConfigCache   *ConfigCache
	NATSPublisher *kubemootnats.Publisher
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpgateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpgateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpgateways/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=ragsources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpqualitypolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpqualitypolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpserverreports,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpserverreports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpcatalogs,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpcatalogs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch

// Reconcile handles MCPGateway reconciliation
func (r *MCPGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the MCPGateway instance
	gateway := &kubemootv1alpha1.MCPGateway{}
	if err := r.Get(ctx, req.NamespacedName, gateway); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !gateway.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, gateway)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(gateway, mcpGatewayFinalizer) {
		if err := addFinalizer(ctx, r.Client, gateway, mcpGatewayFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		return requeueNow(), nil
	}

	log.Info("Reconciling MCPGateway", "name", gateway.Name, "implementation", gateway.Spec.Implementation)

	if result, done := r.reconcileGatewayPreReqs(ctx, gateway); done {
		return result, nil
	}

	// Reconcile Deployment
	deployment, err := r.reconcileDeployment(ctx, gateway)
	if err != nil {
		return r.updateStatus(ctx, gateway, "Error", false, fmt.Sprintf("Failed to reconcile deployment: %v", err))
	}

	// Reconcile Service
	if _, err := r.reconcileService(ctx, gateway); err != nil {
		return r.updateStatus(ctx, gateway, "Error", false, fmt.Sprintf("Failed to reconcile service: %v", err))
	}

	// Check if deployment is ready before registering MCPServers
	if deployment.Status.ReadyReplicas == 0 {
		return r.updateStatusFromDeployment(ctx, gateway, deployment, nil)
	}

	// List MCPServers matching selector
	mcpServers, err := r.listMCPServers(ctx, gateway)
	if err != nil {
		return r.updateStatus(ctx, gateway, "Error", false, fmt.Sprintf("Failed to list MCPServers: %v", err))
	}

	// Scrape feedback from gateway into MCPServerReports
	r.scrapeGatewayFeedback(ctx, gateway)

	// Register MCPServers with gateway (if gateway is ready)
	registeredServers := r.registerMCPServers(ctx, gateway, mcpServers)

	// Update status based on deployment state
	return r.updateStatusFromDeployment(ctx, gateway, deployment, registeredServers)
}

// reconcileGatewayPreReqs handles tool index RAGSource and dynamic MCPServer reconciliation.
// Returns (result, true) if the caller should return early.
func (r *MCPGatewayReconciler) reconcileGatewayPreReqs(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) (ctrl.Result, bool) {
	log := logf.FromContext(ctx)

	if gateway.Spec.Registries != nil && len(gateway.Spec.Registries.Sources) > 0 {
		ragSourceReady, err := r.ensureToolIndexRAGSource(ctx, gateway)
		if err != nil {
			result, _ := r.updateStatus(ctx, gateway, "Error", false, fmt.Sprintf("Failed to reconcile tool index RAGSource: %v", err))
			return result, true
		}
		if !ragSourceReady {
			result, _ := r.updateStatus(ctx, gateway, phaseIndexing, false, "Tool index RAGSource initializing")
			return result, true
		}
	}

	if len(gateway.Spec.CatalogRefs) > 0 {
		if err := r.reconcileDynamicMCPServers(ctx, gateway); err != nil {
			log.Error(err, "Failed to reconcile dynamic MCPServers")
		}
	}

	return ctrl.Result{}, false
}

// reconcileDeployment creates or updates the gateway Deployment
func (r *MCPGatewayReconciler) reconcileDeployment(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) (*appsv1.Deployment, error) {
	log := logf.FromContext(ctx)

	deployment := &appsv1.Deployment{}
	deploymentName := types.NamespacedName{
		Name:      gateway.Name,
		Namespace: gateway.Namespace,
	}

	err := r.Get(ctx, deploymentName, deployment)
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	// Build desired deployment
	desiredDeployment := r.buildDeployment(gateway)

	if errors.IsNotFound(err) {
		log.Info("Creating Gateway Deployment", "name", gateway.Name)
		if err := controllerutil.SetControllerReference(gateway, desiredDeployment, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, desiredDeployment); err != nil {
			return nil, err
		}
		return desiredDeployment, nil
	}

	// Check if update needed (simplified - just update spec)
	deployment.Spec = desiredDeployment.Spec
	if err := r.Update(ctx, deployment); err != nil {
		if errors.IsConflict(err) {
			log.V(1).Info("Deployment update conflict, will retry")
			return deployment, nil
		}
		return nil, err
	}

	return deployment, nil
}

// buildDeployment creates the Deployment spec for the gateway
func (r *MCPGatewayReconciler) buildDeployment(gateway *kubemootv1alpha1.MCPGateway) *appsv1.Deployment {
	replicas := int32(1)
	if gateway.Spec.Replicas != nil {
		replicas = *gateway.Spec.Replicas
	}

	impl := gateway.Spec.Implementation
	if impl == "" {
		impl = kubemootv1alpha1.ImplementationKubemoot
	}

	image, port, probePath, env := r.gatewayImplDefaults(gateway, impl)

	if gateway.Spec.Port != 0 {
		port = gateway.Spec.Port
		overridePortEnv(env, port)
	}

	if gateway.Spec.ContextForge != nil && gateway.Spec.ContextForge.Image != "" {
		image = gateway.Spec.ContextForge.Image
	}

	objLabels := map[string]string{
		labelName:      gateway.Name,
		labelInstance:  gateway.Name,
		labelManagedBy: managedByValue,
		labelComponent: componentMCPGateway,
	}

	// Auth configuration (ContextForge only for now)
	if impl == kubemootv1alpha1.ImplementationContextForge {
		env = append(env, buildGatewayAuthEnv(gateway)...)
	}

	runAsNonRoot := true
	seccompProfile := corev1.SeccompProfile{
		Type: corev1.SeccompProfileTypeRuntimeDefault,
	}

	container := buildGatewayContainer(gateway, image, probePath, port, env, runAsNonRoot, seccompProfile)

	// Image pull secrets - use spec if provided, otherwise fall back to KubemootConfig
	imagePullSecrets := r.gatewayImagePullSecrets(gateway)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gateway.Name,
			Namespace: gateway.Namespace,
			Labels:    objLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: objLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: objLabels,
				},
				Spec: corev1.PodSpec{
					Containers:                    []corev1.Container{container},
					TerminationGracePeriodSeconds: int64Ptr(30),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						SeccompProfile: &seccompProfile,
					},
					ImagePullSecrets: imagePullSecrets,
				},
			},
		},
	}

	return deployment
}

// overridePortEnv updates SERVER_PORT/PORT env vars in place to the given port.
func overridePortEnv(env []corev1.EnvVar, port int32) {
	for i := range env {
		if env[i].Name == envServerPort || env[i].Name == envPort {
			env[i].Value = fmt.Sprintf("%d", port)
		}
	}
}

// buildGatewayContainer constructs the gateway container spec including probes, resources, and security context.
func buildGatewayContainer(gateway *kubemootv1alpha1.MCPGateway, image, probePath string, port int32, env []corev1.EnvVar, runAsNonRoot bool, seccompProfile corev1.SeccompProfile) corev1.Container {
	container := corev1.Container{
		Name:            "mcp-gateway",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{
				Name:          portNameHTTP,
				ContainerPort: port,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: env,
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: probePath,
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 30,
			PeriodSeconds:       30,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: probePath,
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       10,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		},
	}

	// Set resource requirements. The default memory limit leaves room for the JVM's
	// non-heap regions: the buildpacks memory calculator sizes the heap from the limit
	// after reserving thread stacks (250 for a servlet app), code cache and metaspace,
	// and refuses to start the JVM when the limit cannot hold them.
	if gateway.Spec.Resources != nil {
		container.Resources = *gateway.Spec.Resources
	} else {
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
	}

	// Security context
	allowPrivilegeEscalation := false
	runAsUser := int64(1000)
	runAsGroup := int64(1000)

	container.SecurityContext = &corev1.SecurityContext{
		RunAsNonRoot:             &runAsNonRoot,
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		RunAsUser:                &runAsUser,
		RunAsGroup:               &runAsGroup,
		SeccompProfile:           &seccompProfile,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{dropAllCapability},
		},
	}

	return container
}

// gatewayImagePullSecrets returns the spec-provided image pull secrets, falling back to KubemootConfig.
func (r *MCPGatewayReconciler) gatewayImagePullSecrets(gateway *kubemootv1alpha1.MCPGateway) []corev1.LocalObjectReference {
	if len(gateway.Spec.ImagePullSecrets) > 0 {
		return gateway.Spec.ImagePullSecrets
	}
	return r.ConfigCache.GetImagePullSecrets()
}

// gatewayImplDefaults returns implementation-specific image, port, health path, and base env vars.
func (r *MCPGatewayReconciler) gatewayImplDefaults(gateway *kubemootv1alpha1.MCPGateway, impl kubemootv1alpha1.MCPGatewayImplementation) (string, int32, string, []corev1.EnvVar) {
	switch impl {
	case kubemootv1alpha1.ImplementationKubemoot:
		return r.ConfigCache.GetMcpGatewayImage(), defaultGatewayPort, "/actuator/health", []corev1.EnvVar{
			{Name: envServerPort, Value: fmt.Sprintf("%d", defaultGatewayPort)},
			{Name: "SPRING_APPLICATION_NAME", Value: gateway.Name},
		}
	case kubemootv1alpha1.ImplementationContextForge:
		env := []corev1.EnvVar{
			{Name: "HOST", Value: "0.0.0.0"},
			{Name: envPort, Value: fmt.Sprintf("%d", defaultContextForgePort)},
			{Name: "MCPGATEWAY_UI_ENABLED", Value: fmt.Sprintf("%t", gateway.Spec.AdminUIEnabled())},
			{Name: "MCPGATEWAY_ADMIN_API_ENABLED", Value: valueTrue},
		}
		databaseURL := "sqlite:///./mcp.db"
		if gateway.Spec.ContextForge != nil && gateway.Spec.ContextForge.DatabaseURL != "" {
			databaseURL = gateway.Spec.ContextForge.DatabaseURL
		}
		env = append(env, corev1.EnvVar{Name: "DATABASE_URL", Value: databaseURL})
		if gateway.Spec.ContextForge != nil && gateway.Spec.ContextForge.RedisURL != "" {
			env = append(env, corev1.EnvVar{Name: "REDIS_URL", Value: gateway.Spec.ContextForge.RedisURL})
		}
		return defaultContextForgeImage, defaultContextForgePort, healthPath, env
	default:
		return r.ConfigCache.GetMcpGatewayImage(), defaultGatewayPort, "/actuator/health", []corev1.EnvVar{
			{Name: envServerPort, Value: fmt.Sprintf("%d", defaultGatewayPort)},
		}
	}
}

// buildGatewayAuthEnv builds auth-related env vars for ContextForge gateways.
func buildGatewayAuthEnv(gateway *kubemootv1alpha1.MCPGateway) []corev1.EnvVar {
	if gateway.Spec.Auth == nil || !gateway.Spec.Auth.Enabled {
		return []corev1.EnvVar{{Name: "AUTH_REQUIRED", Value: valueFalse}}
	}

	env := []corev1.EnvVar{{Name: "AUTH_REQUIRED", Value: valueTrue}}

	if gateway.Spec.Auth.Type == "jwt" && gateway.Spec.Auth.JWTSecretRef != "" {
		env = append(env, corev1.EnvVar{
			Name: "JWT_SECRET_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: gateway.Spec.Auth.JWTSecretRef,
					},
					Key: "jwt-secret",
				},
			},
		})
	}

	if gateway.Spec.Auth.Type == "basic" && gateway.Spec.Auth.BasicAuth != nil && gateway.Spec.Auth.BasicAuth.SecretRef != "" {
		env = append(env,
			corev1.EnvVar{
				Name: "BASIC_AUTH_USER",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: gateway.Spec.Auth.BasicAuth.SecretRef,
						},
						Key: secretKeyUsername,
					},
				},
			},
			corev1.EnvVar{
				Name: "BASIC_AUTH_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: gateway.Spec.Auth.BasicAuth.SecretRef,
						},
						Key: secretKeyPassword,
					},
				},
			},
		)
	}

	return env
}

// ensureToolIndexRAGSource creates and monitors a RAGSource for MCP tool indexing
// Instead of directly managing indexer Jobs, we delegate to RAGSource which handles
// indexer Jobs and query service deployment.
func (r *MCPGatewayReconciler) ensureToolIndexRAGSource(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) (bool, error) {
	log := logf.FromContext(ctx)

	ragSourceName := fmt.Sprintf("%s-tools", gateway.Name)
	ragSource := &kubemootv1alpha1.RAGSource{}
	ragSourceNamespacedName := types.NamespacedName{
		Name:      ragSourceName,
		Namespace: gateway.Namespace,
	}

	err := r.Get(ctx, ragSourceNamespacedName, ragSource)
	if err != nil && !errors.IsNotFound(err) {
		return false, err
	}

	// RAGSource already exists - check status
	if err == nil {
		return syncToolIndexStatus(ctx, gateway, ragSource), nil
	}

	// RAGSource doesn't exist - create it
	log.Info("Creating tool index RAGSource", "ragSource", ragSourceName)
	desiredRAGSource := r.buildToolIndexRAGSource(gateway)

	if err := controllerutil.SetControllerReference(gateway, desiredRAGSource, r.Scheme); err != nil {
		return false, err
	}

	if err := r.Create(ctx, desiredRAGSource); err != nil {
		return false, err
	}

	if gateway.Status.CatalogSync == nil {
		gateway.Status.CatalogSync = &kubemootv1alpha1.CatalogSyncStatus{}
	}
	gateway.Status.CatalogSync.JobStatus = "Creating"

	return false, nil
}

// syncToolIndexStatus copies an existing tool index RAGSource's state into the
// gateway's catalog sync status and reports whether the index is ready. A ready
// RAGSource also supplies the query endpoint the gateway uses.
func syncToolIndexStatus(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, ragSource *kubemootv1alpha1.RAGSource) bool {
	log := logf.FromContext(ctx)
	if gateway.Status.CatalogSync == nil {
		gateway.Status.CatalogSync = &kubemootv1alpha1.CatalogSyncStatus{}
	}
	gateway.Status.CatalogSync.JobName = ragSource.Status.LastJobName

	if !ragSource.Status.Ready {
		log.Info("Tool index RAGSource not ready", "ragSource", ragSource.Name, "phase", ragSource.Status.Phase)
		gateway.Status.CatalogSync.JobStatus = ragSource.Status.Phase
		return false
	}

	log.Info("Tool index RAGSource is ready", "ragSource", ragSource.Name)
	gateway.Status.CatalogSync.JobStatus = "Succeeded"
	if ragSource.Status.IndexingStats != nil && ragSource.Status.IndexingStats.LastIndexed != nil {
		gateway.Status.CatalogSync.LastSync = ragSource.Status.IndexingStats.LastIndexed
	}
	gateway.Status.ToolIndexEndpoint = ragSource.Status.QueryEndpoint
	return true
}

// buildToolIndexRAGSource creates a RAGSource for MCP tool indexing
// The RAGSource controller will handle creating the indexer Job and query service
func (r *MCPGatewayReconciler) buildToolIndexRAGSource(gateway *kubemootv1alpha1.MCPGateway) *kubemootv1alpha1.RAGSource {
	ragSourceName := fmt.Sprintf("%s-tools", gateway.Name)

	// Use first registry source (can be extended to handle multiple)
	registry := gateway.Spec.Registries.Sources[0]

	// Determine registry type (default to mcp-run)
	registryType := "mcp-run"
	if registry.Type != "" {
		registryType = registry.Type
	}

	// Build MCPRegistry source config for RAGSource
	mcpRegistrySource := &kubemootv1alpha1.RAGMCPRegistrySource{
		URL:  registry.URL,
		Type: registryType,
	}

	// Add filter if specified
	if registry.Filter != nil && len(registry.Filter.Categories) > 0 {
		mcpRegistrySource.Filter = &kubemootv1alpha1.RAGMCPRegistryFilter{
			Categories: registry.Filter.Categories,
		}
	}

	// Add auth if specified
	if registry.AuthSecretRef != "" {
		mcpRegistrySource.AuthSecretRef = registry.AuthSecretRef
	}

	// Build VectorStore config from ToolIndex
	vectorStoreConfig := buildToolIndexVectorStore(gateway)

	// Determine embedding model reference (name of an EmbeddingModel CR)
	embeddingModelRef := r.ConfigCache.GetEmbeddingModel()
	if gateway.Spec.ToolIndex != nil && gateway.Spec.ToolIndex.EmbeddingModel.Model != "" {
		embeddingModelRef = gateway.Spec.ToolIndex.EmbeddingModel.Model
	}

	// Build indexer config
	indexerConfig := buildToolIndexIndexerConfig(gateway)

	// Query service is enabled by default for tool search
	queryServiceEnabled := true
	queryServiceConfig := &kubemootv1alpha1.QueryServiceConfig{
		Enabled:  &queryServiceEnabled,
		Replicas: 1,
	}

	objLabels := map[string]string{
		labelName:      ragSourceName,
		labelInstance:  gateway.Name,
		labelManagedBy: managedByValue,
		labelComponent: "mcp-tool-index",
	}

	ragSource := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ragSourceName,
			Namespace: gateway.Namespace,
			Labels:    objLabels,
		},
		Spec: kubemootv1alpha1.RAGSourceSpec{
			Source: kubemootv1alpha1.SourceConfig{
				Type:        kubemootv1alpha1.RAGSourceTypeMCPRegistry,
				MCPRegistry: mcpRegistrySource,
			},
			VectorStore:       vectorStoreConfig,
			EmbeddingModelRef: embeddingModelRef,
			Indexer:           indexerConfig,
			QueryService:      queryServiceConfig,
		},
	}

	return ragSource
}

// buildToolIndexVectorStore builds the VectorStore config for the tool index from the gateway's ToolIndex spec.
// Returns a zero-value config when no ToolIndex is specified.
func buildToolIndexVectorStore(gateway *kubemootv1alpha1.MCPGateway) kubemootv1alpha1.VectorStoreConfig {
	if gateway.Spec.ToolIndex == nil {
		return kubemootv1alpha1.VectorStoreConfig{}
	}

	vs := gateway.Spec.ToolIndex.VectorStore
	collection := gateway.Spec.ToolIndex.Collection
	if collection == "" {
		collection = "mcp_tools"
	}

	dimensions := int32(768)
	if gateway.Spec.ToolIndex.EmbeddingModel.Dimensions > 0 {
		dimensions = int32(gateway.Spec.ToolIndex.EmbeddingModel.Dimensions)
	}

	return kubemootv1alpha1.VectorStoreConfig{
		Type:       kubemootv1alpha1.VectorStorePgvector,
		Endpoint:   fmt.Sprintf("postgres://%s:%d/%s", vs.Host, vs.Port, vs.Database),
		SecretRef:  vs.SecretRef,
		Collection: collection,
		Dimensions: dimensions,
	}
}

// buildToolIndexIndexerConfig builds the IndexerConfig for the tool index from the gateway's Registries spec.
// Returns nil when no Registries are specified.
func buildToolIndexIndexerConfig(gateway *kubemootv1alpha1.MCPGateway) *kubemootv1alpha1.IndexerConfig {
	if gateway.Spec.Registries == nil {
		return nil
	}

	indexerConfig := &kubemootv1alpha1.IndexerConfig{}
	if gateway.Spec.Registries.IndexerImage != "" {
		indexerConfig.Image = gateway.Spec.Registries.IndexerImage
	}
	if gateway.Spec.Registries.SyncInterval != "" {
		indexerConfig.Schedule = gateway.Spec.Registries.SyncInterval
	}
	return indexerConfig
}

// reconcileService creates or updates the gateway Service
func (r *MCPGatewayReconciler) reconcileService(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) (*corev1.Service, error) {
	log := logf.FromContext(ctx)

	service := &corev1.Service{}
	serviceName := types.NamespacedName{
		Name:      gateway.Name,
		Namespace: gateway.Namespace,
	}

	err := r.Get(ctx, serviceName, service)
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	port := defaultGatewayPort
	if gateway.Spec.Port != 0 {
		port = gateway.Spec.Port
	}

	objLabels := map[string]string{
		labelName:      gateway.Name,
		labelInstance:  gateway.Name,
		labelManagedBy: managedByValue,
		labelComponent: componentMCPGateway,
	}

	desiredService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gateway.Name,
			Namespace: gateway.Namespace,
			Labels:    objLabels,
		},
		Spec: corev1.ServiceSpec{
			Selector: objLabels,
			Ports: []corev1.ServicePort{
				{
					Name:       portNameHTTP,
					Port:       port,
					TargetPort: intstr.FromInt32(port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	if errors.IsNotFound(err) {
		log.Info("Creating Gateway Service", "name", gateway.Name)
		if err := controllerutil.SetControllerReference(gateway, desiredService, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, desiredService); err != nil {
			return nil, err
		}
		return desiredService, nil
	}

	// Update existing service
	service.Spec.Selector = desiredService.Spec.Selector
	service.Spec.Ports = desiredService.Spec.Ports
	if err := r.Update(ctx, service); err != nil {
		return nil, err
	}

	return service, nil
}

// listMCPServers lists MCPServers matching the gateway's selector
func (r *MCPGatewayReconciler) listMCPServers(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) ([]kubemootv1alpha1.MCPServer, error) {
	mcpServerList := &kubemootv1alpha1.MCPServerList{}

	listOpts := []client.ListOption{
		client.InNamespace(gateway.Namespace),
	}

	// Apply label selector if specified
	if gateway.Spec.MCPServerSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(gateway.Spec.MCPServerSelector)
		if err != nil {
			return nil, err
		}
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: selector})
	}

	if err := r.List(ctx, mcpServerList, listOpts...); err != nil {
		return nil, err
	}

	// Filter to only ready MCPServers
	var readyServers []kubemootv1alpha1.MCPServer
	for _, mcp := range mcpServerList.Items {
		if mcp.Status.Ready && mcp.Status.Endpoint != "" {
			readyServers = append(readyServers, mcp)
		}
	}

	return readyServers, nil
}

// getAdminAPIToken retrieves the bearer token for Admin API from secret
func (r *MCPGatewayReconciler) getAdminAPIToken(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) (string, error) {
	if gateway.Spec.ContextForge == nil || gateway.Spec.ContextForge.AdminAPISecretRef == "" {
		return "", nil // No auth required
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: gateway.Namespace,
		Name:      gateway.Spec.ContextForge.AdminAPISecretRef,
	}, secret); err != nil {
		return "", fmt.Errorf("failed to get admin API secret: %w", err)
	}

	token, ok := secret.Data["bearer-token"]
	if !ok {
		return "", fmt.Errorf("bearer-token key not found in secret %s", gateway.Spec.ContextForge.AdminAPISecretRef)
	}

	return string(token), nil
}

// syncMCPServerRegistrations syncs MCPServer registrations with gateway, handling adds and removes
func (r *MCPGatewayReconciler) syncMCPServerRegistrations(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, mcpServers []kubemootv1alpha1.MCPServer) []string {
	log := logf.FromContext(ctx)

	// Get bearer token for Admin API
	bearerToken, err := r.getAdminAPIToken(ctx, gateway)
	if err != nil {
		log.Error(err, "Failed to get Admin API token, continuing without auth")
	}

	port := defaultGatewayPort
	if gateway.Spec.Port != 0 {
		port = gateway.Spec.Port
	}

	// Build set of MCPServers that should be registered
	currentServers := make(map[string]*kubemootv1alpha1.MCPServer)
	for i := range mcpServers {
		mcp := &mcpServers[i]
		// Skip if registry is explicitly disabled
		if mcp.Spec.Registry != nil && !kubemootv1alpha1.BoolOrTrue(mcp.Spec.Registry.Enabled) {
			continue
		}
		currentServers[mcp.Name] = mcp
	}

	// Build set of previously registered servers
	previousServers := make(map[string]bool)
	for _, name := range gateway.Status.MCPServers {
		previousServers[name] = true
	}

	var registered []string

	// Unregister servers no longer in current set
	r.unregisterRemovedMCPServers(ctx, gateway, previousServers, currentServers, port, bearerToken)

	// Register current servers
	for name, mcp := range currentServers {
		if err := r.registerMCPServerWithGateway(ctx, gateway, mcp, port, bearerToken); err != nil {
			log.Error(err, "Failed to register MCPServer with gateway", "mcpServer", name)
			continue
		}
		registered = append(registered, name)
		delete(previousServers, name) // Mark as processed
	}

	return registered
}

// unregisterRemovedMCPServers unregisters servers that were previously registered but are no longer current.
func (r *MCPGatewayReconciler) unregisterRemovedMCPServers(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, previousServers map[string]bool, currentServers map[string]*kubemootv1alpha1.MCPServer, port int32, bearerToken string) {
	log := logf.FromContext(ctx)

	for name := range previousServers {
		if _, exists := currentServers[name]; !exists {
			if err := r.unregisterMCPServer(ctx, gateway, name, port, bearerToken); err != nil {
				log.Error(err, "Failed to unregister MCPServer", "mcpServer", name)
			} else {
				log.Info("Unregistered MCPServer from gateway", "mcpServer", name)
			}
		}
	}
}

// registerMCPServers registers MCPServers with the gateway via Admin API
func (r *MCPGatewayReconciler) registerMCPServers(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, mcpServers []kubemootv1alpha1.MCPServer) []string {
	// Use new sync function that handles both registration and unregistration
	return r.syncMCPServerRegistrations(ctx, gateway, mcpServers)
}

// httpClientOrDefault returns the injected HTTP client, or one with a 10s timeout.
func (r *MCPGatewayReconciler) httpClientOrDefault() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// gatewayRegistrationURL is the admin endpoint that registers an MCP server with the
// gateway: /admin/servers on the Kubemoot gateway, /gateways on ContextForge.
func gatewayRegistrationURL(gateway *kubemootv1alpha1.MCPGateway, impl kubemootv1alpha1.MCPGatewayImplementation, port int32) string {
	path := "gateways"
	if impl == kubemootv1alpha1.ImplementationKubemoot {
		path = "admin/servers"
	}
	return fmt.Sprintf("http://%s.%s.svc:%d/%s", gateway.Name, gateway.Namespace, port, path)
}

// registerMCPServerWithGateway makes HTTP call to register MCP server
func (r *MCPGatewayReconciler) registerMCPServerWithGateway(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, mcp *kubemootv1alpha1.MCPServer, port int32, bearerToken string) error {
	impl := gateway.Spec.Implementation
	if impl == "" {
		impl = kubemootv1alpha1.ImplementationKubemoot
	}

	adminURL := gatewayRegistrationURL(gateway, impl, port)

	payload := r.buildRegistrationPayload(ctx, impl, mcp)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	httpClient := r.httpClientOrDefault()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Add bearer token authentication for Admin API (ContextForge)
	if bearerToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", bearerToken))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body for error details
	body, _ := io.ReadAll(resp.Body)

	// Accept 200, 201, or 409 (already exists)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("failed to register MCP server: %s - %s", resp.Status, string(body))
	}

	return nil
}

// buildRegistrationPayload builds the JSON payload for registering an MCPServer with a gateway.
func (r *MCPGatewayReconciler) buildRegistrationPayload(ctx context.Context, impl kubemootv1alpha1.MCPGatewayImplementation, mcp *kubemootv1alpha1.MCPServer) map[string]interface{} {
	transport := string(mcp.Spec.Transport)
	if transport == "" {
		transport = "sse"
	}
	if impl == kubemootv1alpha1.ImplementationContextForge {
		transport = strings.ToUpper(transport)
	}

	payload := map[string]interface{}{
		jsonKeyName: mcp.Name,
		"url":       mcp.Status.Endpoint,
		"transport": transport,
	}

	if impl == kubemootv1alpha1.ImplementationKubemoot && len(mcp.Spec.ToolOverrides) > 0 {
		payload["toolOverrides"] = mcp.Spec.ToolOverrides
	}

	if impl == kubemootv1alpha1.ImplementationContextForge && mcp.Spec.Registry != nil && mcp.Spec.Registry.AuthSecretRef != "" {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: mcp.Namespace,
			Name:      mcp.Spec.Registry.AuthSecretRef,
		}, secret); err == nil {
			if token, ok := secret.Data["bearer-token"]; ok {
				payload["auth_type"] = "bearer"
				payload["auth_token"] = string(token)
			}
		}
	}

	return payload
}

// unregisterMCPServer removes an MCPServer from the gateway registry
func (r *MCPGatewayReconciler) unregisterMCPServer(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, mcpName string, port int32, bearerToken string) error {
	impl := gateway.Spec.Implementation
	if impl == "" {
		impl = kubemootv1alpha1.ImplementationKubemoot
	}

	// Determine API endpoint based on implementation
	var adminURL string
	switch impl {
	case kubemootv1alpha1.ImplementationKubemoot:
		// Kubemoot Spring gateway uses /admin/servers/{id}
		// Note: uses server ID, not name - for now we use name as ID
		adminURL = fmt.Sprintf("http://%s.%s.svc:%d/admin/servers/%s", gateway.Name, gateway.Namespace, port, mcpName)
	default:
		// ContextForge uses /gateways/{name}
		adminURL = fmt.Sprintf("http://%s.%s.svc:%d/gateways/%s", gateway.Name, gateway.Namespace, port, mcpName)
	}

	httpClient := r.httpClientOrDefault()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, adminURL, nil)
	if err != nil {
		return err
	}

	// Add bearer token authentication for Admin API (ContextForge)
	if bearerToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", bearerToken))
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// Accept 200, 204 (success) or 404 (already unregistered)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to unregister MCP server: %s - %s", resp.Status, string(body))
	}

	return nil
}

// scrapeGatewayFeedback fetches runtime feedback from the gateway and records it in MCPServerReports
func (r *MCPGatewayReconciler) scrapeGatewayFeedback(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) {
	log := logf.FromContext(ctx)

	port := defaultGatewayPort
	if gateway.Spec.Port != 0 {
		port = gateway.Spec.Port
	}

	feedbackURL := fmt.Sprintf("http://%s.%s.svc:%d/admin/feedback", gateway.Name, gateway.Namespace, port)

	httpClient := r.httpClientOrDefault()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedbackURL, nil)
	if err != nil {
		log.V(1).Info("Failed to create feedback request", "error", err)
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.V(1).Info("Failed to scrape gateway feedback", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return
	}

	// Parse feedback: map of serverId -> []feedbackEntry
	var feedback map[string][]feedbackEntry
	if err := json.NewDecoder(resp.Body).Decode(&feedback); err != nil {
		log.V(1).Info("Failed to parse gateway feedback", "error", err)
		return
	}

	// Process each server's feedback into MCPServerReports
	for _, entries := range feedback {
		if len(entries) == 0 {
			continue
		}
		r.processFeedbackEntries(ctx, gateway.Namespace, entries)
	}
}

// feedbackEntry represents a single feedback entry from a gateway.
type feedbackEntry struct {
	ServerName   string `json:"serverName"`
	Phase        string `json:"phase"`
	Success      bool   `json:"success"`
	ErrorMessage string `json:"errorMessage"`
	ToolsFound   int    `json:"toolsFound"`
	Timestamp    string `json:"timestamp"`
}

// processFeedbackEntries records feedback entries into an MCPServerReport for a single server.
func (r *MCPGatewayReconciler) processFeedbackEntries(ctx context.Context, namespace string, entries []feedbackEntry) {
	log := logf.FromContext(ctx)

	serverName := entries[0].ServerName
	reportName := sanitizeK8sName(serverName)

	report := &kubemootv1alpha1.MCPServerReport{}
	err := r.Get(ctx, types.NamespacedName{Name: reportName, Namespace: namespace}, report)
	if errors.IsNotFound(err) {
		report = &kubemootv1alpha1.MCPServerReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      reportName,
				Namespace: namespace,
				Labels: map[string]string{
					labelManagedBy: managedByValue,
				},
			},
			Spec: kubemootv1alpha1.MCPServerReportSpec{
				ServerName: serverName,
			},
		}
		if err := r.Create(ctx, report); err != nil {
			log.Error(err, "Failed to create MCPServerReport from feedback", "server", serverName)
			return
		}
	} else if err != nil {
		return
	}

	for _, entry := range entries {
		now := metav1.Now()
		trial := kubemootv1alpha1.TrialRecord{
			Transport:  string(kubemootv1alpha1.TransportHTTP),
			TestedAt:   &now,
			Phase:      entry.Phase,
			Success:    entry.Success,
			ToolsFound: entry.ToolsFound,
		}
		if !entry.Success {
			trial.ErrorMessage = entry.ErrorMessage
		}
		report.Status.Trials = append(report.Status.Trials, trial)
	}

	if len(report.Status.Trials) > 20 {
		report.Status.Trials = report.Status.Trials[len(report.Status.Trials)-20:]
	}

	if err := r.Status().Update(ctx, report); err != nil {
		log.Error(err, "Failed to update MCPServerReport with feedback", "server", serverName)
		return
	}
	log.V(1).Info("Recorded gateway feedback", "server", serverName, "entries", len(entries))
	if r.NATSPublisher != nil {
		for _, entry := range entries {
			_ = r.NATSPublisher.Publish(
				fmt.Sprintf("kubemoot.gateway.feedback.%s", reportName),
				map[string]interface{}{
					jsonKeyServer:  serverName,
					jsonKeyPhase:   entry.Phase,
					jsonKeySuccess: entry.Success,
					"tools":        entry.ToolsFound,
					"error":        entry.ErrorMessage,
				},
			)
		}
	}
}

// handleDeletion handles MCPGateway deletion
func (r *MCPGatewayReconciler) handleDeletion(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(gateway, mcpGatewayFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Handling MCPGateway deletion", "name", gateway.Name)

	// Deployment and Service will be garbage collected via owner references

	// Remove finalizer
	if err := removeFinalizer(ctx, r.Client, gateway, mcpGatewayFinalizer); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("MCPGateway deleted successfully", "name", gateway.Name)
	return ctrl.Result{}, nil
}

// updateStatusFromDeployment updates MCPGateway status based on Deployment state
func (r *MCPGatewayReconciler) updateStatusFromDeployment(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, deployment *appsv1.Deployment, registeredServers []string) (ctrl.Result, error) {
	port := defaultGatewayPort
	if gateway.Spec.Port != 0 {
		port = gateway.Spec.Port
	}

	// Determine phase and readiness
	phase := phaseDeploying
	ready := false
	message := "Deployment in progress"

	if deployment.Status.ReadyReplicas > 0 && deployment.Status.ReadyReplicas == deployment.Status.Replicas {
		phase = phaseReady
		ready = true
		message = fmt.Sprintf("%d/%d replicas ready, %d MCPServers registered", deployment.Status.ReadyReplicas, deployment.Status.Replicas, len(registeredServers))
	} else if deployment.Status.ReadyReplicas > 0 {
		phase = "Degraded"
		message = fmt.Sprintf("%d/%d replicas ready", deployment.Status.ReadyReplicas, deployment.Status.Replicas)
	}

	// Build endpoints
	endpoint := fmt.Sprintf("http://%s.%s.svc:%d", gateway.Name, gateway.Namespace, port)
	adminEndpoint := ""
	if gateway.Spec.AdminUIEnabled() {
		adminEndpoint = fmt.Sprintf("http://%s.%s.svc:%d/admin", gateway.Name, gateway.Namespace, port)
	}

	return r.updateStatus(ctx, gateway, phase, ready, message,
		gwWithEndpoint(endpoint),
		gwWithAdminEndpoint(adminEndpoint),
		gwWithReplicas(deployment.Status.AvailableReplicas),
		gwWithMCPServers(registeredServers))
}

// Status update options for MCPGateway
type gatewayStatusOption func(*kubemootv1alpha1.MCPGatewayStatus)

func gwWithEndpoint(endpoint string) gatewayStatusOption {
	return func(s *kubemootv1alpha1.MCPGatewayStatus) {
		s.Endpoint = endpoint
	}
}

func gwWithAdminEndpoint(endpoint string) gatewayStatusOption {
	return func(s *kubemootv1alpha1.MCPGatewayStatus) {
		s.AdminEndpoint = endpoint
	}
}

func gwWithReplicas(available int32) gatewayStatusOption {
	return func(s *kubemootv1alpha1.MCPGatewayStatus) {
		s.AvailableReplicas = available
	}
}

func gwWithMCPServers(servers []string) gatewayStatusOption {
	return func(s *kubemootv1alpha1.MCPGatewayStatus) {
		s.MCPServers = servers
		s.RegisteredServers = len(servers)
	}
}

// updateStatus updates the MCPGateway status
func (r *MCPGatewayReconciler) updateStatus(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, phase string, ready bool, message string, opts ...gatewayStatusOption) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	gateway.Status.Phase = phase
	gateway.Status.Ready = ready
	gateway.Status.Message = message

	// Apply options
	for _, opt := range opts {
		opt(&gateway.Status)
	}

	// Set condition
	condition := metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             phase,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&gateway.Status.Conditions, condition)

	if err := r.Status().Update(ctx, gateway); err != nil {
		log.Error(err, "Failed to update MCPGateway status")
		return ctrl.Result{}, err
	}

	// Requeue to check deployment progress or re-sync MCPServers
	if !ready {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Requeue periodically to sync MCPServer registrations
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// findGatewaysForMCPServer maps MCPServer changes to MCPGateway reconcile requests
func (r *MCPGatewayReconciler) findGatewaysForMCPServer(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)
	mcp := obj.(*kubemootv1alpha1.MCPServer)

	// Find all MCPGateways in the same namespace
	gatewayList := &kubemootv1alpha1.MCPGatewayList{}
	if err := r.List(ctx, gatewayList, client.InNamespace(mcp.Namespace)); err != nil {
		log.Error(err, "Failed to list MCPGateways")
		return nil
	}

	var requests []reconcile.Request
	for _, gw := range gatewayList.Items {
		// Check if this MCPServer matches the gateway's selector
		if gw.Spec.MCPServerSelector != nil {
			selector, err := metav1.LabelSelectorAsSelector(gw.Spec.MCPServerSelector)
			if err != nil {
				continue
			}
			if !selector.Matches(labels.Set(mcp.Labels)) {
				continue
			}
		}
		// MCPServer matches (or gateway has no selector), trigger reconcile
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      gw.Name,
				Namespace: gw.Namespace,
			},
		})
	}

	return requests
}

// ============================================================================
// Dynamic MCP Server Provisioning from Catalogs
// ============================================================================

// reconcileDynamicMCPServers provisions MCPServers from catalog discoveries
func (r *MCPGatewayReconciler) reconcileDynamicMCPServers(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) error {
	log := logf.FromContext(ctx)

	// Collect all discovered servers from catalogs
	allDiscoveredServers := r.collectDiscoveredServers(ctx, gateway)
	log.Info("Discovered servers from catalogs", "count", len(allDiscoveredServers), "catalogCount", len(gateway.Spec.CatalogRefs))

	// Apply quality policy filtering (if specified)
	allowedServers, err := r.filterServersByQualityPolicy(ctx, gateway, allDiscoveredServers)
	if err != nil {
		return err
	}

	log.Info("Servers passing quality policy", "allowed", len(allowedServers), "total", len(allDiscoveredServers))

	// Get existing dynamic MCPServers (owned by this gateway)
	existingServers := &kubemootv1alpha1.MCPServerList{}
	if err := r.List(ctx, existingServers,
		client.InNamespace(gateway.Namespace),
		client.MatchingLabels{
			annoDynamic: valueTrue,
			annoGateway: gateway.Name,
		},
	); err != nil {
		return fmt.Errorf("failed to list existing dynamic MCPServers: %w", err)
	}

	// Build map of allowed server names for efficient lookup (using sanitized names for K8s comparison)
	allowedServerNames := make(map[string]kubemootv1alpha1.DiscoveredServer)
	for _, server := range allowedServers {
		allowedServerNames[sanitizeK8sName(server.Name)] = server
	}

	r.cleanupRemovedDynamicMCPServers(ctx, existingServers.Items, allowedServerNames)

	// Create or update MCPServers for allowed servers
	for _, server := range allowedServers {
		if err := r.ensureDynamicMCPServer(ctx, gateway, server); err != nil {
			log.Error(err, "Failed to ensure dynamic MCPServer", "server", server.Name)
			// Continue with other servers
		}
	}

	return nil
}

// collectDiscoveredServers gathers all discovered servers across the gateway's referenced catalogs.
func (r *MCPGatewayReconciler) collectDiscoveredServers(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway) []kubemootv1alpha1.DiscoveredServer {
	log := logf.FromContext(ctx)

	var allDiscoveredServers []kubemootv1alpha1.DiscoveredServer
	for _, catalogName := range gateway.Spec.CatalogRefs {
		catalog := &kubemootv1alpha1.MCPCatalog{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      catalogName,
			Namespace: gateway.Namespace,
		}, catalog); err != nil {
			log.Error(err, "Failed to get catalog", "catalog", catalogName)
			continue
		}

		// Add discovered servers from this catalog
		allDiscoveredServers = append(allDiscoveredServers, catalog.Status.DiscoveredServers...)
	}

	return allDiscoveredServers
}

// filterServersByQualityPolicy applies the gateway's quality policy (if any) to the discovered servers.
// With no policy configured, all discovered servers are allowed.
func (r *MCPGatewayReconciler) filterServersByQualityPolicy(ctx context.Context, gateway *kubemootv1alpha1.MCPGateway, discovered []kubemootv1alpha1.DiscoveredServer) ([]kubemootv1alpha1.DiscoveredServer, error) {
	log := logf.FromContext(ctx)

	if gateway.Spec.QualityPolicyRef == "" {
		// No quality policy - allow all discovered servers
		return discovered, nil
	}

	policy := &kubemootv1alpha1.MCPQualityPolicy{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      gateway.Spec.QualityPolicyRef,
		Namespace: gateway.Namespace,
	}, policy); err != nil {
		log.Error(err, "Failed to get quality policy", "policy", gateway.Spec.QualityPolicyRef)
		// Without policy, don't provision anything
		return nil, fmt.Errorf("quality policy %s not found", gateway.Spec.QualityPolicyRef)
	}

	// Filter servers through quality policy
	var allowedServers []kubemootv1alpha1.DiscoveredServer
	for _, server := range discovered {
		// Check if already evaluated in catalog
		if server.QualityDecision == policyActionAllow {
			allowedServers = append(allowedServers, server)
		}
	}
	return allowedServers, nil
}

// cleanupRemovedDynamicMCPServers deletes dynamic MCPServers that are no longer in the allowed set.
func (r *MCPGatewayReconciler) cleanupRemovedDynamicMCPServers(ctx context.Context, existing []kubemootv1alpha1.MCPServer, allowedServerNames map[string]kubemootv1alpha1.DiscoveredServer) {
	log := logf.FromContext(ctx)

	for i := range existing {
		server := &existing[i]
		if _, exists := allowedServerNames[server.Name]; !exists {
			log.Info("Deleting dynamic MCPServer no longer in catalog", "server", server.Name)
			if err := r.Delete(ctx, server); err != nil {
				log.Error(err, "Failed to delete dynamic MCPServer", "server", server.Name)
			}
		}
	}
}

// ensureDynamicMCPServer creates or updates a dynamic MCPServer from catalog discovery
func (r *MCPGatewayReconciler) ensureDynamicMCPServer(
	ctx context.Context,
	gateway *kubemootv1alpha1.MCPGateway,
	discovered kubemootv1alpha1.DiscoveredServer,
) error {
	log := logf.FromContext(ctx)

	// Sanitize name for Kubernetes (RFC 1123 DNS subdomain)
	sanitizedName := sanitizeK8sName(discovered.Name)

	// Check if explicit MCPServer CR already exists
	existingMCP := &kubemootv1alpha1.MCPServer{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      sanitizedName,
		Namespace: gateway.Namespace,
	}, existingMCP)

	// If explicit MCPServer exists without the dynamic label, don't override it
	if err == nil {
		if existingMCP.Labels[annoDynamic] != valueTrue {
			log.Info("Explicit MCPServer exists, skipping dynamic provisioning", "server", discovered.Name)
			return nil
		}
		// If it's already a dynamic server owned by this gateway, update if needed
		// For now, we'll leave it as-is (catalog controller should handle updates)
		return nil
	}

	if !errors.IsNotFound(err) {
		return err
	}

	// Find matching credential policy
	policy := r.findMatchingCredentialPolicy(gateway.Spec.CredentialPolicies, discovered.Categories)

	// Resolve transport from report, discovered metadata, policy, or registry type
	transport := r.resolveTransport(ctx, gateway.Namespace, discovered, policy)

	// Determine image, command, args, and potentially override transport based on registry type
	image, command, args, transport, err := buildDynamicMCPServerSpec(discovered, transport)
	if err != nil {
		log.Info("Skipping server", "server", discovered.Name, "reason", err.Error())
		return nil
	}

	// Stdio transport uses supergateway on port 8080
	port := int32(8080)
	if transport == kubemootv1alpha1.TransportHTTP {
		port = 3000
	}

	mcpServerSpec := kubemootv1alpha1.MCPServerSpec{
		Image:     image,
		Command:   command,
		Args:      args,
		Port:      port,
		Transport: transport,
	}

	// Apply credentials from matching policy
	if policy != nil {
		mcpServerSpec.ServiceAccountName = policy.ServiceAccountName
		mcpServerSpec.SecretRef = policy.SecretRef
		mcpServerSpec.SecretVolumes = policy.SecretVolumes
		mcpServerSpec.EmptyDirVolumes = policy.EmptyDirVolumes
	}

	// Create MCPServer with owner reference (sanitizedName defined at top of function)
	mcpServer := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sanitizedName,
			Namespace: gateway.Namespace,
			Labels: map[string]string{
				annoDynamic: valueTrue,
				annoGateway: gateway.Name,
			},
			Annotations: map[string]string{
				"kubemoot.ai/original-name":      discovered.Name, // Store original name as annotation (can contain /)
				"kubemoot.ai/registry-type":      discovered.RegistryType,
				"kubemoot.ai/package-identifier": discovered.PackageIdentifier,
			},
		},
		Spec: mcpServerSpec,
	}

	// Add GitHub URL annotation if available
	if discovered.GitHubURL != "" {
		mcpServer.Annotations["kubemoot.ai/github-url"] = discovered.GitHubURL
	}

	// Set owner reference so MCPServer is garbage collected with gateway
	if err := controllerutil.SetControllerReference(gateway, mcpServer, r.Scheme); err != nil {
		return err
	}

	log.Info("Creating dynamic MCPServer from catalog", "server", discovered.Name, "registry", discovered.RegistryType)
	return r.Create(ctx, mcpServer)
}

// ============================================================================
// Legacy Dynamic MCP Server Provisioning with Credential Policies
// ============================================================================

// DiscoveredMCPServer represents an MCP server discovered from a registry
// DEPRECATED: Use kubemootv1alpha1.DiscoveredServer instead
type DiscoveredMCPServer struct {
	Name        string
	Author      string
	Version     string
	Description string
	Categories  []string
	Image       string
	Port        int32
	Transport   string
	GitHubURL   string
	Registry    string
}

// resolveTransport determines the transport for a dynamic MCPServer using priority order:
// 1. MCPServerReport learned recommendation
// 2. DiscoveredServer metadata
// 3. CredentialPolicy override
// 4. Infer from registry type
func (r *MCPGatewayReconciler) resolveTransport(
	ctx context.Context,
	namespace string,
	discovered kubemootv1alpha1.DiscoveredServer,
	policy *kubemootv1alpha1.CredentialPolicy,
) kubemootv1alpha1.MCPTransport {
	log := logf.FromContext(ctx)

	// Priority 1: Check MCPServerReport for learned transport
	reportName := sanitizeK8sName(discovered.Name)
	report := &kubemootv1alpha1.MCPServerReport{}
	if err := r.Get(ctx, types.NamespacedName{Name: reportName, Namespace: namespace}, report); err == nil {
		if report.Status.RecommendedTransport != "" {
			log.Info("Using learned transport from MCPServerReport", "server", discovered.Name, "transport", report.Status.RecommendedTransport)
			return kubemootv1alpha1.MCPTransport(report.Status.RecommendedTransport)
		}
	}

	// Priority 2: DiscoveredServer metadata
	switch strings.ToLower(discovered.Transport) {
	case string(kubemootv1alpha1.TransportHTTP), "streamable-http":
		return kubemootv1alpha1.TransportHTTP
	case "sse":
		return kubemootv1alpha1.TransportSSE
	case "stdio":
		return kubemootv1alpha1.TransportStdio
	}

	// Priority 3: Policy override
	if policy != nil && policy.Transport != "" {
		return policy.Transport
	}

	// Priority 4: Infer from registry type
	switch strings.ToLower(discovered.RegistryType) {
	case "npm", "pip", "pypi":
		return kubemootv1alpha1.TransportStdio
	default:
		return kubemootv1alpha1.TransportHTTP
	}
}

// buildDynamicMCPServerSpec determines image, command, args, and transport from registry type.
// Returns an error if the registry type is unknown or image cannot be determined.
func buildDynamicMCPServerSpec(
	discovered kubemootv1alpha1.DiscoveredServer,
	transport kubemootv1alpha1.MCPTransport,
) (string, []string, []string, kubemootv1alpha1.MCPTransport, error) {
	switch strings.ToLower(discovered.RegistryType) {
	case "oci", "docker":
		if discovered.PackageIdentifier == "" {
			return "", nil, nil, transport, fmt.Errorf("no deployable package")
		}
		return discovered.PackageIdentifier, nil, nil, transport, nil
	case "npm":
		return "node:20-alpine", []string{"npx"}, []string{"-y", discovered.PackageIdentifier},
			kubemootv1alpha1.TransportStdio, nil
	case "pypi", "pip":
		args := "pip install --quiet " + discovered.PackageIdentifier + " && python -m " + getModuleName(discovered.PackageIdentifier)
		return "python:3.11-alpine", []string{"sh", "-c"}, []string{args},
			kubemootv1alpha1.TransportStdio, nil
	default:
		return "", nil, nil, transport, fmt.Errorf("unknown registry type: %s", discovered.RegistryType)
	}
}

// MCPServerMetadata holds server info + live metrics fetched via HTTP
type MCPServerMetadata struct {
	Name           string    `json:"name"`
	Author         string    `json:"author"`
	Version        string    `json:"version"`
	Description    string    `json:"description"`
	Categories     []string  `json:"categories"`
	GitHubURL      string    `json:"github_url,omitempty"`
	Stars          int       `json:"stars,omitempty"`
	Forks          int       `json:"forks,omitempty"`
	Downloads      int       `json:"downloads,omitempty"`
	LastCommitDate time.Time `json:"last_commit_date,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
}

// PolicyDecision returned by quality evaluation
type PolicyDecision struct {
	Action     string  `json:"action"`     // "allow" or "deny"
	Confidence float64 `json:"confidence"` // 0.0-1.0 for AI decisions
	Reason     string  `json:"reason"`
}

// findMatchingCredentialPolicy finds the first credential policy matching the given categories
func (r *MCPGatewayReconciler) findMatchingCredentialPolicy(
	policies []kubemootv1alpha1.CredentialPolicy,
	categories []string,
) *kubemootv1alpha1.CredentialPolicy {
	for i := range policies {
		policy := &policies[i]
		// Check for catch-all
		for _, cat := range policy.Categories {
			if cat == "*" {
				return policy
			}
		}
		// Check for category match (case-insensitive)
		for _, policyCategory := range policy.Categories {
			for _, serverCategory := range categories {
				if strings.EqualFold(policyCategory, serverCategory) {
					return policy
				}
			}
		}
	}
	return nil // No matching policy - use defaults
}

// ============================================================================
// MCP Quality Policy Evaluation
// ============================================================================

// EvaluateServerQuality runs the full evaluation pipeline against an MCPQualityPolicy
// Evaluation order: allowing → blocking → considering (AI evaluation)
func (r *MCPGatewayReconciler) EvaluateServerQuality(
	ctx context.Context,
	policy *kubemootv1alpha1.MCPQualityPolicy,
	server MCPServerMetadata,
) PolicyDecision {
	log := logf.FromContext(ctx)

	// Step 1: Check allowing (accepted immediately, no evaluation)
	if r.matchesAllowing(policy.Spec.Allowing, server) {
		log.V(1).Info("Server in allowing list", "server", server.Name)
		return PolicyDecision{
			Action:     policyActionAllow,
			Confidence: 1.0,
			Reason:     "allowing: essential infrastructure MCP",
		}
	}

	// Step 2: Check blocking (rejected immediately, no evaluation)
	if reason := r.matchesBlocking(policy.Spec.Blocking, server); reason != "" {
		log.V(1).Info("Server in blocking list", "server", server.Name, "reason", reason)
		return PolicyDecision{
			Action:     policyActionDeny,
			Confidence: 1.0,
			Reason:     fmt.Sprintf("blocking: %s", reason),
		}
	}

	// Step 3: Tested tier (MCPServerReport data)
	if policy.Spec.Tested != nil && policy.Spec.Tested.Enabled {
		decision := r.evaluateTestedTierForMetadata(ctx, policy, server)
		if decision.Action != "" {
			return decision
		}
		// Empty action means no report or caution — fall through to considering
	}

	// Step 4: Considering (default - AI evaluation for everything else)
	return r.evaluateConsideringTier(ctx, policy, server)
}

// evaluateConsideringTier runs the AI "considering" evaluation step (or its fallback) for a server.
func (r *MCPGatewayReconciler) evaluateConsideringTier(ctx context.Context, policy *kubemootv1alpha1.MCPQualityPolicy, server MCPServerMetadata) PolicyDecision {
	log := logf.FromContext(ctx)

	considering := policy.Spec.Considering
	if considering == nil || !considering.Enabled {
		// No AI evaluation configured, use fallback
		fallback := policyActionDeny
		if considering != nil && considering.FallbackAction != "" {
			fallback = considering.FallbackAction
		}
		return PolicyDecision{
			Action:     fallback,
			Confidence: 1.0,
			Reason:     "considering: AI evaluation disabled, using fallback",
		}
	}

	// Fetch live metrics via direct HTTP (no MCP dependency)
	server = r.fetchLiveMetrics(ctx, server)

	// Consult the quality evaluator Agent
	decision, err := r.consultQualityAgent(ctx, policy, server)
	if err != nil {
		log.Error(err, "AI evaluation failed, using fallback")
		return PolicyDecision{
			Action:     considering.FallbackAction,
			Confidence: 0.0,
			Reason:     fmt.Sprintf("considering: AI unavailable - %v", err),
		}
	}

	// Check confidence threshold
	threshold := 0.7
	if considering.ConfidenceThreshold != "" {
		if parsed, err := parseFloat(considering.ConfidenceThreshold); err == nil {
			threshold = parsed
		}
	}

	if decision.Confidence >= threshold {
		return decision
	}

	// Below confidence threshold
	return PolicyDecision{
		Action:     considering.FallbackAction,
		Confidence: decision.Confidence,
		Reason:     fmt.Sprintf("considering: AI confidence %.2f below threshold %.2f", decision.Confidence, threshold),
	}
}

// evaluateTestedTierForMetadata checks MCPServerReport for prior test experience (metadata variant)
func (r *MCPGatewayReconciler) evaluateTestedTierForMetadata(ctx context.Context, policy *kubemootv1alpha1.MCPQualityPolicy, server MCPServerMetadata) PolicyDecision {
	return evaluateTestedTier(ctx, r.Client, policy, server.Name)
}

// matchesAllowing checks if server is in the allowing list (accepted immediately)
func (r *MCPGatewayReconciler) matchesAllowing(entries []kubemootv1alpha1.AllowingEntry, server MCPServerMetadata) bool {
	for _, entry := range entries {
		if entry.Name != "" && strings.EqualFold(entry.Name, server.Name) {
			return true
		}
		if entry.Author != "" && strings.EqualFold(entry.Author, server.Author) {
			return true
		}
	}
	return false
}

// matchesBlocking checks if server matches any blocking entry
// Returns the blocking reason if matched, empty string if not blocked
func (r *MCPGatewayReconciler) matchesBlocking(entries []kubemootv1alpha1.BlockingEntry, server MCPServerMetadata) string {
	for _, entry := range entries {
		if r.matchesBlockingEntry(entry, server) {
			if entry.Reason != "" {
				return entry.Reason
			}
			return "matched blocking rule"
		}
	}
	return ""
}

// matchesBlockingEntry checks if server matches a blocking entry with glob/regex and version support
func (r *MCPGatewayReconciler) matchesBlockingEntry(entry kubemootv1alpha1.BlockingEntry, server MCPServerMetadata) bool {
	// If name matcher specified, must match
	if entry.Name != nil && !r.matchesStringMatcher(*entry.Name, server.Name) {
		return false
	}
	// If author matcher specified, must match
	if entry.Author != nil && !r.matchesStringMatcher(*entry.Author, server.Author) {
		return false
	}
	// If version constraint specified, must match
	if entry.Version != "" {
		if !r.matchesVersionConstraint(entry.Version, server.Version) {
			return false
		}
	}
	// At least one matcher must be specified
	if entry.Name == nil && entry.Author == nil && entry.Version == "" {
		return false
	}
	return true
}

// matchesStringMatcher handles exact, glob, and regex matching
func (r *MCPGatewayReconciler) matchesStringMatcher(m kubemootv1alpha1.StringMatcher, value string) bool {
	var matched bool
	switch m.Type {
	case kubemootv1alpha1.MatcherTypeExact, "":
		matched = strings.EqualFold(value, m.Value)
	case kubemootv1alpha1.MatcherTypeGlob:
		// Simple glob matching using filepath.Match (supports *, ?)
		matched, _ = matchGlob(strings.ToLower(m.Value), strings.ToLower(value))
	case kubemootv1alpha1.MatcherTypeRegex:
		re, err := compileRegex(m.Value)
		if err != nil {
			return false
		}
		matched = re.MatchString(value)
	}
	if m.Negate {
		return !matched
	}
	return matched
}

// matchesVersionConstraint checks if version satisfies the constraint.
// Delegates to the shared matchVersionConstraintShared function.
func (r *MCPGatewayReconciler) matchesVersionConstraint(constraint, version string) bool {
	return matchVersionConstraintShared(constraint, version)
}

// parseVersion parses a semver string into [major, minor, patch]
func parseVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) < 1 {
		return nil
	}

	result := make([]int, 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		// Handle pre-release suffixes (e.g., "1.0.0-beta")
		numPart := strings.Split(parts[i], "-")[0]
		num, err := parseInt(numPart)
		if err != nil {
			if i == 0 {
				return nil // Major version is required
			}
			break
		}
		result[i] = num
	}
	return result
}

// compareVersions compares two version arrays, returns -1, 0, or 1
func compareVersions(a, b []int) int {
	for i := 0; i < 3; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// parseInt is a helper to parse int from string
func parseInt(s string) (int, error) {
	var result int
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}

// parseFloat is a helper to parse float64 from string
func parseFloat(s string) (float64, error) {
	var result float64
	_, err := fmt.Sscanf(s, "%f", &result)
	return result, err
}

// matchGlob performs simple glob matching
func matchGlob(pattern, value string) (bool, error) {
	// Convert glob to regex: * -> .*, ? -> .
	regexPattern := "^"
	for _, c := range pattern {
		switch c {
		case '*':
			regexPattern += ".*"
		case '?':
			regexPattern += "."
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			regexPattern += "\\" + string(c)
		default:
			regexPattern += string(c)
		}
	}
	regexPattern += "$"

	re, err := compileRegex(regexPattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(value), nil
}

// compileRegex compiles a regex pattern with caching potential
func compileRegex(pattern string) (*regexpType, error) {
	return regexpCompile(pattern)
}

// ============================================================================
// Live Metrics Fetching (Direct HTTP - No MCP Dependency)
// ============================================================================

// GitHubRepoMetrics from direct API call
type GitHubRepoMetrics struct {
	Stars      int       `json:"stargazers_count"`
	Forks      int       `json:"forks_count"`
	LastCommit time.Time `json:"pushed_at"`
}

// fetchLiveMetrics gets fresh metrics via direct HTTP (NOT MCP - avoids recursion)
func (r *MCPGatewayReconciler) fetchLiveMetrics(ctx context.Context, server MCPServerMetadata) MCPServerMetadata {
	log := logf.FromContext(ctx)

	// Extract GitHub owner/repo from URL if available
	if server.GitHubURL != "" {
		metrics, err := r.fetchGitHubMetrics(ctx, server.GitHubURL)
		if err == nil {
			server.Stars = metrics.Stars
			server.Forks = metrics.Forks
			server.LastCommitDate = metrics.LastCommit
		} else {
			log.V(1).Info("Failed to fetch GitHub metrics", "url", server.GitHubURL, "error", err)
		}
	}
	return server
}

// fetchGitHubMetrics via direct HTTP to GitHub API (no MCP)
func (r *MCPGatewayReconciler) fetchGitHubMetrics(ctx context.Context, repoURL string) (*GitHubRepoMetrics, error) {
	// Parse owner/repo from URL: https://github.com/owner/repo
	repoURL = strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid GitHub URL: %s", repoURL)
	}
	owner, repo := parts[0], parts[1]

	// Direct HTTP call to GitHub API
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)

	httpClient := r.httpClientOrDefault()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Kubemoot-Operator/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var metrics GitHubRepoMetrics
	if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
		return nil, err
	}
	return &metrics, nil
}

// ============================================================================
// AI Quality Evaluation (Dogfooding Kubemoot Agent)
// ============================================================================

// qualityAgentMessage builds the chat message that asks the quality agent to
// evaluate a server: its metadata as JSON plus the policy criteria, read from the
// referenced ConfigMap when set and from the inline criteria otherwise (or when the
// ConfigMap cannot be read).
func (r *MCPGatewayReconciler) qualityAgentMessage(ctx context.Context, policy *kubemootv1alpha1.MCPQualityPolicy, server MCPServerMetadata) (string, error) {
	serverJSON, err := json.Marshal(server)
	if err != nil {
		return "", fmt.Errorf("failed to marshal server metadata: %w", err)
	}

	criteria := policy.Spec.Considering.Criteria
	if policy.Spec.Considering.CriteriaFromConfigMap != nil {
		loadedCriteria, err := r.getCriteriaFromConfigMap(ctx, policy)
		if err != nil {
			logf.FromContext(ctx).Error(err, "Failed to load criteria from ConfigMap, using inline criteria")
		} else {
			criteria = loadedCriteria
		}
	}

	return fmt.Sprintf("Evaluate this MCP server:\n\n```json\n%s\n```\n\nCriteria:\n%s",
		serverJSON, criteria), nil
}

// consultQualityAgent calls the dogfooded Agent for AI evaluation
func (r *MCPGatewayReconciler) consultQualityAgent(
	ctx context.Context,
	policy *kubemootv1alpha1.MCPQualityPolicy,
	server MCPServerMetadata,
) (PolicyDecision, error) {
	considering := policy.Spec.Considering
	if considering == nil {
		return PolicyDecision{}, fmt.Errorf("considering config is nil")
	}

	// Get Agent endpoint (in same namespace or kubemoot-system)
	agentRef := considering.AgentRef
	agentNamespace := policy.Namespace
	if agentNamespace == "" {
		agentNamespace = "kubemoot-system"
	}

	agentURL := fmt.Sprintf("http://%s.%s:8080/chat", agentRef, agentNamespace)

	message, err := r.qualityAgentMessage(ctx, policy, server)
	if err != nil {
		return PolicyDecision{}, err
	}
	reqBody, err := json.Marshal(map[string]string{jsonKeyMessage: message})
	if err != nil {
		return PolicyDecision{}, err
	}

	timeout := qualityAgentTimeout(considering)

	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	req, err := http.NewRequestWithContext(ctxWithTimeout, http.MethodPost, agentURL, bytes.NewReader(reqBody))
	if err != nil {
		return PolicyDecision{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return PolicyDecision{}, fmt.Errorf("agent call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	reply, err := readQualityAgentReply(resp)
	if err != nil {
		return PolicyDecision{}, err
	}
	return parseAgentDecision(reply)
}

// qualityAgentTimeout is the policy's agent call timeout, 30s when unset.
func qualityAgentTimeout(considering *kubemootv1alpha1.ConsideringConfig) time.Duration {
	if considering.TimeoutSeconds > 0 {
		return time.Duration(considering.TimeoutSeconds) * time.Second
	}
	return 30 * time.Second
}

// readQualityAgentReply returns the "response" field of the quality agent's JSON
// reply, or an error carrying the body when the agent did not answer 200.
func readQualityAgentReply(resp *http.Response) (string, error) {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("agent returned %d: %s", resp.StatusCode, string(body))
	}
	var agentResp struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agentResp); err != nil {
		return "", fmt.Errorf("failed to parse agent response: %w", err)
	}
	return agentResp.Response, nil
}

// parseAgentDecision extracts a PolicyDecision from an agent's text response.
// Tries direct JSON parse first, then falls back to extracting JSON from prose.
func parseAgentDecision(response string) (PolicyDecision, error) {
	var decision PolicyDecision
	if err := json.Unmarshal([]byte(response), &decision); err != nil {
		jsonStart := strings.Index(response, "{")
		jsonEnd := strings.LastIndex(response, "}")
		if jsonStart >= 0 && jsonEnd > jsonStart {
			if err := json.Unmarshal([]byte(response[jsonStart:jsonEnd+1]), &decision); err != nil {
				return PolicyDecision{}, fmt.Errorf("agent response not valid JSON: %w", err)
			}
		} else {
			return PolicyDecision{}, fmt.Errorf("agent response not valid JSON: %w", err)
		}
	}

	decision.Action = strings.ToLower(decision.Action)
	if decision.Action != policyActionAllow && decision.Action != policyActionDeny {
		return PolicyDecision{}, fmt.Errorf("invalid decision action: %s", decision.Action)
	}
	return decision, nil
}

// getCriteriaFromConfigMap loads evaluation criteria from a ConfigMap
func (r *MCPGatewayReconciler) getCriteriaFromConfigMap(ctx context.Context, policy *kubemootv1alpha1.MCPQualityPolicy) (string, error) {
	if policy.Spec.Considering == nil || policy.Spec.Considering.CriteriaFromConfigMap == nil {
		return "", fmt.Errorf("no ConfigMap reference specified")
	}

	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: policy.Namespace,
		Name:      policy.Spec.Considering.CriteriaFromConfigMap.Name,
	}, configMap); err != nil {
		return "", fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	key := policy.Spec.Considering.CriteriaFromConfigMap.Key
	criteria, ok := configMap.Data[key]
	if !ok {
		return "", fmt.Errorf("key %s not found in ConfigMap %s", key, configMap.Name)
	}

	return criteria, nil
}

// Regex type alias for testing
type regexpType = regexp.Regexp

var regexpCompile = regexp.Compile

// findGatewaysForCatalog maps MCPCatalog changes to MCPGateway reconcile requests
func (r *MCPGatewayReconciler) findGatewaysForCatalog(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)
	catalog := obj.(*kubemootv1alpha1.MCPCatalog)

	// Find all MCPGateways in the same namespace that reference this catalog
	gatewayList := &kubemootv1alpha1.MCPGatewayList{}
	if err := r.List(ctx, gatewayList, client.InNamespace(catalog.Namespace)); err != nil {
		log.Error(err, "Failed to list MCPGateways")
		return nil
	}

	var requests []reconcile.Request
	for _, gw := range gatewayList.Items {
		// Check if this gateway references the catalog
		for _, catalogRef := range gw.Spec.CatalogRefs {
			if catalogRef == catalog.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      gw.Name,
						Namespace: gw.Namespace,
					},
				})
				break
			}
		}
	}

	return requests
}

// unnamedServerName is the name sanitizeK8sName gives a server whose name has no usable characters.
const unnamedServerName = "unnamed-server"

// sanitizeK8sName converts a string to a valid Kubernetes name (DNS-1035 label)
// DNS-1035 labels must consist of lowercase alphanumeric characters or '-',
// start with an alphabetic character, and end with an alphanumeric character.
// This is stricter than DNS-1123 subdomain because Services use DNS-1035.
func sanitizeK8sName(name string) string {
	// Convert to lowercase
	result := strings.ToLower(name)
	// Replace invalid characters with dashes
	result = strings.ReplaceAll(result, "/", "-")
	result = strings.ReplaceAll(result, "_", "-")
	result = strings.ReplaceAll(result, " ", "-")
	result = strings.ReplaceAll(result, ".", "-") // Services require DNS-1035 (no dots)
	// Remove any characters that aren't alphanumeric or dash
	result = strings.Map(keepDNSLabelRune, result)
	// Trim leading/trailing dashes
	result = strings.Trim(result, "-")
	// Ensure it starts with a letter (DNS-1035 requirement)
	if len(result) > 0 && (result[0] >= '0' && result[0] <= '9') {
		result = "mcp-" + result
	}
	// Ensure it's not empty
	if result == "" {
		result = unnamedServerName
	}
	// Truncate to max length (63 for DNS-1035 label)
	if len(result) > 63 {
		result = result[:63]
		result = strings.TrimRight(result, "-")
	}
	return result
}

// keepDNSLabelRune keeps lowercase letters, digits, and '-' and drops every other
// rune (strings.Map drops a rune mapped to -1).
func keepDNSLabelRune(c rune) rune {
	if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
		return c
	}
	return -1
}

// getModuleName extracts a Python module name from a package identifier
// e.g., "mcp-server-fetch" -> "mcp_server_fetch"
func getModuleName(packageIdentifier string) string {
	// Remove version specifier if present (e.g., "package>=1.0.0" -> "package")
	name := packageIdentifier
	for _, sep := range []string{">=", "<=", "==", ">", "<", "~="} {
		if idx := strings.Index(name, sep); idx != -1 {
			name = name[:idx]
		}
	}
	// Replace dashes with underscores for Python module name
	return strings.ReplaceAll(name, "-", "_")
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// MCPServer status updates fire on every probe cycle (~30s per
	// MCPServer × 6 MCPServers = 12 status events/min). Without a
	// predicate, EACH triggers a gateway reconcile, which then
	// re-registers ALL MCPServers with the gateway. The gateway
	// dedupes most of them but the log noise and small wasted work
	// per cycle were observed concretely 2026-05-26 (~12 gateway
	// reconciles/min for the homelab-pilot-gateway CR).
	//
	// GenerationChangedPredicate fires only when metadata.generation
	// changes — i.e., when the SPEC changes. Status-only updates
	// no longer enqueue gateway reconciles. Spec-change events (the
	// only ones the gateway actually cares about for registration)
	// continue to flow.
	specChangedOnly := builder.WithPredicates(predicate.GenerationChangedPredicate{})

	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.MCPGateway{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&kubemootv1alpha1.RAGSource{}).
		// Watch MCPServers and trigger gateway reconciliation — but only
		// on spec changes, not status updates (see comment above).
		Watches(
			&kubemootv1alpha1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.findGatewaysForMCPServer),
			specChangedOnly,
		).
		// Watch MCPCatalogs — same spec-change-only filter.
		Watches(
			&kubemootv1alpha1.MCPCatalog{},
			handler.EnqueueRequestsFromMapFunc(r.findGatewaysForCatalog),
			specChangedOnly,
		).
		// Re-reconcile every MCPGateway when KubemootConfig changes so a
		// mcpGateway image bump propagates to the gateway Deployment
		// without per-CR annotation. See kubemootconfig_propagation.go.
		Watches(
			&kubemootv1alpha1.KubemootConfig{},
			enqueueAllOnKubemootConfigChange(mgr.GetClient(),
				func() client.ObjectList { return &kubemootv1alpha1.MCPGatewayList{} },
				"mcpgateway"),
		).
		Named("mcpgateway").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
