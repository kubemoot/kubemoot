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
	"reflect"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	rolloutAgent     = "a1"
	rolloutOldImage  = "reg/agent-runtime:old"
	rolloutNewImage  = "reg/agent-runtime:new"
	rolloutCrew      = "crew1"
	rolloutTemp      = "0.1"
	envTemperature   = "KUBEMOOT_MODEL_TEMPERATURE"
	envExtra         = "EXTRA_FLAG"
	envPromptFile    = "KUBEMOOT_SYSTEM_PROMPT_FILE"
	envAgentName     = "KUBEMOOT_AGENT_NAME"
	envNamespaceName = "KUBEMOOT_NAMESPACE"
)

type rolloutRig struct {
	t   *testing.T
	cli client.Client
	r   *AgentReconciler
	req ctrl.Request
}

func newRolloutRig(t *testing.T, modelReady bool) *rolloutRig {
	t.Helper()
	scheme := gatewayScheme(t)
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: rolloutAgent, Namespace: testNS1, Labels: map[string]string{labelCrew: rolloutCrew}},
		Spec: kubemootv1alpha1.AgentSpec{
			Capabilities: []string{testReasoning}, Temperature: rolloutTemp,
			Deployment: &kubemootv1alpha1.DeploymentConfig{Image: rolloutOldImage},
		},
	}
	server := &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: testProvName, Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeOllama, Endpoint: testProvEndpoint},
		Status:     kubemootv1alpha1.ModelServerStatus{Ready: true},
	}
	model := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelSpec{Model: testModelID, ServerRef: testProvName},
		Status:     kubemootv1alpha1.ModelStatus{Ready: modelReady},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, server, model).
		WithStatusSubresource(agent, server, model).Build()
	return &rolloutRig{t: t, cli: cli,
		r:   &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()},
		req: ctrl.Request{NamespacedName: types.NamespacedName{Name: rolloutAgent, Namespace: testNS1}}}
}

func (g *rolloutRig) reconcile() {
	g.t.Helper()
	if _, err := g.r.Reconcile(context.Background(), g.req); err != nil {
		g.t.Fatalf("reconcile: %v", err)
	}
}

func (g *rolloutRig) setModelReady(ready bool) {
	g.t.Helper()
	m := &kubemootv1alpha1.Model{}
	if err := g.cli.Get(context.Background(), types.NamespacedName{Name: "m1", Namespace: testNS1}, m); err != nil {
		g.t.Fatal(err)
	}
	m.Status.Ready = ready
	if err := g.cli.Status().Update(context.Background(), m); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rolloutRig) editAgent(edit func(*kubemootv1alpha1.Agent)) {
	g.t.Helper()
	a := g.agent()
	edit(a)
	if err := g.cli.Update(context.Background(), a); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rolloutRig) agent() *kubemootv1alpha1.Agent {
	g.t.Helper()
	a := &kubemootv1alpha1.Agent{}
	if err := g.cli.Get(context.Background(), g.req.NamespacedName, a); err != nil {
		g.t.Fatal(err)
	}
	return a
}

func (g *rolloutRig) deployment() *appsv1.Deployment {
	g.t.Helper()
	d := &appsv1.Deployment{}
	if err := g.cli.Get(context.Background(), g.req.NamespacedName, d); err != nil {
		g.t.Fatalf("Deployment: %v", err)
	}
	return d
}

func rolloutEnv(d *appsv1.Deployment) map[string]string {
	out := map[string]string{}
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		out[e.Name] = e.Value
	}
	return out
}

// While no model is feasible, a changed Agent spec still reaches the Deployment (image, env,
// a removed temperature), the model values and server labels stay as they were, and the status
// stays Unschedulable with the same message.
func TestUnschedulableAgentDeploymentFollowsSpecChanges(t *testing.T) {
	g := newRolloutRig(t, true)
	g.reconcile()
	before := g.deployment()
	if rolloutEnv(before)[envTemperature] != rolloutTemp || rolloutEnv(before)[envModelModel] != testModelID {
		t.Fatalf("setup: %v", rolloutEnv(before))
	}

	g.setModelReady(false)
	g.editAgent(func(a *kubemootv1alpha1.Agent) {
		a.Spec.Temperature = ""
		a.Spec.Deployment.Image = rolloutNewImage
		a.Spec.Deployment.Env = []corev1.EnvVar{{Name: envExtra, Value: "on"}}
	})
	g.reconcile()

	after := g.deployment()
	assertSpecChangesRolledOut(t, after)
	assertModelValuesHeld(t, before, after)
	got := g.agent()
	if got.Status.Phase != testPhaseUnschedulable || got.Status.Ready || got.Status.Message == "" {
		t.Errorf("status must stay Unschedulable with a message, got %+v", got.Status)
	}
}

