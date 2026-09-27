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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	"github.com/javajon/kubemoot/operator/internal/crewscope"
	kubemootnats "github.com/javajon/kubemoot/operator/internal/nats"
)

const maxReportTrials = 20

const mcpServerFinalizer = "kubemoot.ai/mcpserver-finalizer"

// MCPServerReconciler reconciles a MCPServer object
type MCPServerReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ConfigCache   *ConfigCache
	NATSPublisher *kubemootnats.Publisher
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpserverreports,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpserverreports/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

// Reconcile handles MCPServer reconciliation
func (r *MCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the MCPServer instance
	mcpServer := &kubemootv1alpha1.MCPServer{}
	if err := r.Get(ctx, req.NamespacedName, mcpServer); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !mcpServer.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, mcpServer)
	}

	// Add finalizer if not present
	if added, result, err := r.ensureFinalizer(ctx, mcpServer); added || err != nil {
		return result, err
	}

	// Check if this is an external MCP server (no deployment needed)
	if mcpServer.Spec.ExternalEndpoint != "" {
		log.Info("Reconciling external MCPServer", "name", mcpServer.Name, "endpoint", mcpServer.Spec.ExternalEndpoint)
		return r.reconcileExternalMCPServer(ctx, mcpServer)
	}

	// Validate that Image is specified for managed MCP servers
	if mcpServer.Spec.Image == "" {
		return r.updateStatus(ctx, mcpServer, "Error", false, "Either Image or ExternalEndpoint must be specified")
	}

	log.Info("Reconciling managed MCPServer", "name", mcpServer.Name, "image", mcpServer.Spec.Image)

	// Reconcile Deployment
	deployment, err := r.reconcileDeployment(ctx, mcpServer)
	if err != nil {
		return r.updateStatus(ctx, mcpServer, "Error", false, fmt.Sprintf("Failed to reconcile deployment: %v", err))
	}

	// Reconcile Service
	// For http/sse transport: always create service on configured port
	// For stdio transport: create service if proxy injection is enabled (proxy exposes HTTP)
	if shouldCreateMCPServerService(mcpServer) {
		if _, err := r.reconcileService(ctx, mcpServer); err != nil {
			return r.updateStatus(ctx, mcpServer, "Error", false, fmt.Sprintf("Failed to reconcile service: %v", err))
		}
	}

	// Update status based on deployment state
	return r.updateStatusFromDeployment(ctx, mcpServer, deployment)
}

// ensureFinalizer adds the MCPServer finalizer if missing. It returns added=true
// (with a requeue Result) when it added the finalizer, signalling the caller to
// stop and let the update re-trigger reconciliation.
func (r *MCPServerReconciler) ensureFinalizer(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer) (bool, ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(mcpServer, mcpServerFinalizer) {
		return false, ctrl.Result{}, nil
	}
	controllerutil.AddFinalizer(mcpServer, mcpServerFinalizer)
	if err := r.Update(ctx, mcpServer); err != nil {
		return false, ctrl.Result{}, err
	}
	return true, ctrl.Result{Requeue: true}, nil
}

// shouldCreateMCPServerService reports whether a Service should be created.
// For http/sse transport a Service is always created; for stdio transport one is
// created only when proxy injection is enabled (default: true).
func shouldCreateMCPServerService(mcpServer *kubemootv1alpha1.MCPServer) bool {
	if mcpServer.Spec.Transport != kubemootv1alpha1.TransportStdio {
		return true
	}
	return mcpServerProxyEnabled(mcpServer)
}

// mcpServerProxyEnabled reports whether proxy injection is enabled for the
// MCPServer (default: true when unspecified).
func mcpServerProxyEnabled(mcpServer *kubemootv1alpha1.MCPServer) bool {
	if mcpServer.Spec.ProxyInjection != nil && mcpServer.Spec.ProxyInjection.Enabled != nil {
		return *mcpServer.Spec.ProxyInjection.Enabled
	}
	return true
}

// mcpServerProxyPort returns the bridge/proxy port for the MCPServer, defaulting
// to 8080 when not explicitly configured.
func mcpServerProxyPort(mcpServer *kubemootv1alpha1.MCPServer) int32 {
	if mcpServer.Spec.ProxyInjection != nil && mcpServer.Spec.ProxyInjection.Port > 0 {
		return mcpServer.Spec.ProxyInjection.Port
	}
	return int32(8080)
}

