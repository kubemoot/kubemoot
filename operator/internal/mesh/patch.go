/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package mesh builds the part of an agent pod that belongs to the Moot Mesh: the sidecar, its
// volumes, and the loopback environment the harness gets. ApplySidecar is the one definition of
// that shape. The Agent controller calls it when it builds a pod in sidecar mode; the mutating
// webhook that joins pods Kubemoot does not build will call the same function.
package mesh

import (
	"fmt"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	meshcontract "github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

// SidecarContainerName is the native sidecar's name. A pod that already has a container of this
// name is left as it is.
const SidecarContainerName = "kubemoot-sidecar"

// Names of the volumes ApplySidecar adds.
const (
	volumeContract = "mesh-contract"
	volumePolicy   = "mesh-policy"
	volumeSkills   = "mesh-skills"
	volumeNATS     = "mesh-nats"
)

// Environment variables the harness receives. The harness learns two loopback URLs and its own
// port, and nothing about NATS, the gateway or the cluster.
const (
	EnvModelURL    = "KUBEMOOT_MODEL_URL"
	EnvToolsURL    = "KUBEMOOT_TOOLS_URL"
	EnvTurnPort    = "KUBEMOOT_TURN_PORT"
	EnvNativeTools = "KUBEMOOT_NATIVE_TOOLS"
)

// agentCardPath is where an A2A harness serves its card; the harness is ready when it answers.
const agentCardPath = "/.well-known/agent-card.json"

// SidecarPatch is everything the patch needs. It holds no controller state, so any caller that
// can name these values can join a pod to the mesh.
type SidecarPatch struct {
	// Image is the sidecar image.
	Image      string
	PullPolicy corev1.PullPolicy
	// HTTPPort is the sidecar's pod-facing port (health, metrics, chat); TurnPort is the
	// harness's.
	HTTPPort int32
	TurnPort int32
	// ContractConfigMap holds contract.yaml. PolicyConfigMap and SkillsConfigMap are optional
	// mounts for the instructions; NATSCredentialsSecret is an optional Secret with the
	// sidecar's own NATS credentials.
	ContractConfigMap     string
	PolicyConfigMap       string
	SkillsConfigMap       string
	NATSCredentialsSecret string
	// NativeTools is handed to the harness image, which applies it to its own configuration.
	NativeTools meshcontract.NativeTools
	// HarnessEnv is extra environment for the harness, set after the mesh variables.
	HarnessEnv []corev1.EnvVar
}

// ApplySidecar joins the pod to the mesh: it adds the sidecar as a native sidecar container
// (an init container that keeps running), mounts the contract, takes away the pod's
// service-account token, and gives the container named harnessContainer the loopback
// environment and a readiness probe on its agent card. It returns an error when the harness
// container is missing and changes nothing in that case. A pod that already has the sidecar is
// left untouched.
func ApplySidecar(tmpl *corev1.PodTemplateSpec, harnessContainer string, p SidecarPatch) error {
	if hasContainer(tmpl.Spec.InitContainers, SidecarContainerName) {
		return nil
	}
	idx := slices.IndexFunc(tmpl.Spec.Containers, func(c corev1.Container) bool { return c.Name == harnessContainer })
	if idx < 0 {
		return fmt.Errorf("the pod has no container named %q to join to the mesh", harnessContainer)
	}
	tmpl.Spec.AutomountServiceAccountToken = new(bool)
	tmpl.Spec.Volumes = append(tmpl.Spec.Volumes, volumes(p)...)
	tmpl.Spec.InitContainers = append(tmpl.Spec.InitContainers, sidecarContainer(p))
	patchHarness(&tmpl.Spec.Containers[idx], p)
	return nil
}

func hasContainer(list []corev1.Container, name string) bool {
	return slices.ContainsFunc(list, func(c corev1.Container) bool { return c.Name == name })
}

func volumes(p SidecarPatch) []corev1.Volume {
	out := []corev1.Volume{configMapVolume(volumeContract, p.ContractConfigMap, false)}
	if p.PolicyConfigMap != "" {
		out = append(out, configMapVolume(volumePolicy, p.PolicyConfigMap, true))
	}
	if p.SkillsConfigMap != "" {
		out = append(out, configMapVolume(volumeSkills, p.SkillsConfigMap, true))
	}
	if p.NATSCredentialsSecret != "" {
		optional := true
		out = append(out, corev1.Volume{Name: volumeNATS, VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: p.NATSCredentialsSecret, Optional: &optional},
		}})
	}
	return out
}

