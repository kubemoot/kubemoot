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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	identityCrew     = "pilot"
	identityAudience = "gw-a"
)

func identityCrewWith(access *kubemootv1alpha1.DiscussionAccess) *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: identityCrew, Namespace: rbacTestNS, UID: "identity-crew-uid"},
		Spec:       kubemootv1alpha1.CrewSpec{Discussion: &kubemootv1alpha1.DiscussionConfig{Access: access}},
	}
}

func identityCF() *kubemootv1alpha1.CrewFitness {
	return &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: rbacTestNS},
		Spec:       kubemootv1alpha1.CrewFitnessSpec{CrewRef: identityCrew},
	}
}

func TestIdentityForCrewsThatVerifyTokens(t *testing.T) {
	if identityFor(identityCrewWith(nil)) != nil {
		t.Error("a crew with no access verifies no one, the runner needs no identity")
	}
	if identityFor(identityCrewWith(&kubemootv1alpha1.DiscussionAccess{TokenReview: ptr.To(false)})) != nil {
		t.Error("a crew that checks no Kubernetes token cannot use one")
	}
	closed := identityFor(identityCrewWith(&kubemootv1alpha1.DiscussionAccess{AllowAnonymous: ptr.To(false)}))
	if closed == nil || closed.Audience != "" {
		t.Errorf("a crew that requires tokens with no audiences wants the API server's own, got %+v", closed)
	}
	aud := identityFor(identityCrewWith(&kubemootv1alpha1.DiscussionAccess{TokenAudiences: []string{identityAudience, "gw-b"}}))
	if aud == nil || aud.Audience != identityAudience {
		t.Errorf("audience = %+v, want the first the gateway accepts", aud)
	}
}

func TestWithGatewayIdentityProjectsTheToken(t *testing.T) {
	job := (&CrewFitnessReconciler{ConfigCache: NewConfigCache()}).buildJob(identityCF(), "j", "http://gw", "k", "cm")
	spec := &job.Spec.Template.Spec
	volumes := len(spec.Volumes)

	withGatewayIdentity(spec, &gatewayIdentity{Audience: identityAudience})

	if len(spec.Volumes) != volumes+1 {
		t.Fatalf("volumes = %d, want one more than %d", len(spec.Volumes), volumes)
	}
	wantTokenProjection(t, spec.Volumes[volumes], identityAudience)
	c := containerNamed(t, *spec, componentFitnessRunner)
	wantTokenMounted(t, c)
	if got, _ := envValue(c.Env, envGatewayTokenFile); got != gatewayTokenDir+"/"+credentialBrokerTokenFile {
		t.Errorf("%s = %q", envGatewayTokenFile, got)
	}
	if spec.ServiceAccountName != componentFitnessRunner {
		t.Errorf("service account = %q", spec.ServiceAccountName)
	}
}

func wantTokenProjection(t *testing.T, v corev1.Volume, audience string) {
	t.Helper()
	if v.Projected == nil || len(v.Projected.Sources) != 1 || v.Projected.Sources[0].ServiceAccountToken == nil {
		t.Fatalf("not a projected ServiceAccount token: %+v", v)
	}
	tok := v.Projected.Sources[0].ServiceAccountToken
	if tok.Audience != audience || tok.Path != credentialBrokerTokenFile || tok.ExpirationSeconds == nil || *tok.ExpirationSeconds < 600 {
		t.Errorf("token projection = %+v", tok)
	}
}

func wantTokenMounted(t *testing.T, c corev1.Container) {
	t.Helper()
	for _, m := range c.VolumeMounts {
		if m.Name == gatewayTokenVolume && m.MountPath == gatewayTokenDir && m.ReadOnly {
			return
		}
	}
	t.Errorf("token not mounted read-only: %+v", c.VolumeMounts)
}

func TestWithGatewayIdentityNilLeavesThePodAlone(t *testing.T) {
	job := (&CrewFitnessReconciler{ConfigCache: NewConfigCache()}).buildJob(identityCF(), "j", "http://gw", "k", "cm")
	before := job.DeepCopy()
	withGatewayIdentity(&job.Spec.Template.Spec, nil)
	if len(job.Spec.Template.Spec.Volumes) != len(before.Spec.Template.Spec.Volumes) {
		t.Error("a crew that verifies no token must not change the pod")
	}
}

