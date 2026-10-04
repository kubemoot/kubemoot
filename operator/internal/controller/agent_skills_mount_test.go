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
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestSkillsVolumeMountCrewAgent verifies that a crew agent's pod template
// contains:
//   - a "skills" volume sourced from ConfigMap "crew-<crew>-skills" with optional=true
//   - a read-only VolumeMount for "skills" at /app/config/skills
//   - env var KUBEMOOT_SKILLS_DIR=/app/config/skills
func TestSkillsVolumeMountCrewAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: cli}

	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "k8s-tooler",
			Namespace: testCrewNamespace,
			Labels: map[string]string{
				labelCrew: testCrewNamespace,
			},
		},
	}
	pick := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}

	d := r.buildDeployment(context.Background(), agent, pick, pick, "k8s-tooler-policy", "abc123")
	podSpec := d.Spec.Template.Spec

	// --- Volume ---
	assertCrewSkillsVolume(t, podSpec)

	// --- VolumeMount ---
	container := podSpec.Containers[0]
	assertCrewSkillsMount(t, container)

	// --- Env var ---
	assertCrewSkillsEnv(t, container)
}

// assertCrewSkillsVolume checks the optional skills ConfigMap volume of a crew agent.
func assertCrewSkillsVolume(t *testing.T, podSpec corev1.PodSpec) {
	t.Helper()
	skillsVolume := findVolumeByName(podSpec.Volumes, testSkills)
	if skillsVolume == nil {
		t.Fatal("skills volume missing from pod template")
	}
	if skillsVolume.ConfigMap == nil {
		t.Fatal("skills volume: expected ConfigMap source")
	}
	wantCMName := "crew-crew-x-skills"
	if got := skillsVolume.ConfigMap.Name; got != wantCMName {
		t.Errorf("skills volume ConfigMap name: got %q, want %q", got, wantCMName)
	}
	if skillsVolume.ConfigMap.Optional == nil || !*skillsVolume.ConfigMap.Optional {
		t.Errorf("skills volume must be optional=true so pod starts when ConfigMap is absent")
	}
}

// assertCrewSkillsMount checks the read-only skills mount of a crew agent container.
func assertCrewSkillsMount(t *testing.T, container corev1.Container) {
	t.Helper()
	skillsMount := findMountByName(container.VolumeMounts, testSkills)
	if skillsMount == nil {
		t.Fatal("skills VolumeMount missing from agent container")
	}
	if skillsMount.MountPath != skillsMountPath {
		t.Errorf("skills mount path: got %q, want %q", skillsMount.MountPath, skillsMountPath)
	}
	if !skillsMount.ReadOnly {
		t.Errorf("skills VolumeMount must be ReadOnly=true")
	}
}

// assertCrewSkillsEnv checks KUBEMOOT_SKILLS_DIR on a crew agent container.
func assertCrewSkillsEnv(t *testing.T, container corev1.Container) {
	t.Helper()
	v, ok := envValue(container.Env, "KUBEMOOT_SKILLS_DIR")
	if !ok {
		t.Fatal("KUBEMOOT_SKILLS_DIR env var missing for crew agent")
	}
	if v != skillsMountPath {
		t.Errorf("KUBEMOOT_SKILLS_DIR: got %q, want %q", v, skillsMountPath)
	}
}

// TestSkillsVolumeMountAbsentForCrewlessAgent verifies that an agent with no
// crew label does NOT get the skills volume, mount, or env var. Such agents have
// no crew ConfigMap to mount; adding an optional mount would still create a
// dangling volume reference for a non-existent ConfigMap name.
func TestSkillsVolumeMountAbsentForCrewlessAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: cli}

	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "standalone-agent",
			Namespace: testDefault,
			// No crew label.
		},
	}
	pick := &modelPick{ModelID: testModelID, Endpoint: testOllamaURL}

	d := r.buildDeployment(context.Background(), agent, pick, pick, "standalone-agent-policy", "abc123")
	podSpec := d.Spec.Template.Spec

	for _, v := range podSpec.Volumes {
		if v.Name == testSkills {
			t.Errorf("skills volume must be absent for crew-less agent; found it")
		}
	}

	container := podSpec.Containers[0]
	for _, m := range container.VolumeMounts {
		if m.Name == testSkills {
			t.Errorf("skills VolumeMount must be absent for crew-less agent; found it")
		}
	}

	if v, ok := envValue(container.Env, "KUBEMOOT_SKILLS_DIR"); ok {
		t.Errorf("KUBEMOOT_SKILLS_DIR must be absent for crew-less agent; got %q", v)
	}
}

// findVolumeByName returns the named volume from a pod template, or nil.
func findVolumeByName(volumes []corev1.Volume, name string) *corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}
	return nil
}

// findMountByName returns the named volume mount from a container, or nil.
func findMountByName(mounts []corev1.VolumeMount, name string) *corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == name {
			return &mounts[i]
		}
	}
	return nil
}