func assertSpecChangesRolledOut(t *testing.T, d *appsv1.Deployment) {
	t.Helper()
	env := rolloutEnv(d)
	if _, has := env[envTemperature]; has {
		t.Error("the removed temperature must leave the Deployment")
	}
	if env[envExtra] != "on" {
		t.Errorf("the new env var must reach the Deployment, got %v", env)
	}
	if got := d.Spec.Template.Spec.Containers[0].Image; got != rolloutNewImage {
		t.Errorf("image = %q, want %q", got, rolloutNewImage)
	}
}

func assertModelValuesHeld(t *testing.T, before, after *appsv1.Deployment) {
	t.Helper()
	was, env := rolloutEnv(before), rolloutEnv(after)
	for _, name := range []string{envModelModel, envModelEndpoint, envTriageModel, envTriageEndpoint, envOllamaBaseURL} {
		if env[name] != was[name] {
			t.Errorf("%s = %q, want the held %q", name, env[name], was[name])
		}
	}
	if !reflect.DeepEqual(after.Labels, before.Labels) {
		t.Errorf("server labels must be kept: %v vs %v", after.Labels, before.Labels)
	}
}

// Held values make an unchanged spec a no-op: the pods are not rolled for the missing model.
func TestUnschedulableAgentWithUnchangedSpecLeavesTheDeploymentAlone(t *testing.T) {
	g := newRolloutRig(t, true)
	g.reconcile()
	before := g.deployment()
	g.setModelReady(false)
	g.reconcile()
	after := g.deployment()
	if after.ResourceVersion != before.ResourceVersion {
		t.Errorf("Deployment was updated although nothing but the model went away")
	}
}

// An Agent that never had a model still gets its Deployment and Service, with no model values,
// and a later feasible model fills them in.
func TestAgentWithNoModelYetDeploysWithoutModelValuesAndGetsThemLater(t *testing.T) {
	g := newRolloutRig(t, false)
	g.reconcile()

	env := rolloutEnv(g.deployment())
	for _, name := range []string{envModelModel, envModelEndpoint, envTriageModel, envTriageEndpoint, envOllamaBaseURL} {
		if v, has := env[name]; has {
			t.Errorf("%s = %q, want unset while no model has been chosen", name, v)
		}
	}
	if env[envTemperature] != rolloutTemp {
		t.Errorf("spec-derived env must be present: %v", env)
	}
	if err := g.cli.Get(context.Background(), g.req.NamespacedName, &corev1.Service{}); err != nil {
		t.Errorf("the Service must exist: %v", err)
	}
	if p := g.agent().Status.Phase; p != testPhaseUnschedulable {
		t.Errorf("phase = %q, want Unschedulable", p)
	}

	g.setModelReady(true)
	g.reconcile()
	d := g.deployment()
	if rolloutEnv(d)[envModelModel] != testModelID || rolloutEnv(d)[envModelEndpoint] != testProvEndpoint {
		t.Errorf("a feasible model must fill the model values: %v", rolloutEnv(d))
	}
	if d.Labels[labelMullingServer] != testProvName {
		t.Errorf("the server label must be set: %v", d.Labels)
	}
	if p := g.agent().Status.Phase; p == testPhaseUnschedulable {
		t.Error("the agent must leave Unschedulable once a model is feasible")
	}
}

// The picks read back from a built Deployment equal the picks it was built with, so holding
// them reproduces the same pod template.
func TestHeldPicksFromDeploymentRoundTrip(t *testing.T) {
	r := newBuildOnlyReconciler(t)
	mk := func(id, server string, prefer int32) *modelPick {
		return &modelPick{ModelID: id, Endpoint: "http://" + server + ":11434", PreferFaster: prefer,
			Server:     &kubemootv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: server}},
			Candidates: []modelCandidate{{Model: id, Score: 7}, {Model: testMismatchName, Score: 3}}}
	}
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: rolloutAgent, Namespace: testNS1}}
	mulling, triage := mk("big", "s1", 5), mk("small", "s2", 0)
	built := r.buildDeployment(context.Background(), agent, mulling, triage, "p", "h")
	gotM, gotT := picksFromDeployment(built)
	rebuilt := r.buildDeployment(context.Background(), agent, gotM, gotT, "p", "h")
	if computeDeploymentHash(built) != computeDeploymentHash(rebuilt) || !reflect.DeepEqual(built.Labels, rebuilt.Labels) {
		t.Errorf("held picks must rebuild the same Deployment")
	}
}

