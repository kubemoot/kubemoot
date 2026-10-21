/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/mesh"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	meshcontract "github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

// Names and keys of an Agent whose harness runs beside a Moot Mesh sidecar.
const (
	// harnessContainerName is the harness's container, whatever the harness is.
	harnessContainerName = "agent"
	// labelHarnessMode marks the pods and Deployments of sidecar-mode agents; the chart's
	// PodMonitors tell them from legacy agents by it.
	labelHarnessMode = "kubemoot.ai/harness-mode"
	// annoNativeTools records what the harness's own tools may do, for the dashboard.
	annoNativeTools = "kubemoot.ai/native-tools"
	// contractHashAnnotation carries the hash of contract.yaml on the pod template, so a
	// change to the contract rolls the pod.
	contractHashAnnotation = "kubemoot.ai/contract-hash"
	// turnDeadlineSeconds is the safety net for one turn; the normal path ends on state.
	turnDeadlineSeconds = 300
	// envPlacementServiceURL names the operator's placement service setting.
	envPlacementServiceURL = "PLACEMENT_SERVICE_URL"
	// defaultHarnessPort is where a harness serves the turn when the Agent names no port.
	defaultHarnessPort = meshcontract.DefaultTurnPort
)

// usesSidecar reports whether the Agent's harness runs beside a Moot Mesh sidecar. An Agent
// without a harness block is a legacy agent-runtime pod.
func usesSidecar(agent *kubemootv1alpha1.Agent) bool {
	return agent.Spec.Harness != nil && agent.Spec.Harness.Mode == kubemootv1alpha1.HarnessModeSidecar
}

// harnessPort is the port the harness serves the turn on.
func harnessPort(h *kubemootv1alpha1.HarnessSpec) int32 {
	if h.Port > 0 {
		return h.Port
	}
	return defaultHarnessPort
}

// harnessImage is the harness image: harness.image, else the deployment's image.
func harnessImage(agent *kubemootv1alpha1.Agent) string {
	if agent.Spec.Harness.Image != "" {
		return agent.Spec.Harness.Image
	}
	if agent.Spec.Deployment != nil {
		return agent.Spec.Deployment.Image
	}
	return ""
}

// sidecarHTTPPort is the sidecar's pod-facing port, which the Service targets.
func sidecarHTTPPort(agent *kubemootv1alpha1.Agent) int32 {
	if agent.Spec.Deployment != nil && agent.Spec.Deployment.Port > 0 {
		return agent.Spec.Deployment.Port
	}
	return meshcontract.DefaultHTTPPort
}

// validateSidecarAgent reports why a sidecar-mode Agent cannot be built. The crew label is
// required because every bus subject is scoped by it.
func validateSidecarAgent(agent *kubemootv1alpha1.Agent) error {
	var errs []error
	if agent.Labels[labelCrew] == "" {
		errs = append(errs, fmt.Errorf("harness mode sidecar needs the %s label: the sidecar scopes every subject by crew", labelCrew))
	}
	if isCoordinator(agent) {
		errs = append(errs, errors.New("a crew's coordinator runs in legacy mode: its consensus state machine is not a harness seat"))
	}
	if harnessImage(agent) == "" {
		errs = append(errs, errors.New("harness mode sidecar needs harness.image or deployment.image"))
	}
	if hp, sp := harnessPort(agent.Spec.Harness), sidecarHTTPPort(agent); hp == sp {
		errs = append(errs, fmt.Errorf("harness.port and deployment.port are both %d: the sidecar and the harness need different ports", hp))
	}
	return errors.Join(errs...)
}

// aliasFor builds a model alias from a phase pick: its ranked candidates, preferred first, and
// the server the pick is bound to as the floor when the placement service cannot be reached.
func aliasFor(pick *modelPick) meshcontract.Alias {
	a := meshcontract.Alias{Candidates: []string{pick.ModelID}}
	if len(pick.Candidates) > 0 {
		a.Candidates = a.Candidates[:0]
		for _, c := range pick.Candidates {
			a.Candidates = append(a.Candidates, c.Model)
		}
	}
	if pick.Server != nil {
		a.Fallback = &meshcontract.Endpoint{Server: pick.Server.Name, Endpoint: pick.Endpoint, Model: pick.ModelID}
	}
	return a
}