// reconcileExternalMCPServer handles external MCP servers (no deployment needed)
func (r *MCPServerReconciler) reconcileExternalMCPServer(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer) (ctrl.Result, error) {
	// External MCP servers don't need deployment or service
	// Just set the status with the external endpoint
	return r.updateStatus(ctx, mcpServer, "Ready", true, "External MCP server",
		withEndpoint(mcpServer.Spec.ExternalEndpoint),
		withCapabilities(mcpServer.Spec.Capabilities))
}

// reconcileDeployment creates or updates the MCP server Deployment
func (r *MCPServerReconciler) reconcileDeployment(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer) (*appsv1.Deployment, error) {
	deployment := &appsv1.Deployment{}
	deploymentName := types.NamespacedName{
		Name:      boundedName(mcpServer.Name),
		Namespace: mcpServer.Namespace,
	}

	err := r.Get(ctx, deploymentName, deployment)
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	// Build desired deployment
	desiredDeployment := r.buildDeployment(mcpServer)

	if errors.IsNotFound(err) {
		return r.createDeployment(ctx, mcpServer, desiredDeployment)
	}

	// Update existing deployment only if spec changed (using hash annotation)
	return r.updateDeploymentIfChanged(ctx, mcpServer, deployment, desiredDeployment)
}

// createDeployment sets the owner reference and hash annotation on the desired
// deployment and creates it.
func (r *MCPServerReconciler) createDeployment(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer, desiredDeployment *appsv1.Deployment) (*appsv1.Deployment, error) {
	log := logf.FromContext(ctx)

	log.Info("Creating Deployment", "name", mcpServer.Name)
	if err := controllerutil.SetControllerReference(mcpServer, desiredDeployment, r.Scheme); err != nil {
		return nil, err
	}
	setDeploymentHash(desiredDeployment, computeDeploymentHash(desiredDeployment))
	if err := r.Create(ctx, desiredDeployment); err != nil {
		return nil, err
	}
	return desiredDeployment, nil
}

// updateDeploymentIfChanged updates the existing deployment only when its spec
// hash differs from the desired one (or when it lacks a hash annotation).
func (r *MCPServerReconciler) updateDeploymentIfChanged(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer, deployment, desiredDeployment *appsv1.Deployment) (*appsv1.Deployment, error) {
	log := logf.FromContext(ctx)

	desiredHash := computeDeploymentHash(desiredDeployment)
	currentHash := deployment.Annotations[deploymentHashAnnotation]

	if currentHash == "" {
		// Deployment exists but has no hash annotation (created before this fix or by another controller)
		// Just add the annotation without changing spec to avoid unnecessary ReplicaSet creation
		log.Info("Adding hash annotation to existing Deployment", "name", mcpServer.Name)
		setDeploymentHash(deployment, desiredHash)
		if err := r.Update(ctx, deployment); err != nil {
			return nil, err
		}
	} else if currentHash != desiredHash {
		log.Info("Updating Deployment", "name", mcpServer.Name, "reason", "spec changed")
		deployment.Spec = desiredDeployment.Spec
		setDeploymentHash(deployment, desiredHash)
		if err := r.Update(ctx, deployment); err != nil {
			return nil, err
		}
	}

	return deployment, nil
}

// setDeploymentHash sets the deployment hash annotation, initializing the
// annotations map when needed.
func setDeploymentHash(deployment *appsv1.Deployment, hash string) {
	if deployment.Annotations == nil {
		deployment.Annotations = make(map[string]string)
	}
	deployment.Annotations[deploymentHashAnnotation] = hash
}

// buildDeployment creates the Deployment spec for the MCP server
func (r *MCPServerReconciler) buildDeployment(mcpServer *kubemootv1alpha1.MCPServer) *appsv1.Deployment {
	replicas := int32(2)
	if mcpServer.Spec.Replicas != nil {
		replicas = *mcpServer.Spec.Replicas
	}

	port := int32(3000)
	if mcpServer.Spec.Port != 0 {
		port = mcpServer.Spec.Port
	}

	labels := map[string]string{
		labelName:      boundedName(mcpServer.Name),
		labelInstance:  boundedName(mcpServer.Name),
		labelManagedBy: managedByValue,
		labelComponent: componentMCPServer,
	}

	container := buildMCPServerContainer(mcpServer, port)
	strictSecurity := mcpServer.Spec.SecurityMode == kubemootv1alpha1.SecurityModeStrict
	seccompProfile := applyMCPServerSecurityContext(&container, strictSecurity)

	volumes, volumeMounts := buildMCPServerVolumes(mcpServer)
	container.VolumeMounts = volumeMounts

	initContainers, volumes, container := r.applyBridgeSidecar(mcpServer, container, volumes, volumeMounts)
	// User-declared sidecars (e.g. the artifact-access materializer) run as native
	// sidecars alongside the bridge, sharing the pod's volumes.
	initContainers = append(initContainers, buildUserSidecars(mcpServer)...)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      boundedName(mcpServer.Name),
			Namespace: mcpServer.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					InitContainers:                initContainers,
					Containers:                    []corev1.Container{container},
					Volumes:                       volumes,
					TerminationGracePeriodSeconds: int64Ptr(30),
					ServiceAccountName:            mcpServer.Spec.ServiceAccountName,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   boolPtr(strictSecurity),
						SeccompProfile: &seccompProfile,
					},
					ImagePullSecrets: r.ConfigCache.GetImagePullSecrets(),
				},
			},
		},
	}

	return deployment
}