func TestEnsureRunnerCrewAccessGrantsGetOnThatCrewOnly(t *testing.T) {
	crew := identityCrewWith(&kubemootv1alpha1.DiscussionAccess{})
	c := rbacTestClient(t, crew)
	for i := 0; i < 2; i++ {
		if err := ensureRunnerCrewAccess(context.Background(), c, crew); err != nil {
			t.Fatal(err)
		}
	}

	role := &rbacv1.Role{}
	getRBAC(t, c, crewRunnerRoleName(identityCrew), role)
	wantGetOnThatCrew(t, role)

	binding := &rbacv1.RoleBinding{}
	getRBAC(t, c, crewRunnerRoleName(identityCrew), binding)
	if len(binding.Subjects) != 1 || binding.Subjects[0].Name != componentFitnessRunner || binding.Subjects[0].Namespace != rbacTestNS ||
		binding.RoleRef.Name != role.Name || binding.RoleRef.Kind != "Role" {
		t.Errorf("binding = %+v", binding)
	}
}

func wantGetOnThatCrew(t *testing.T, role *rbacv1.Role) {
	t.Helper()
	if len(role.Rules) != 1 {
		t.Fatalf("rules = %+v, want exactly one", role.Rules)
	}
	rule := role.Rules[0]
	if !reflect.DeepEqual(rule.APIGroups, []string{kubemootv1alpha1.GroupVersion.Group}) ||
		!reflect.DeepEqual(rule.Resources, []string{crewsResource}) ||
		!reflect.DeepEqual(rule.Verbs, []string{verbGet}) ||
		!reflect.DeepEqual(rule.ResourceNames, []string{identityCrew}) {
		t.Errorf("rule = %+v, want get on crews/%s only", rule, identityCrew)
	}
	if len(role.OwnerReferences) != 1 || role.OwnerReferences[0].Name != identityCrew {
		t.Errorf("the Role should go with its Crew: %+v", role.OwnerReferences)
	}
}

func TestCrewIdentityGrantsOnlyForACrewThatVerifies(t *testing.T) {
	open := identityCrewWith(nil)
	c := rbacTestClient(t, open)
	r := &CrewFitnessReconciler{Client: c}
	id, err := r.crewIdentity(context.Background(), identityCF())
	if err != nil || id != nil {
		t.Fatalf("open crew: id=%v err=%v", id, err)
	}
	if err := c.Get(context.Background(), keyOf(crewRunnerRoleName(identityCrew)), &rbacv1.Role{}); err == nil {
		t.Error("an open crew needs no Role")
	}

	closed := rbacTestClient(t, identityCrewWith(&kubemootv1alpha1.DiscussionAccess{AllowAnonymous: ptr.To(false)}))
	r = &CrewFitnessReconciler{Client: closed}
	id, err = (r).crewIdentity(context.Background(), identityCF())
	if err != nil || id == nil {
		t.Fatalf("closed crew: id=%v err=%v", id, err)
	}
	getRBAC(t, closed, crewRunnerRoleName(identityCrew), &rbacv1.Role{})
}

func TestCrewIdentityIgnoresAMissingCrew(t *testing.T) {
	r := &CrewFitnessReconciler{Client: rbacTestClient(t)}
	if id, err := r.crewIdentity(context.Background(), identityCF()); id != nil || err != nil {
		t.Errorf("id=%v err=%v, a missing crew is reported by the endpoint lookup", id, err)
	}
}

// A run whose crew refused the runner is an Error that says so, not the Job's bare
// "backoff limit" and not a Pass for want of assertions.
func TestCheckJobNamesARefusedRun(t *testing.T) {
	const reason = "the crew refused the fitness runner: it requires identity (401)"
	cf := identityCF()
	cf.Status = kubemootv1alpha1.CrewFitnessStatus{Phase: kubemootv1alpha1.CrewFitnessPhaseRunning, JobRef: "cf-run-1"}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-run-1", Namespace: rbacTestNS, Annotations: map[string]string{fitnessRefusedAnnotation: reason}},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
	scheme := rbacTestClient(t).Scheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cf, job).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitness{}).Build()
	r := &CrewFitnessReconciler{Client: c}

	if _, err := r.checkJob(context.Background(), cf); err != nil {
		t.Fatal(err)
	}
	got := &kubemootv1alpha1.CrewFitness{}
	getRBAC(t, c, "run-1", got)
	if got.Status.Phase != kubemootv1alpha1.CrewFitnessPhaseError || got.Status.Error != reason {
		t.Errorf("phase=%q error=%q, want Error naming the refusal", got.Status.Phase, got.Status.Error)
	}
}

func keyOf(name string) types.NamespacedName {
	return types.NamespacedName{Name: name, Namespace: rbacTestNS}
}

// The runner module holds its own copies of these names; they are a contract between the two.
func TestRunnerContractNamesAreStable(t *testing.T) {
	if fitnessRefusedAnnotation != "kubemoot.ai/fitness-refused" || envGatewayTokenFile != "GATEWAY_TOKEN_FILE" {
		t.Errorf("%q %q: change fitness-runner/main.go and runner.go with them", fitnessRefusedAnnotation, envGatewayTokenFile)
	}
}
