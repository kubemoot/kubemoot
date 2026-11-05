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
	"sort"
	"strings"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	modProtocol = "protocol"
	modASystem  = "a-system"
	modStyle    = "style"
)

func withRefs(refs ...string) func(*kubemootv1alpha1.Agent) {
	return func(a *kubemootv1alpha1.Agent) { a.Spec.PromptRefs = refs }
}

func mkModule(name, content string, order int32) *kubemootv1alpha1.PromptModule {
	return &kubemootv1alpha1.PromptModule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testCrewNamespace},
		Spec:       kubemootv1alpha1.PromptModuleSpec{Content: content, Order: order},
	}
}

func TestOwnModuleRefs(t *testing.T) {
	mk := func(refs ...string) kubemootv1alpha1.Agent {
		return *mkResumeAgent(testCrewNamespace, "a", testCrewName, testRoleTooler, withRefs(refs...))
	}
	cases := []struct {
		name  string
		refs  []string
		other [][]string
		want  []string
	}{
		{"no modules", nil, [][]string{{"shared"}}, nil},
		{"all modules shared", []string{modProtocol, modStyle}, [][]string{{modProtocol, modStyle}, {modProtocol, modStyle}}, nil},
		{"one own module", []string{modProtocol, modASystem}, [][]string{{modProtocol, "b-system"}}, []string{modASystem}},
		{"several own modules", []string{modASystem, modProtocol, "a-extra"}, [][]string{{modProtocol}}, []string{modASystem, "a-extra"}},
		{"module shared with one other agent is not own", []string{"pair", modASystem}, [][]string{{"pair"}}, []string{modASystem}},
		{"duplicate ref counts once", []string{modASystem, modASystem}, nil, []string{modASystem}},
		{"sole agent owns everything", []string{modProtocol, modASystem}, nil, []string{modProtocol, modASystem}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agent := mk(c.refs...)
			agents := []kubemootv1alpha1.Agent{agent}
			for _, refs := range c.other {
				agents = append(agents, mk(refs...))
			}
			got := ownModuleRefs(&agent, countModuleUse(agents))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("own modules = %v, want %v", got, c.want)
			}
		})
	}
}

// TestCompileCrewResumesCarriesOwnModulesOnly: the resume prompt holds the text of
// the modules only this agent references, in module order, and none of the shared
// ones; the coordinator's references count as sharing.
func TestCompileCrewResumesCarriesOwnModulesOnly(t *testing.T) {
	coord := mkResumeAgent(testCrewNamespace, testHomelabCoordinator, testCrewName, testRoleCoordinator, withRefs(modProtocol, "coordinator-logic"))
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(
		coord,
		mkResumeAgent(testCrewNamespace, testK8sNodes, testCrewName, testRoleTooler, withRefs(modProtocol, "nodes-late", "nodes-early")),
		mkResumeAgent(testCrewNamespace, "gpu-now", testCrewName, testRoleTooler, withRefs(modProtocol, "gpu-system", "coordinator-logic")),
		mkModule(modProtocol, "SHARED PROTOCOL", 10),
		mkModule("coordinator-logic", "COORDINATOR LOGIC", 20),
		mkModule("nodes-late", "NODES LATE", 40),
		mkModule("nodes-early", "NODES EARLY", 30),
		mkModule("gpu-system", "GPU SYSTEM", 30),
	).Build()
	r := &AgentReconciler{Client: cli}

	resumes := r.compileCrewResumes(context.Background(), coord, testCrewName)
	if len(resumes) != 2 {
		t.Fatalf("got %d resumes, want 2", len(resumes))
	}
	if got := resumes[0].Prompt; got != "GPU SYSTEM" {
		t.Errorf("gpu-now prompt = %q, want only its own module (coordinator-logic is shared with the coordinator)", got)
	}
	if got := resumes[1].Prompt; got != "NODES EARLY\n\nNODES LATE" {
		t.Errorf("k8s-nodes prompt = %q, want both own modules in order", got)
	}
	for _, res := range resumes {
		if strings.Contains(res.Prompt, "SHARED") || strings.Contains(res.Prompt, "COORDINATOR") {
			t.Errorf("%s prompt holds a shared module: %q", res.Name, res.Prompt)
		}
	}
}