// emptyDirVolumeName is the deterministic pod-volume name for the i-th
// EmptyDirVolume. Shared by buildMCPServerVolumes and buildUserSidecars so the
// name is defined in one place.
func emptyDirVolumeName(i int) string {
	return fmt.Sprintf("empty-vol-%d", i)
}

// buildUserSidecars renders MCPServer.Spec.Sidecars as modern native sidecars
// (initContainers with restartPolicy=Always). The operator forces RestartPolicy
// regardless of what the caller set, so a supplied container reliably runs for
// the pod's lifetime alongside the main container instead of blocking startup.
// Each sidecar also gets the pod's EmptyDirVolumes mounted at the same paths the
// main container sees (EmptyDirVolume has no user-settable name, so the sidecar
// cannot reference it itself) - this is how a sidecar stages files the main
// container reads (the artifact-access materializer -> the code sandbox).
func buildUserSidecars(mcpServer *kubemootv1alpha1.MCPServer) []corev1.Container {
	if len(mcpServer.Spec.Sidecars) == 0 {
		return nil
	}
	var emptyDirMounts []corev1.VolumeMount
	for i, ev := range mcpServer.Spec.EmptyDirVolumes {
		emptyDirMounts = append(emptyDirMounts, corev1.VolumeMount{
			Name:      emptyDirVolumeName(i),
			MountPath: ev.MountPath,
		})
	}
	restartAlways := corev1.ContainerRestartPolicyAlways
	sidecars := make([]corev1.Container, 0, len(mcpServer.Spec.Sidecars))
	for i := range mcpServer.Spec.Sidecars {
		sc := *mcpServer.Spec.Sidecars[i].DeepCopy()
		sc.RestartPolicy = &restartAlways
		sc.VolumeMounts = append(sc.VolumeMounts, emptyDirMounts...)
		sc.Env = withNamespaceEnv(sc.Env)
		sidecars = append(sidecars, sc)
	}
	return sidecars
}

// withNamespaceEnv returns a copy of env with KUBEMOOT_NAMESPACE from the
// downward API appended, unless env already sets it. MCP servers that build
// namespaced subjects or keys (scheduling-mcp, artifact-access) read it.
func withNamespaceEnv(env []corev1.EnvVar) []corev1.EnvVar {
	for _, e := range env {
		if e.Name == crewscope.NamespaceEnv {
			return env
		}
	}
	out := make([]corev1.EnvVar, 0, len(env)+1)
	out = append(out, env...)
	return append(out, namespaceEnvVar())
}

// buildMCPServerContainer creates the main container spec for an MCP server.
func buildMCPServerContainer(mcpServer *kubemootv1alpha1.MCPServer, port int32) corev1.Container {
	container := corev1.Container{
		Name:            componentMCPServer,
		Image:           mcpServer.Spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{
				Name:          "mcp",
				ContainerPort: port,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: withNamespaceEnv(mcpServer.Spec.Env),
	}

	if len(mcpServer.Spec.Command) > 0 {
		container.Command = mcpServer.Spec.Command
	}
	if len(mcpServer.Spec.Args) > 0 {
		container.Args = mcpServer.Spec.Args
	}

	if mcpServer.Spec.SecretRef != "" {
		container.EnvFrom = []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: mcpServer.Spec.SecretRef,
					},
				},
			},
		}
	}

	if mcpServer.Spec.Resources != nil {
		container.Resources = *mcpServer.Spec.Resources
	} else {
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		}
	}

	applyMCPServerProbes(&container, mcpServer, port)

	return container
}

// applyMCPServerProbes sets liveness and readiness probes based on transport and health path config.
func applyMCPServerProbes(container *corev1.Container, mcpServer *kubemootv1alpha1.MCPServer, port int32) {
	if mcpServer.Spec.Transport == kubemootv1alpha1.TransportStdio {
		return
	}

	if mcpServer.Spec.HealthPath != "" {
		readinessPath := mcpServer.Spec.HealthPath
		if mcpServer.Spec.ReadinessPath != "" {
			readinessPath = mcpServer.Spec.ReadinessPath
		}

		container.LivenessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: mcpServer.Spec.HealthPath,
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       30,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		}
		container.ReadinessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: readinessPath,
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		}
	} else {
		container.LivenessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       30,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		}
		container.ReadinessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		}
	}
}