// A Deployment that never carried a model gives no held pick.
func TestHeldPicksFromDeploymentWithoutModel(t *testing.T) {
	r := newBuildOnlyReconciler(t)
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: rolloutAgent, Namespace: testNS1}}
	m, tr := picksFromDeployment(r.buildDeployment(context.Background(), agent, nil, nil, "p", "h"))
	if m != nil || tr != nil {
		t.Errorf("want no picks, got %+v %+v", m, tr)
	}
}

// A sidecar-mode contract round-trips through the held picks, and a missing or garbled one
// gives none.
func TestHeldPicksFromContract(t *testing.T) {
	r := newBuildOnlyReconciler(t)
	agent := sidecarAgent()
	mulling := harnessPick("big", "s1")
	data, err := r.buildContract(context.Background(), agent, mulling, mulling).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	gotM, gotT := picksFromContract(string(data))
	again, err := r.buildContract(context.Background(), agent, gotM, gotT).Marshal()
	if err != nil || string(again) != string(data) {
		t.Errorf("held picks must rebuild the same contract (err %v)", err)
	}
	if m, tr := picksFromContract("not: [a contract"); m != nil || tr != nil {
		t.Error("a garbled contract holds no picks")
	}
	if m, _ := picksFromContract(""); m != nil {
		t.Error("an empty contract holds no picks")
	}
}

// A sidecar-mode Agent that never had a model has no contract to write, so it waits for its
// first model: no Deployment, status Unschedulable.
func TestSidecarAgentWithNoModelYetWaits(t *testing.T) {
	g := newRolloutRig(t, false)
	g.editAgent(func(a *kubemootv1alpha1.Agent) {
		a.Spec.Harness = sidecarAgent().Spec.Harness
		a.Spec.Deployment = nil
	})
	g.reconcile()
	if err := g.cli.Get(context.Background(), g.req.NamespacedName, &appsv1.Deployment{}); err == nil {
		t.Error("a sidecar agent with no model has nothing to build yet")
	}
	if p := g.agent().Status.Phase; p != testPhaseUnschedulable {
		t.Errorf("phase = %q", p)
	}
}

func newBuildOnlyReconciler(t *testing.T) *AgentReconciler {
	t.Helper()
	scheme := gatewayScheme(t)
	return &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme, ConfigCache: NewConfigCache()}
}

// The model env of a Deployment is read from the operator's entries, not from a user's
// spec.deployment.env entry of the same name, and unreadable candidates hold nothing.
func TestHeldPicksIgnoreUserEnvAndBadCandidates(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Env: []corev1.EnvVar{
			{Name: envModelModel, Value: "real"}, {Name: envModelEndpoint, Value: "http://real"},
			{Name: envModelModel, Value: "user-override"},
		}}}}}}}
	m, tr := picksFromDeployment(d)
	if m == nil || m.ModelID != "real" || tr != m {
		t.Errorf("want the operator's value and the triage fallback, got %+v %+v", m, tr)
	}
	if heldPick("m", "http://e", "", "{bad", "") != nil {
		t.Error("unreadable candidates must hold nothing")
	}
	only := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Env: []corev1.EnvVar{{Name: envTriageModel, Value: "t"}}}}}}}}
	if m, tr := picksFromDeployment(only); m != nil || tr != nil {
		t.Error("a triage pick without a mulling pick holds nothing")
	}
}

// The model-free part of the base env keeps its order, so existing agents do not roll.
func TestBaseAgentEnvVarsOrder(t *testing.T) {
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: rolloutAgent}}
	pick := &modelPick{ModelID: "m", Endpoint: "http://e"}
	env := baseAgentEnvVars(agent, "", pick, pick, 8080)
	names := make([]string, 0, len(env))
	for _, e := range env {
		names = append(names, e.Name)
	}
	want := []string{envAgentName, "KUBEMOOT_AGENT_TYPE", "KUBEMOOT_AGENT_DESCRIPTION", "KUBEMOOT_CREW",
		envNamespaceName, envPromptFile, envModelModel, envModelEndpoint, envTriageModel,
		envTriageEndpoint, "QUARKUS_HTTP_PORT", envOllamaBaseURL, "KUBEMOOT_NATS_URL"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("env order = %v", names)
	}
}
