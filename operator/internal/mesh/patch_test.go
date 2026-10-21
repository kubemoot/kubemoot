/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mesh

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	meshcontract "github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

func template() *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "agent", Image: "harness:1", Env: []corev1.EnvVar{{Name: "KEEP", Value: "me"}, {Name: EnvNativeTools, Value: "stale"}},
	}}}}
}

func patch() SidecarPatch {
	return SidecarPatch{
		Image: "agent-sidecar:1", PullPolicy: corev1.PullIfNotPresent, HTTPPort: 8080, TurnPort: 9000,
		ContractConfigMap: "a-contract", PolicyConfigMap: "a-policy", SkillsConfigMap: "crew-x-skills",
		NATSCredentialsSecret: "a-nats", NativeTools: meshcontract.NativeToolsDeclared,
		HarnessEnv: []corev1.EnvVar{{Name: "EXTRA", Value: "1"}},
	}
}

func applied(t *testing.T) *corev1.PodTemplateSpec {
	t.Helper()
	tmpl := template()
	if err := ApplySidecar(tmpl, "agent", patch()); err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func env(c corev1.Container, name string) (string, int) {
	n, v := 0, ""
	for _, e := range c.Env {
		if e.Name == name {
			n++
			v = e.Value
		}
	}
	return v, n
}

// facts compares named values with what is expected, so one test states many fields.
type facts map[string][2]any

func (f facts) check(t *testing.T) {
	t.Helper()
	for name, pair := range f {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
		}
	}
}

func TestTheSidecarIsANativeSidecarThatStartsWithoutTheHarness(t *testing.T) {
	tmpl := applied(t)
	if len(tmpl.Spec.InitContainers) != 1 {
		t.Fatalf("want one init container, got %d", len(tmpl.Spec.InitContainers))
	}
	sc := tmpl.Spec.InitContainers[0]
	// The startup probe gates the harness's start, so it must be TCP on the sidecar's own port.
	facts{
		"name":            {sc.Name, SidecarContainerName},
		"image":           {sc.Image, "agent-sidecar:1"},
		"restart policy":  {*sc.RestartPolicy, corev1.ContainerRestartPolicyAlways},
		"args":            {sc.Args, []string{"--config=/etc/kubemoot/contract/contract.yaml"}},
		"startup port":    {sc.StartupProbe.TCPSocket.Port.IntValue(), 8080},
		"startup period":  {sc.StartupProbe.PeriodSeconds, int32(2)},
		"startup retries": {sc.StartupProbe.FailureThreshold, int32(60)},
		"readiness":       {sc.ReadinessProbe.HTTPGet.Path, "/readyz"},
		"liveness":        {sc.LivenessProbe.HTTPGet.Path, "/healthz"},
		"port name":       {sc.Ports[0].Name, "http"},
		"port":            {sc.Ports[0].ContainerPort, int32(8080)},
	}.check(t)
}

func TestTheSidecarIsSmallAndLockedDown(t *testing.T) {
	sc := applied(t).Spec.InitContainers[0]
	req, lim, s := sc.Resources.Requests, sc.Resources.Limits, sc.SecurityContext
	facts{
		"cpu request":     {req.Cpu().String(), "25m"},
		"memory request":  {req.Memory().String(), "32Mi"},
		"cpu limit":       {lim.Cpu().String(), "250m"},
		"memory limit":    {lim.Memory().String(), "64Mi"},
		"non root":        {*s.RunAsNonRoot, true},
		"read only root":  {*s.ReadOnlyRootFilesystem, true},
		"escalation":      {*s.AllowPrivilegeEscalation, false},
		"dropped caps":    {s.Capabilities.Drop, []corev1.Capability{"ALL"}},
		"seccomp profile": {s.SeccompProfile.Type, corev1.SeccompProfileTypeRuntimeDefault},
	}.check(t)
}

func TestOnlyTheSidecarMountsTheContractAndCredentials(t *testing.T) {
	tmpl := applied(t)
	got, writable := map[string]string{}, 0
	for _, m := range tmpl.Spec.InitContainers[0].VolumeMounts {
		got[m.MountPath] = m.Name
		if !m.ReadOnly {
			writable++
		}
	}
	facts{
		"sidecar mounts": {got, map[string]string{
			"/etc/kubemoot/contract": "mesh-contract", "/etc/kubemoot/policy": "mesh-policy",
			"/app/config/skills": "mesh-skills", "/etc/kubemoot/nats": "mesh-nats"}},
		"writable mounts": {writable, 0},
		"harness mounts":  {len(tmpl.Spec.Containers[0].VolumeMounts), 0},
	}.check(t)
}