// applyMCPServerSecurityContext sets container security context based on security mode.
// Returns the seccomp profile for use in pod-level security context.
func applyMCPServerSecurityContext(container *corev1.Container, strictSecurity bool) corev1.SeccompProfile {
	allowPrivilegeEscalation := false
	seccompProfile := corev1.SeccompProfile{
		Type: corev1.SeccompProfileTypeRuntimeDefault,
	}

	if strictSecurity {
		runAsNonRoot := true
		runAsUser := int64(1000)
		runAsGroup := int64(1000)
		container.SecurityContext = &corev1.SecurityContext{
			RunAsNonRoot:             &runAsNonRoot,
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			RunAsUser:                &runAsUser,
			RunAsGroup:               &runAsGroup,
			SeccompProfile:           &seccompProfile,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		}
	} else {
		container.SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			SeccompProfile:           &seccompProfile,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		}
	}
	return seccompProfile
}

// buildMCPServerVolumes creates secret and emptyDir volumes from MCPServer spec.
func buildMCPServerVolumes(mcpServer *kubemootv1alpha1.MCPServer) ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	for i, sv := range mcpServer.Spec.SecretVolumes {
		volumeName := fmt.Sprintf("secret-vol-%d", i)
		readOnly := true
		if sv.ReadOnly != nil {
			readOnly = *sv.ReadOnly
		}

		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: sv.Name,
				},
			},
		})

		mount := corev1.VolumeMount{
			Name:      volumeName,
			MountPath: sv.MountPath,
			ReadOnly:  readOnly,
		}
		if sv.SubPath != "" {
			mount.SubPath = sv.SubPath
		}
		volumeMounts = append(volumeMounts, mount)
	}

	for i, ev := range mcpServer.Spec.EmptyDirVolumes {
		volumeName := emptyDirVolumeName(i)

		emptyDirSource := &corev1.EmptyDirVolumeSource{}
		if ev.SizeLimit != "" {
			quantity := resource.MustParse(ev.SizeLimit)
			emptyDirSource.SizeLimit = &quantity
		}

		volumes = append(volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: emptyDirSource,
			},
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: ev.MountPath,
		})
	}

	return volumes, volumeMounts
}