// TestBuildAgentResumeWithoutOwnModuleHasNoPrompt: an agent whose modules are all
// shared embeds its declarative fields only.
func TestBuildAgentResumeWithoutOwnModuleHasNoPrompt(t *testing.T) {
	a := mkResumeAgent(testCrewNamespace, "a", testCrewName, testRoleTooler, withRefs(modProtocol))
	b := mkResumeAgent(testCrewNamespace, "b", testCrewName, testRoleTooler, withRefs(modProtocol))
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(a, b, mkModule(modProtocol, "SHARED", 10)).Build()
	rec := &AgentReconciler{Client: cli}
	use := countModuleUse([]kubemootv1alpha1.Agent{*a, *b})
	if got := rec.buildAgentResume(context.Background(), a, use); got.Prompt != "" || got.Description != "a desc" {
		t.Errorf("resume = %+v, want declarative fields and an empty prompt", got)
	}
}

// TestResumeHashFollowsOwnModuleText: editing an own module changes the resume
// hash (so the crew re-embeds); editing a shared module does not.
func TestResumeHashFollowsOwnModuleText(t *testing.T) {
	coord := mkResumeAgent(testCrewNamespace, testHomelabCoordinator, testCrewName, testRoleCoordinator, nil)
	build := func(own, shared string) string {
		cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(
			coord,
			mkResumeAgent(testCrewNamespace, "a", testCrewName, testRoleTooler, withRefs(modProtocol, modASystem)),
			mkResumeAgent(testCrewNamespace, "b", testCrewName, testRoleTooler, withRefs(modProtocol, "b-system")),
			mkModule(modProtocol, shared, 10), mkModule(modASystem, own, 30), mkModule("b-system", "B", 30),
		).Build()
		r := &AgentReconciler{Client: cli}
		return HashResumes(r.compileCrewResumes(context.Background(), coord, testCrewName))
	}
	base := build("own v1", "shared v1")
	if build("own v2", "shared v1") == base {
		t.Error("an own-module edit must change the resume hash")
	}
	if build("own v1", "shared v2") != base {
		t.Error("a shared-module edit must not change the resume hash")
	}
}

func sortedRequestNames(reqs []reconcile.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

// TestPromptModuleChangeEnqueuesCoordinator: a module change reaches the
// coordinator of the crews whose agents use it, and only those crews. A
// coordinator without a crew label compiles the whole namespace, so it is
// reached from any crew.
func TestPromptModuleChangeEnqueuesCoordinator(t *testing.T) {
	coordA := mkResumeAgent(testCrewNamespace, "coord-a", "crew-a", testRoleCoordinator, nil)
	coordB := mkResumeAgent(testCrewNamespace, "coord-b", "crew-b", testRoleCoordinator, nil)
	coordAll := mkResumeAgent(testCrewNamespace, "coord-all", "", testRoleCoordinator, nil)
	coordAll.Labels = nil
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(
		coordA, coordB, coordAll,
		mkResumeAgent(testCrewNamespace, "a1", "crew-a", testRoleTooler, withRefs(modASystem)),
		mkResumeAgent(testCrewNamespace, "b1", "crew-b", testRoleTooler, withRefs("b-system")),
	).Build()
	ctx := context.Background()

	got := sortedRequestNames(mapPromptModuleToAgentRequests(ctx, cli, mkModule(modASystem, "x", 1)))
	want := []string{"a1", "coord-a", "coord-all"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a-system change enqueued %v, want %v", got, want)
	}
	if got := sortedRequestNames(mapPromptModuleToAgentRequests(ctx, cli, mkModule("unused", "x", 1))); len(got) != 0 {
		t.Errorf("a module no agent uses enqueued %v, want nothing", got)
	}
}

// TestPromptModuleChangeDoesNotDoubleQueueCoordinator: a coordinator that
// references the changed module is queued once.
func TestPromptModuleChangeDoesNotDoubleQueueCoordinator(t *testing.T) {
	coord := mkResumeAgent(testCrewNamespace, "coord", "crew-a", testRoleCoordinator, withRefs("shared"))
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(
		coord, mkResumeAgent(testCrewNamespace, "a1", "crew-a", testRoleTooler, withRefs("shared")),
	).Build()
	got := sortedRequestNames(mapPromptModuleToAgentRequests(context.Background(), cli, mkModule("shared", "x", 1)))
	if want := []string{"a1", "coord"}; !reflect.DeepEqual(got, want) {
		t.Errorf("enqueued %v, want %v", got, want)
	}
}