func TestTheContractIsRequiredAndTheCredentialsAreOptional(t *testing.T) {
	optional := map[string]bool{}
	for _, v := range applied(t).Spec.Volumes {
		switch {
		case v.Secret != nil:
			optional[v.Name] = *v.Secret.Optional
		case v.ConfigMap != nil:
			optional[v.Name] = *v.ConfigMap.Optional
		}
	}
	facts{"optional volumes": {optional, map[string]bool{
		"mesh-contract": false, "mesh-policy": true, "mesh-skills": true, "mesh-nats": true}}}.check(t)
}

func TestThePodGetsNoServiceAccountToken(t *testing.T) {
	tmpl := applied(t)
	if tmpl.Spec.AutomountServiceAccountToken == nil || *tmpl.Spec.AutomountServiceAccountToken {
		t.Error("automountServiceAccountToken must be false")
	}
}

func TestTheHarnessGetsLoopbackURLsAndNoClusterAddresses(t *testing.T) {
	h := applied(t).Spec.Containers[0]
	got := map[string]string{}
	for _, e := range h.Env {
		got[e.Name] = e.Value
		if strings.Contains(strings.ToLower(e.Name+e.Value), "nats") {
			t.Errorf("the harness sees NATS: %s=%s", e.Name, e.Value)
		}
	}
	facts{
		"env": {got, map[string]string{
			EnvModelURL: "http://127.0.0.1:8081/v1", EnvToolsURL: "http://127.0.0.1:8082/mcp",
			EnvTurnPort: "9000", EnvNativeTools: "declared", "EXTRA": "1", "KEEP": "me"}},
		"env entries":     {len(h.Env), 6},
		"readiness path":  {h.ReadinessProbe.HTTPGet.Path, "/.well-known/agent-card.json"},
		"readiness port":  {h.ReadinessProbe.HTTPGet.Port.IntValue(), 9000},
		"turn port":       {h.Ports[len(h.Ports)-1].ContainerPort, int32(9000)},
		"harness volumes": {len(h.VolumeMounts), 0},
	}.check(t)
}

func TestNativeToolsDefaultToNone(t *testing.T) {
	tmpl := template()
	p := patch()
	p.NativeTools = ""
	if err := ApplySidecar(tmpl, "agent", p); err != nil {
		t.Fatal(err)
	}
	if got, _ := env(tmpl.Spec.Containers[0], EnvNativeTools); got != "none" {
		t.Errorf("nativeTools = %q", got)
	}
}

func TestOptionalMountsAreLeftOutWhenNotNamed(t *testing.T) {
	tmpl := template()
	p := patch()
	p.PolicyConfigMap, p.SkillsConfigMap, p.NATSCredentialsSecret = "", "", ""
	if err := ApplySidecar(tmpl, "agent", p); err != nil {
		t.Fatal(err)
	}
	if len(tmpl.Spec.Volumes) != 1 || len(tmpl.Spec.InitContainers[0].VolumeMounts) != 1 {
		t.Errorf("want only the contract, got %d volumes and %d mounts",
			len(tmpl.Spec.Volumes), len(tmpl.Spec.InitContainers[0].VolumeMounts))
	}
}

func TestApplyingTwiceChangesNothing(t *testing.T) {
	once := applied(t)
	twice := once.DeepCopy()
	if err := ApplySidecar(twice, "agent", patch()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(once, twice) {
		t.Error("a second application changed the pod")
	}
}

func TestAMissingHarnessContainerIsAnErrorAndLeavesThePodAlone(t *testing.T) {
	tmpl := template()
	before := tmpl.DeepCopy()
	if err := ApplySidecar(tmpl, "nope", patch()); err == nil {
		t.Fatal("want an error")
	}
	if !reflect.DeepEqual(before, tmpl) {
		t.Error("the pod changed although the patch failed")
	}
}

func TestAHarnessMayWriteToItsRootFileSystemButNothingElseIsLoosened(t *testing.T) {
	s := Hardened()
	if *s.ReadOnlyRootFilesystem || !*s.RunAsNonRoot || *s.AllowPrivilegeEscalation || s.Capabilities.Drop[0] != "ALL" {
		t.Errorf("harness security context = %+v", s)
	}
}