// applyBridgeSidecar adds an MCP bridge sidecar for stdio transport if injection is enabled.
// Returns updated init containers, volumes, and main container.
func (r *MCPServerReconciler) applyBridgeSidecar(
	mcpServer *kubemootv1alpha1.MCPServer,
	container corev1.Container,
	volumes []corev1.Volume,
	volumeMounts []corev1.VolumeMount,
) ([]corev1.Container, []corev1.Volume, corev1.Container) {
	if mcpServer.Spec.Transport != kubemootv1alpha1.TransportStdio {
		return nil, volumes, container
	}

	if !mcpServerProxyEnabled(mcpServer) {
		return nil, volumes, container
	}

	proxyPort := mcpServerProxyPort(mcpServer)

	bridgeImage := r.resolveBridgeImage(mcpServer)

	pipesVolName := "mcp-pipes"
	volumes = append(volumes, corev1.Volume{
		Name: pipesVolName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})

	restartAlways := corev1.ContainerRestartPolicyAlways
	bridgeContainer := corev1.Container{
		Name:            "mcp-bridge",
		Image:           bridgeImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		RestartPolicy:   &restartAlways,
		Args: []string{
			"--port", fmt.Sprintf("%d", proxyPort),
			"--healthz", "/healthz",
			"--pipe-dir", bridgePipeDir,
		},
		Ports: []corev1.ContainerPort{{
			Name:          "http",
			ContainerPort: proxyPort,
			Protocol:      corev1.ProtocolTCP,
		}},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      pipesVolName,
			MountPath: bridgePipeDir,
		}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("16Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		SecurityContext: container.SecurityContext,
		// Three-probe lifecycle. The crucial constraint: the MAIN
		// container runs the MCP server THROUGH this bridge in exec mode
		// (see the exec-redirect below), and Kubernetes gates the main
		// container's start on this sidecar passing its STARTUP probe.
		//
		// The startup probe therefore CANNOT be any HTTP health endpoint,
		// because every one of the bridge's HTTP endpoints depends on the
		// MCP server being up:
		//   /healthz → 200 only when the MCP server has connected to the
		//              pipes (bridge.connected) — but the MCP server is the
		//              gated main container, so it can never connect.
		//   /readyz  → 200 only after the self-initialize handshake, which
		//              also needs the MCP server running.
		// Either one deadlocks: main container waits on the probe, the
		// probe waits on the main container. (Observed 2026-05-26: every
		// MCP server stuck in PodInitializing, bridge SIGTERM'd every 120s.)
		//
		// startup → TCP on the bridge port: succeeds the instant the bridge
		//   process is listening — independent of the MCP server. This is
		//   the correct gate (it means "the bridge is up and the pipe FIFOs
		//   exist, so the MCP server can be launched") and breaks the
		//   deadlock. The main container starts, the MCP server connects to
		//   the pipes, and only THEN can the HTTP handshake complete.
		// readiness → /readyz: gates SERVICE routing (not main-container
		//   start) on the completed MCP initialize handshake, so the pod
		//   only receives traffic once tools are actually answerable.
		// liveness → /healthz: once connected, a dropped pipe connection
		//   restarts the bridge.
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt32(proxyPort),
				},
			},
			PeriodSeconds:    2,
			TimeoutSeconds:   2,
			FailureThreshold: 60, // 120s budget for bridge process listen
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/readyz",
					Port: intstr.FromInt32(proxyPort),
				},
			},
			InitialDelaySeconds: 2,
			PeriodSeconds:       5,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(proxyPort),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       30,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
	}

	// Redirect main container's stdio through bridge exec mode
	var cmdParts []string
	if len(container.Command) > 0 {
		cmdParts = append(cmdParts, container.Command...)
		cmdParts = append(cmdParts, container.Args...)
	} else if len(container.Args) > 0 {
		cmdParts = container.Args
	}

	execArgs := []string{"exec", "--pipe-dir", bridgePipeDir, "--"}
	execArgs = append(execArgs, cmdParts...)
	container.Command = []string{"/pipes/kubemoot-mcp-bridge"}
	container.Args = execArgs

	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      pipesVolName,
		MountPath: bridgePipeDir,
	})
	container.VolumeMounts = volumeMounts

	container.Ports = nil
	container.LivenessProbe = nil
	container.ReadinessProbe = nil

	return []corev1.Container{bridgeContainer}, volumes, container
}

// resolveBridgeImage determines the bridge image from per-MCPServer override, KubemootConfig, or fallback.
func (r *MCPServerReconciler) resolveBridgeImage(mcpServer *kubemootv1alpha1.MCPServer) string {
	if mcpServer.Spec.ProxyInjection != nil && mcpServer.Spec.ProxyInjection.Image != "" {
		return mcpServer.Spec.ProxyInjection.Image
	}
	if r.ConfigCache != nil {
		return r.ConfigCache.GetMcpBridgeImage()
	}
	return FallbackMcpBridgeImage
}

// reconcileService creates or updates the MCP server Service
func (r *MCPServerReconciler) reconcileService(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer) (*corev1.Service, error) {
	log := logf.FromContext(ctx)

	service := &corev1.Service{}
	serviceName := types.NamespacedName{
		Name:      boundedName(mcpServer.Name),
		Namespace: mcpServer.Namespace,
	}

	err := r.Get(ctx, serviceName, service)
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	port := int32(3000)
	if mcpServer.Spec.Port != 0 {
		port = mcpServer.Spec.Port
	}

	// For stdio transport with proxy injection, use the proxy port
	if mcpServer.Spec.Transport == kubemootv1alpha1.TransportStdio && mcpServerProxyEnabled(mcpServer) {
		port = mcpServerProxyPort(mcpServer)
	}

	labels := map[string]string{
		labelName:      boundedName(mcpServer.Name),
		labelInstance:  boundedName(mcpServer.Name),
		labelManagedBy: managedByValue,
		labelComponent: componentMCPServer,
	}

	desiredService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      boundedName(mcpServer.Name),
			Namespace: mcpServer.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "mcp",
					Port:       port,
					TargetPort: intstr.FromInt32(port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
			// Session affinity is REQUIRED for the MCP stdio/SSE transport with
			// replicas > 1. The gateway holds one SSE GET stream (bound to a
			// sessionId on ONE pod) and POSTs tool requests to
			// /message?sessionId=. Without affinity the Service round-robins, so
			// a POST can land on a different replica than the one holding the SSE
			// stream — that pod doesn't know the session, the response never
			// arrives on the gateway's stream, and the call dies on a 30s
			// timeout (observed 2026-05-27: prometheus-mcp replicas=2, gateway
			// looping "Did not observe any item within 30000ms / re-initializing"
			// after a crew roll reshuffled pods). ClientIP affinity pins all of
			// the gateway's connections (one source IP) to one backend pod, so
			// SSE stream and POSTs stay coherent. Was masked before by manually
			// restarting the gateway — this is the durable fix.
			SessionAffinity: corev1.ServiceAffinityClientIP,
		},
	}

	if errors.IsNotFound(err) {
		log.Info("Creating Service", "name", mcpServer.Name)
		if err := controllerutil.SetControllerReference(mcpServer, desiredService, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, desiredService); err != nil {
			return nil, err
		}
		return desiredService, nil
	}

	// Update existing service. Apply SessionAffinity too so already-created
	// Services pick up the fix on reconcile (no manual delete needed).
	service.Spec.Selector = desiredService.Spec.Selector
	service.Spec.Ports = desiredService.Spec.Ports
	service.Spec.SessionAffinity = desiredService.Spec.SessionAffinity
	if err := r.Update(ctx, service); err != nil {
		return nil, err
	}

	return service, nil
}