// buildContract is the contract.yaml content for a sidecar-mode Agent.
func (r *AgentReconciler) buildContract(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) meshcontract.Contract {
	h := agent.Spec.Harness
	id := agentIdentity(agent)
	concurrency := int(h.Concurrency)
	if concurrency < 1 {
		concurrency = 1
	}
	c := meshcontract.Contract{
		ContractVersion: meshcontract.ContractVersion,
		Identity:        id,
		HTTPPort:        int(sidecarHTTPPort(agent)),
		Bus: meshcontract.Bus{NATSURL: agentNATSURL(), Stream: bus.DiscussStream, Consumer: id.ConsumerName(),
			CredentialsDir: meshcontract.NATSCredentialsDir},
		Turn: meshcontract.Turn{
			URL:         fmt.Sprintf("http://127.0.0.1:%d", harnessPort(h)),
			Concurrency: concurrency, DeadlineSeconds: turnDeadlineSeconds,
		},
		Models: meshcontract.Models{Aliases: map[string]meshcontract.Alias{
			meshcontract.DefaultAlias: aliasFor(mulling), "triage": aliasFor(triage),
		}},
		Placement: meshcontract.Placement{
			Policy: r.placementPolicyEnvVar(ctx, agent).Value, ServiceURL: os.Getenv(envPlacementServiceURL),
		},
		Tools: meshcontract.Tools{
			Enabled: agent.Spec.EnabledTools, Disabled: agent.Spec.DisabledTools,
			NativeTools: meshcontract.NativeTools(stringOrDefault(h.NativeTools, string(meshcontract.NativeToolsNone))),
		},
		Policy: &meshcontract.Policy{
			SystemPromptFile: meshcontract.PolicyDir + "/" + meshcontract.SystemPromptFile,
			SkillsDir:        meshcontract.SkillsDir,
		},
	}
	if name, port, ok := r.findCrewGateway(ctx, agent.Namespace); ok {
		c.Tools.Gateway = fmt.Sprintf("http://%s.%s:%d", name, agent.Namespace, port)
	}
	if r.ConfigCache != nil {
		if otlp := r.ConfigCache.GetOTelCollectorEndpoint(); otlp != "" {
			c.Observability = &meshcontract.Observability{OTLPEndpoint: otlp}
		}
	}
	return c
}

// sidecarContractData validates a sidecar-mode Agent and renders its contract.yaml. An error
// here is a fault in the Agent, not a transient failure.
func (r *AgentReconciler) sidecarContractData(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) (string, error) {
	if err := validateSidecarAgent(agent); err != nil {
		return "", err
	}
	data, err := r.buildContract(ctx, agent, mulling, triage).Marshal()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// agentIdentity is who the agent is on the bus. The contract and the durable consumer both come
// from it, so they cannot name different consumers.
func agentIdentity(agent *kubemootv1alpha1.Agent) meshcontract.Identity {
	id := meshcontract.Identity{
		Namespace: agent.Namespace, Crew: agent.Labels[labelCrew], Agent: agent.Name,
		Role: agent.Spec.DiscussRole, Channels: agent.Spec.DiscussChannels, TriageSummary: agent.Spec.TriageSummary,
	}
	if agent.Spec.Harness != nil {
		id.Harness = agent.Spec.Harness.Name
	}
	return id
}

func contractConfigMapName(agent *kubemootv1alpha1.Agent) string { return agent.Name + "-contract" }

// ensureContractConfigMap writes the Agent's contract.yaml ConfigMap.
func (r *AgentReconciler) ensureContractConfigMap(ctx context.Context, agent *kubemootv1alpha1.Agent, data string) error {
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: contractConfigMapName(agent), Namespace: agent.Namespace,
			Labels: map[string]string{labelManagedBy: managedByValue, labelAgent: agent.Name},
		},
		Data: map[string]string{meshcontract.ContractFile: data},
	}
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner ref on the contract ConfigMap: %w", err)
	}
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: agent.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if existing.Data[meshcontract.ContractFile] == data {
		return nil
	}
	existing.Data = desired.Data
	return r.Update(ctx, existing)
}

// consumerEnsurer is the part of the NATS publisher that creates a durable consumer.
type consumerEnsurer interface {
	EnsureConsumer(stream, consumerName string, filterSubjects []string) error
}

// ensureSidecarConsumer creates the durable consumer the agent's sidecar binds by name. A
// failure is logged and never fails the reconcile: the sidecar subscribes on core NATS until the
// consumer exists, and the next reconcile tries again.
func ensureSidecarConsumer(ctx context.Context, e consumerEnsurer, agent *kubemootv1alpha1.Agent) {
	id := agentIdentity(agent)
	if err := e.EnsureConsumer(bus.DiscussStream, id.ConsumerName(), id.DiscussFilters()); err != nil {
		logf.FromContext(ctx).Info("Could not ensure the sidecar's JetStream consumer", "consumer", id.ConsumerName(), "error", err.Error())
	}
}