// configMapVolume is a ConfigMap volume; an optional one never blocks the pod from starting.
func configMapVolume(name, configMap string, optional bool) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: configMap},
		Optional:             &optional,
	}}}
}

func mounts(p SidecarPatch) []corev1.VolumeMount {
	out := []corev1.VolumeMount{{Name: volumeContract, MountPath: meshcontract.ContractDir, ReadOnly: true}}
	if p.PolicyConfigMap != "" {
		out = append(out, corev1.VolumeMount{Name: volumePolicy, MountPath: meshcontract.PolicyDir, ReadOnly: true})
	}
	if p.SkillsConfigMap != "" {
		out = append(out, corev1.VolumeMount{Name: volumeSkills, MountPath: meshcontract.SkillsDir, ReadOnly: true})
	}
	if p.NATSCredentialsSecret != "" {
		out = append(out, corev1.VolumeMount{Name: volumeNATS, MountPath: meshcontract.NATSCredentialsDir, ReadOnly: true})
	}
	return out
}

func sidecarContainer(p SidecarPatch) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	port := intstr.FromInt32(p.HTTPPort)
	return corev1.Container{
		Name:            SidecarContainerName,
		Image:           p.Image,
		ImagePullPolicy: p.PullPolicy,
		RestartPolicy:   &always,
		Args:            []string{"--config=" + meshcontract.ContractDir + "/" + meshcontract.ContractFile},
		Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: p.HTTPPort, Protocol: corev1.ProtocolTCP}},
		VolumeMounts:    mounts(p),
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("25m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		SecurityContext: hardened(true),
		// The start of the harness waits on this probe, so it must not depend on the harness:
		// it passes when the sidecar listens. Readiness is state (NATS connected, the harness
		// card answered) and gates traffic only.
		StartupProbe: &corev1.Probe{
			ProbeHandler:     corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: port}},
			PeriodSeconds:    2,
			FailureThreshold: 60,
		},
		ReadinessProbe: httpProbe("/readyz", port, 5),
		LivenessProbe:  httpProbe("/healthz", port, 30),
	}
}

func httpProbe(path string, port intstr.IntOrString, period int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: port}},
		PeriodSeconds: period,
	}
}

// hardened is the container security context of the mesh: no privilege, no capability, the
// runtime's default seccomp profile, and optionally a read-only root file system.
func hardened(readOnlyRoot bool) *corev1.SecurityContext {
	no, yes := false, true
	return &corev1.SecurityContext{
		RunAsNonRoot:             &yes,
		ReadOnlyRootFilesystem:   &readOnlyRoot,
		AllowPrivilegeEscalation: &no,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// Hardened is the security context the Agent controller gives a harness: the mesh's, except
// that a harness may write to its own root file system (its home directory, caches).
func Hardened() *corev1.SecurityContext { return hardened(false) }

func patchHarness(c *corev1.Container, p SidecarPatch) {
	native := p.NativeTools
	if native == "" {
		native = meshcontract.NativeToolsNone
	}
	meshEnv := []corev1.EnvVar{
		{Name: EnvModelURL, Value: "http://" + meshcontract.ModelDoorAddr + "/v1"},
		{Name: EnvToolsURL, Value: "http://" + meshcontract.ToolsDoorAddr + "/mcp"},
		{Name: EnvTurnPort, Value: strconv.Itoa(int(p.TurnPort))},
		{Name: EnvNativeTools, Value: string(native)},
	}
	for _, e := range append(meshEnv, p.HarnessEnv...) {
		c.Env = setEnv(c.Env, e)
	}
	if !slices.ContainsFunc(c.Ports, func(cp corev1.ContainerPort) bool { return cp.ContainerPort == p.TurnPort }) {
		c.Ports = append(c.Ports, corev1.ContainerPort{Name: "turn", ContainerPort: p.TurnPort, Protocol: corev1.ProtocolTCP})
	}
	// A harness is ready when it answers its agent card, however its image defines readiness.
	c.ReadinessProbe = httpProbe(agentCardPath, intstr.FromInt32(p.TurnPort), 5)
}

// setEnv sets one variable, replacing an earlier one of the same name.
func setEnv(env []corev1.EnvVar, e corev1.EnvVar) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == e.Name {
			env[i] = e
			return env
		}
	}
	return append(env, e)
}