// handleDeletion handles MCPServer deletion
func (r *MCPServerReconciler) handleDeletion(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(mcpServer, mcpServerFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Handling MCPServer deletion", "name", mcpServer.Name)

	// Deployment and Service will be garbage collected via owner references

	// Remove finalizer
	controllerutil.RemoveFinalizer(mcpServer, mcpServerFinalizer)
	if err := r.Update(ctx, mcpServer); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("MCPServer deleted successfully", "name", mcpServer.Name)
	return ctrl.Result{}, nil
}

// updateStatusFromDeployment updates MCPServer status based on Deployment state
func (r *MCPServerReconciler) updateStatusFromDeployment(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer, deployment *appsv1.Deployment) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	phase, ready, message := determineDeploymentPhase(deployment)

	if ready {
		if mcpServer.Status.Phase != "Ready" {
			r.recordDeploymentTrial(ctx, mcpServer, true, message)
		}
		if mcpServer.Spec.Registry != nil && (mcpServer.Spec.Registry.Enabled == nil || *mcpServer.Spec.Registry.Enabled) {
			if err := r.ensureToolIndexed(ctx, mcpServer); err != nil {
				log.Error(err, "Failed to ensure tool indexing", "mcpServer", mcpServer.Name)
			}
		}
	}

	r.recordFailedDeploymentTrial(ctx, mcpServer, deployment, ready)

	endpoint := buildMCPServerEndpoint(mcpServer)

	return r.updateStatus(ctx, mcpServer, phase, ready, message,
		withEndpoint(endpoint),
		withReplicas(deployment.Status.Replicas, deployment.Status.AvailableReplicas),
		withCapabilities(mcpServer.Spec.Capabilities))
}

// determineDeploymentPhase derives phase, readiness, and message from Deployment status.
func determineDeploymentPhase(deployment *appsv1.Deployment) (string, bool, string) {
	if deployment.Status.ReadyReplicas > 0 && deployment.Status.ReadyReplicas == deployment.Status.Replicas {
		return "Ready", true, fmt.Sprintf("%d/%d replicas ready", deployment.Status.ReadyReplicas, deployment.Status.Replicas)
	}
	if deployment.Status.ReadyReplicas > 0 {
		return "Degraded", false, fmt.Sprintf("%d/%d replicas ready", deployment.Status.ReadyReplicas, deployment.Status.Replicas)
	}
	return "Deploying", false, "Deployment in progress"
}

// recordFailedDeploymentTrial checks for stalled deployments and records a failed trial.
func (r *MCPServerReconciler) recordFailedDeploymentTrial(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer, deployment *appsv1.Deployment, ready bool) {
	if ready || mcpServer.Status.Phase != "Deploying" || deployment.Status.Replicas == 0 || deployment.Status.ReadyReplicas > 0 {
		return
	}
	for _, cond := range deployment.Status.Conditions {
		if cond.Type == "Progressing" && cond.Status == "False" {
			r.recordDeploymentTrial(ctx, mcpServer, false, cond.Message)
			return
		}
	}
}

// boundedName returns name unchanged when it fits Kubernetes' 63-char limit for
// resource names and label values; otherwise it truncates to 54 chars and appends
// a short hash of the full name (54 + 1 + 8 = 63) so the result stays unique,
// stable, and a valid DNS-1123 label. An MCPServer name is the crew's Helm
// fullname plus the component suffix, which can exceed 63 chars (e.g. a "-prose"
// crew variant doubles the prefix); without bounding, the Service create and the
// Deployment label/selector fail validation and that MCP silently never deploys.
// Short names pass through unchanged, so existing deployments are not disturbed.
func boundedName(name string) string {
	const maxLen = 63
	if len(name) <= maxLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:54] + "-" + hex.EncodeToString(sum[:])[:8]
}