// ensureAgentConfig writes the ConfigMaps the Deployment mounts: the policy always, and for a
// sidecar-mode Agent the contract, after validating the Agent, and the sidecar's consumer. stop
// is true when the Agent cannot be built (its status says why) or the reconcile failed.
func (r *AgentReconciler) ensureAgentConfig(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick, policyCMName string) (stop bool, err error) {
	if err := r.ensurePolicyConfigMap(ctx, agent, policyCMName); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to ensure policy ConfigMap")
	}
	if !usesSidecar(agent) {
		return false, nil
	}
	return r.reconcileHarness(ctx, agent, mulling, triage)
}

// reconcileHarness does the sidecar-mode work beside the Deployment.
func (r *AgentReconciler) reconcileHarness(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) (stop bool, err error) {
	data, invalid := r.sidecarContractData(ctx, agent, mulling, triage)
	if invalid != nil {
		agent.Status.Phase = "Error"
		agent.Status.Ready = false
		agent.Status.Message = invalid.Error()
		if uerr := r.Status().Update(ctx, agent); uerr != nil && !apierrors.IsNotFound(uerr) {
			logf.FromContext(ctx).Error(uerr, "Failed to record the harness error in the Agent status")
		}
		return true, nil
	}
	if err := r.ensureContractConfigMap(ctx, agent, data); err != nil {
		return true, err
	}
	if r.NATSPublisher != nil {
		ensureSidecarConsumer(ctx, r.NATSPublisher, agent)
	}
	return false, nil
}

// buildSidecarDeployment builds the Deployment of a sidecar-mode Agent: the harness container
// alone with the mesh patch applied, and none of the legacy runtime's environment, policy
// mount or cluster credentials.
func (r *AgentReconciler) buildSidecarDeployment(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick, policyCMName, promptHash string) *appsv1.Deployment {
	h := agent.Spec.Harness
	replicas, _, _, serviceAccountName := r.resolveDeploymentParams(agent)
	container := corev1.Container{
		Name: harnessContainerName, Image: harnessImage(agent), ImagePullPolicy: corev1.PullIfNotPresent,
		Command: h.Command, Args: h.Args, Env: append([]corev1.EnvVar(nil), h.Env...),
		SecurityContext: mesh.Hardened(),
	}
	if h.Resources != nil {
		container.Resources = *h.Resources
	}
	labels := podLabels(agent)
	labels[labelHarnessMode] = string(kubemootv1alpha1.HarnessModeSidecar)
	tmpl := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{promptHashAnnotation: promptHash}},
		Spec: corev1.PodSpec{
			ServiceAccountName: serviceAccountName, ImagePullSecrets: r.imagePullSecrets(),
			Containers: []corev1.Container{container},
		},
	}
	if data, err := r.sidecarContractData(ctx, agent, mulling, triage); err == nil {
		tmpl.Annotations[contractHashAnnotation] = hashString(data)
	}
	if err := mesh.ApplySidecar(&tmpl, harnessContainerName, r.sidecarPatch(agent, policyCMName)); err != nil {
		logf.FromContext(ctx).Error(err, "Could not join the pod to the Moot Mesh")
	}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: agent.Name, Namespace: agent.Namespace, Labels: sidecarDeploymentLabels(agent, mulling, triage),
			Annotations: map[string]string{annoNativeTools: stringOrDefault(h.NativeTools, string(meshcontract.NativeToolsNone))},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{labelAgent: agent.Name}},
			Template: tmpl,
		},
	}
	d.Annotations[deploymentHashAnnotation] = computeDeploymentHash(d)
	return d
}

// sidecarDeploymentLabels are the Deployment labels of a sidecar-mode Agent: the legacy ones
// plus the mode.
func sidecarDeploymentLabels(agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) map[string]string {
	labels := buildDeploymentLabels(agent, mulling, triage)
	labels[labelHarnessMode] = string(kubemootv1alpha1.HarnessModeSidecar)
	return labels
}

// sidecarPatch collects what the pod patch needs from the Agent and the operator's settings.
func (r *AgentReconciler) sidecarPatch(agent *kubemootv1alpha1.Agent, policyCMName string) mesh.SidecarPatch {
	image := FallbackAgentSidecarImage
	if r.ConfigCache != nil {
		image = r.ConfigCache.GetAgentSidecarImage()
	}
	p := mesh.SidecarPatch{
		Image: image, PullPolicy: corev1.PullIfNotPresent,
		HTTPPort: sidecarHTTPPort(agent), TurnPort: harnessPort(agent.Spec.Harness),
		ContractConfigMap: contractConfigMapName(agent), PolicyConfigMap: policyCMName,
		NATSCredentialsSecret: agent.Name + "-nats",
		NativeTools:           meshcontract.NativeTools(stringOrDefault(agent.Spec.Harness.NativeTools, string(meshcontract.NativeToolsNone))),
	}
	if crew := agent.Labels[labelCrew]; crew != "" {
		p.SkillsConfigMap = "crew-" + crew + "-skills"
	}
	return p
}
