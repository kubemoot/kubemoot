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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	reviewTestPolicy    = "pilot-policy"
	reviewTestOtherCrew = "other-crew"
	reviewTestPanel     = "panel"
	reviewTestAll       = "all"
	reviewTestAnalyst   = "analyst"
)

func reviewTestReconciler(t *testing.T, objs ...runtime.Object) *AgentReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()}
}

func reviewTestArchetype(name string, review *kubemootv1alpha1.ArchetypeReview) *kubemootv1alpha1.MootArchetype {
	return &kubemootv1alpha1.MootArchetype{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kubemootv1alpha1.MootArchetypeSpec{Review: review},
	}
}

func reviewTestPolicyFor(crew, archetype string) *kubemootv1alpha1.CrewSchedulingPolicy {
	return &kubemootv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: reviewTestPolicy + "-" + crew, Namespace: testCrewNamespace},
		Spec:       kubemootv1alpha1.CrewSchedulingPolicySpec{CrewRef: crew, ArchetypeRef: archetype},
	}
}

func reviewTestCoordinator() *kubemootv1alpha1.Agent {
	return &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testRoleCoordinator, Namespace: testCrewNamespace},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: testRoleCoordinator},
	}
}

func TestReviewEnvVars_NoBlockSetsNothing(t *testing.T) {
	if got := reviewEnvVars(nil); len(got) != 0 {
		t.Fatalf("no review block: want no variables, got %v", got)
	}
	if got := reviewEnvVars(&kubemootv1alpha1.ArchetypeReview{}); len(got) != 0 {
		t.Fatalf("empty review block: want no variables, got %v", got)
	}
}

func TestReviewEnvVars_BothFieldsBecomeVariables(t *testing.T) {
	got := reviewEnvVars(&kubemootv1alpha1.ArchetypeReview{
		Convene: kubemootv1alpha1.ReviewConveneDomain, Fallback: kubemootv1alpha1.ReviewFallbackPrimary})
	if len(got) != 2 || got[0].Name != envReviewConvene || got[0].Value != "domain" ||
		got[1].Name != envReviewFallback || got[1].Value != "primary" {
		t.Fatalf("unexpected variables: %v", got)
	}
}

func TestCrewArchetypeName_FirstPolicyThatNamesOneWinsOtherwiseTheDefault(t *testing.T) {
	named := reviewTestPolicyFor(testCrewName, reviewTestPanel)
	unset := reviewTestPolicyFor(testCrewName, "")
	if got := crewArchetypeName([]*kubemootv1alpha1.CrewSchedulingPolicy{unset, named}); got != reviewTestPanel {
		t.Errorf("policies in name order, the first that names one wins: want %q, got %q", reviewTestPanel, got)
	}
	if got := crewArchetypeName([]*kubemootv1alpha1.CrewSchedulingPolicy{unset}); got != defaultArchetypeName {
		t.Errorf("policy with no archetypeRef: want %q, got %q", defaultArchetypeName, got)
	}
	if got := crewArchetypeName(nil); got != defaultArchetypeName {
		t.Errorf("no policies: want %q, got %q", defaultArchetypeName, got)
	}
}

func reviewTestCoordinatorOf(crew string) *kubemootv1alpha1.Agent {
	a := reviewTestCoordinator()
	a.Labels = map[string]string{labelCrew: crew}
	return a
}

func TestCoordinatorReviewEnvVars_FollowsTheCrewsArchetype(t *testing.T) {
	r := reviewTestReconciler(t,
		reviewTestArchetype(defaultArchetypeName, &kubemootv1alpha1.ArchetypeReview{Convene: kubemootv1alpha1.ReviewConveneDomain}),
		reviewTestArchetype(reviewTestPanel, &kubemootv1alpha1.ArchetypeReview{Convene: kubemootv1alpha1.ReviewConveneAll}),
		reviewTestPolicyFor(testCrewName, reviewTestPanel),
	)
	got := r.coordinatorReviewEnvVars(context.Background(), reviewTestCoordinatorOf(testCrewName))
	if len(got) != 1 || got[0].Name != envReviewConvene || got[0].Value != reviewTestAll {
		t.Fatalf("crew on the panel archetype: want convene=all, got %v", got)
	}
	got = r.coordinatorReviewEnvVars(context.Background(), reviewTestCoordinatorOf(reviewTestOtherCrew))
	if len(got) != 1 || got[0].Value != "domain" {
		t.Fatalf("crew without a policy uses the default archetype: want convene=domain, got %v", got)
	}
}

func TestCoordinatorReviewEnvVars_MissingArchetypeSetsNothing(t *testing.T) {
	r := reviewTestReconciler(t)
	if got := r.coordinatorReviewEnvVars(context.Background(), reviewTestCoordinatorOf(testCrewName)); len(got) != 0 {
		t.Fatalf("no archetype installed: want no variables, got %v", got)
	}
}

func TestResumeDomains_ReachTheResumeTheHashAndTheEmbeddedText(t *testing.T) {
	plain := []AgentResume{{Name: testAlpha, Role: reviewTestAnalyst}}
	withDomains := []AgentResume{{Name: testAlpha, Role: reviewTestAnalyst, Domains: []string{"proxmox", "vm"}}}
	if HashResumes(plain) == HashResumes(withDomains) {
		t.Error("declaring domains must change the resume hash so the resume is embedded again")
	}
	if !strings.Contains(BuildResumeText(withDomains[0]), "Domains: proxmox, vm") {
		t.Errorf("resume text lacks the domains: %q", BuildResumeText(withDomains[0]))
	}
	if strings.Contains(BuildResumeText(plain[0]), "Domains") {
		t.Error("an agent without domains adds no Domains line")
	}
}

func TestCoordinatorRequests_OnlyCoordinatorsAreEnqueued(t *testing.T) {
	tooler := kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "k8s-nodes", Namespace: testCrewNamespace}}
	coordinator := *reviewTestCoordinator()
	reqs := coordinatorRequests([]kubemootv1alpha1.Agent{tooler, coordinator})
	if len(reqs) != 1 || reqs[0].Name != testRoleCoordinator || reqs[0].Namespace != testCrewNamespace {
		t.Fatalf("want one request for the coordinator, got %v", reqs)
	}
	if got := coordinatorRequests(nil); len(got) != 0 {
		t.Fatalf("no agents: want no requests, got %v", got)
	}
}

func TestHashResumes_NoDomainsKeepsTheHashACrewHadBefore(t *testing.T) {
	resumes := []AgentResume{
		{Name: testAlpha, Description: testAlphaDescription, Role: testRoleTooler, Keywords: []string{testKeywordK8s}},
		{Name: testBeta, Description: testBetaDescription, Role: reviewTestAnalyst},
	}
	const golden = "866abccd6fb47e6afb00911dd7e5055be25eab2967b94d94890009e2f392b2cd" // computed by the code before domains existed
	if got := HashResumes(resumes); got != golden {
		t.Fatalf("a crew that declares no domains must keep its resume hash: want %s, got %s", golden, got)
	}
}