// buildMCPServerEndpoint constructs the service endpoint URL for an MCPServer.
func buildMCPServerEndpoint(mcpServer *kubemootv1alpha1.MCPServer) string {
	port := int32(3000)
	if mcpServer.Spec.Port != 0 {
		port = mcpServer.Spec.Port
	}

	if mcpServer.Spec.Transport != kubemootv1alpha1.TransportStdio {
		return fmt.Sprintf(svcEndpointFmt, boundedName(mcpServer.Name), mcpServer.Namespace, port)
	}

	if !mcpServerProxyEnabled(mcpServer) {
		return ""
	}

	proxyPort := mcpServerProxyPort(mcpServer)
	return fmt.Sprintf(svcEndpointFmt, boundedName(mcpServer.Name), mcpServer.Namespace, proxyPort)
}

// ensureToolIndexed creates or updates a RAGSource for indexing tools from this MCPServer.
// Requires a vector store endpoint to be configured in KubemootConfig — skips when not set.
func (r *MCPServerReconciler) ensureToolIndexed(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer) error {
	log := logf.FromContext(ctx)

	// Skip tool indexing when no vector store endpoint is configured
	vectorStoreEndpoint := r.ConfigCache.GetVectorStoreEndpoint()
	if vectorStoreEndpoint == "" {
		log.V(1).Info("Skipping tool indexing: no vectorStoreEndpoint configured in KubemootConfig", "mcpServer", mcpServer.Name)
		return nil
	}

	ragSourceName := boundedName(mcpServer.Name + "-tools")

	// Check if RAGSource already exists
	existingRAGSource := &kubemootv1alpha1.RAGSource{}
	err := r.Get(ctx, types.NamespacedName{Name: ragSourceName, Namespace: mcpServer.Namespace}, existingRAGSource)
	if err == nil {
		// RAGSource exists, nothing to do
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Create RAGSource for tool indexing
	log.Info("Creating RAGSource for tool indexing", "mcpServer", mcpServer.Name, "ragSource", ragSourceName)

	endpoint := fmt.Sprintf(svcEndpointFmt, boundedName(mcpServer.Name), mcpServer.Namespace, mcpServer.Spec.Port)
	if mcpServer.Spec.Port == 0 {
		endpoint = fmt.Sprintf("http://%s.%s:3000", boundedName(mcpServer.Name), mcpServer.Namespace)
	}

	ragSource := &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ragSourceName,
			Namespace: mcpServer.Namespace,
			Labels: map[string]string{
				labelManagedBy:          managedByValue,
				labelComponent:          "tool-index",
				"kubemoot.ai/mcpserver": boundedName(mcpServer.Name),
			},
		},
		Spec: kubemootv1alpha1.RAGSourceSpec{
			Source: kubemootv1alpha1.SourceConfig{
				Type: kubemootv1alpha1.RAGSourceTypeMCPRegistry,
				MCPRegistry: &kubemootv1alpha1.RAGMCPRegistrySource{
					URL:  endpoint,
					Type: "custom",
				},
			},
			VectorStore: kubemootv1alpha1.VectorStoreConfig{
				Type:       kubemootv1alpha1.VectorStorePgvector,
				Endpoint:   vectorStoreEndpoint,
				Collection: "mcp_tools",
			},
			EmbeddingModelRef: r.ConfigCache.GetEmbeddingModel(),
		},
	}

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(mcpServer, ragSource, r.Scheme); err != nil {
		return err
	}

	return r.Create(ctx, ragSource)
}

// Status update options
type statusOption func(*kubemootv1alpha1.MCPServerStatus)

func withEndpoint(endpoint string) statusOption {
	return func(s *kubemootv1alpha1.MCPServerStatus) {
		s.Endpoint = endpoint
	}
}

func withReplicas(replicas, available int32) statusOption {
	return func(s *kubemootv1alpha1.MCPServerStatus) {
		s.Replicas = replicas
		s.AvailableReplicas = available
	}
}

func withCapabilities(caps []string) statusOption {
	return func(s *kubemootv1alpha1.MCPServerStatus) {
		s.Capabilities = caps
	}
}

// updateStatus updates the MCPServer status
func (r *MCPServerReconciler) updateStatus(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer, phase string, ready bool, message string, opts ...statusOption) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	mcpServer.Status.Phase = phase
	mcpServer.Status.Ready = ready
	mcpServer.Status.Message = message

	// Apply options
	for _, opt := range opts {
		opt(&mcpServer.Status)
	}

	// Set condition
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
	meta.SetStatusCondition(&mcpServer.Status.Conditions, condition)

	if err := r.Status().Update(ctx, mcpServer); err != nil {
		log.Error(err, "Failed to update MCPServer status")
		return ctrl.Result{}, err
	}

	// Publish status change to NATS
	if r.NATSPublisher != nil {
		_ = r.NATSPublisher.Publish(
			fmt.Sprintf("kubemoot.operator.mcpserver.%s.status", mcpServer.Name),
			map[string]interface{}{
				"name":     mcpServer.Name,
				"phase":    phase,
				"ready":    ready,
				"replicas": mcpServer.Status.Replicas,
				"message":  message,
			},
		)
	}

	// Requeue to check deployment progress
	if !ready {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// recordDeploymentTrial creates or updates an MCPServerReport with the deployment outcome
func (r *MCPServerReconciler) recordDeploymentTrial(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer, success bool, message string) {
	log := logf.FromContext(ctx)

	// Build a sanitized report name from the MCPServer name
	reportName := boundedName(mcpServer.Name)

	// Look up or create the MCPServerReport
	report := &kubemootv1alpha1.MCPServerReport{}
	err := r.Get(ctx, types.NamespacedName{Name: reportName, Namespace: mcpServer.Namespace}, report)

	if errors.IsNotFound(err) {
		// Create new report
		report = &kubemootv1alpha1.MCPServerReport{
			ObjectMeta: metav1.ObjectMeta{
				Name:      reportName,
				Namespace: mcpServer.Namespace,
				Labels: map[string]string{
					labelManagedBy: managedByValue,
				},
			},
			Spec: kubemootv1alpha1.MCPServerReportSpec{
				ServerName:   mcpServer.Name,
				RegistryType: mcpServer.Annotations["kubemoot.ai/registry-type"],
				GitHubURL:    mcpServer.Annotations["kubemoot.ai/github-url"],
			},
		}
		if err := r.Create(ctx, report); err != nil {
			log.Error(err, "Failed to create MCPServerReport", "report", reportName)
			return
		}
		log.Info("Created MCPServerReport", "report", reportName)
	} else if err != nil {
		log.Error(err, "Failed to get MCPServerReport", "report", reportName)
		return
	}

	// Build trial record
	now := metav1.Now()
	trial := kubemootv1alpha1.TrialRecord{
		Version:   mcpServer.Labels[labelVersion],
		Transport: string(mcpServer.Spec.Transport),
		Image:     mcpServer.Spec.Image,
		TestedAt:  &now,
		Phase:     string(kubemootv1alpha1.TrialPhaseDeploy),
		Success:   success,
	}
	if !success {
		trial.ErrorMessage = message
	}

	// Append trial, trim to max
	report.Status.Trials = append(report.Status.Trials, trial)
	if len(report.Status.Trials) > maxReportTrials {
		report.Status.Trials = report.Status.Trials[len(report.Status.Trials)-maxReportTrials:]
	}

	// Ensure verdict is set (CRD enum validation rejects empty string)
	if report.Status.Verdict == "" {
		report.Status.Verdict = "untested"
	}

	if err := r.Status().Update(ctx, report); err != nil {
		log.Error(err, "Failed to update MCPServerReport with trial", "report", reportName)
		return
	}

	// Publish trial event to NATS
	if r.NATSPublisher != nil {
		_ = r.NATSPublisher.Publish(
			fmt.Sprintf("kubemoot.chronicle.%s.trial", mcpServer.Name),
			map[string]interface{}{
				"server":    mcpServer.Name,
				"phase":     "deploy",
				"success":   success,
				"transport": string(mcpServer.Spec.Transport),
				"version":   mcpServer.Labels[labelVersion],
				"image":     mcpServer.Spec.Image,
				"message":   message,
			},
		)
	}
}

func int64Ptr(i int64) *int64 {
	return &i
}

func boolPtr(b bool) *bool {
	return &b
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.MCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("mcpserver").
		WithOptions(controller.Options{MaxConcurrentReconciles: 3}).
		// Re-reconcile every MCPServer when KubemootConfig changes so a
		// mcpBridge image bump propagates to the bridge sidecar without
		// per-CR annotation. See kubemootconfig_propagation.go.
		Watches(
			&kubemootv1alpha1.KubemootConfig{},
			enqueueAllOnKubemootConfigChange(mgr.GetClient(),
				func() client.ObjectList { return &kubemootv1alpha1.MCPServerList{} },
				"mcpserver"),
		).
		Complete(r)
}
