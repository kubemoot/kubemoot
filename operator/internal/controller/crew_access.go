/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	// conditionTypeAuthentication says whether the crew's gateway verifies who is asking.
	// It is informational and never affects Ready.
	conditionTypeAuthentication = "Authentication"
	reasonAnonymous             = "Anonymous"
	reasonTokenReview           = "TokenReview"
	reasonClosed                = "Closed"
	reasonServiceToken          = "ServiceToken"
	reasonTrustedHop            = "TrustedHop"

	verbCreate = "create"
)

// discussionAccess is the crew's access settings, nil when it declares none.
func discussionAccess(crew *kubemootv1alpha1.Crew) *kubemootv1alpha1.DiscussionAccess {
	if crew.Spec.Discussion == nil {
		return nil
	}
	return crew.Spec.Discussion.Access
}

// gatewayAccessEnv is the access part of a gateway's environment: nothing for a crew that
// declares none, so its gateway runs exactly as it did before identity existed.
func gatewayAccessEnv(access *kubemootv1alpha1.DiscussionAccess) []corev1.EnvVar {
	if access == nil {
		return nil
	}
	env := []corev1.EnvVar{
		{Name: identity.EnvEnforce, Value: identity.EnvTrue},
		{Name: identity.EnvTokenReview, Value: strconv.FormatBool(access.TokenReviewEnabled())},
		{Name: identity.EnvAllowAnonymous, Value: strconv.FormatBool(access.AnonymousAllowed())},
	}
	if len(access.TokenAudiences) > 0 {
		env = append(env, corev1.EnvVar{Name: identity.EnvTokenAudiences, Value: strings.Join(access.TokenAudiences, ",")})
	}
	return append(env, gatewayServiceTokenEnv(access)...)
}

// exposesCaller reports whether the crew lets its agents be told who is asking. It needs
// access: a gateway that verifies no one has no caller to describe.
func exposesCaller(crew *kubemootv1alpha1.Crew) bool {
	if crew == nil {
		return false
	}
	d := crew.Spec.Discussion
	return d != nil && d.Access != nil && d.Identity != nil && d.Identity.ExposeToAgents
}

// exposureEnv is the variable that turns on one side of telling agents who is asking: nothing
// unless the crew exposes the caller, so by default no prompt changes and no person's name is on
// the bus.
func exposureEnv(name string, crew *kubemootv1alpha1.Crew) []corev1.EnvVar {
	if !exposesCaller(crew) {
		return nil
	}
	return []corev1.EnvVar{{Name: name, Value: identity.EnvTrue}}
}

// gatewayIdentityEnv makes the gateway stamp a label on the principal of the questions it queues.
func gatewayIdentityEnv(crew *kubemootv1alpha1.Crew) []corev1.EnvVar {
	return exposureEnv(identity.EnvExposeLabel, crew)
}

// agentIdentityEnv makes an agent put the caller in its turn context.
func agentIdentityEnv(crew *kubemootv1alpha1.Crew) []corev1.EnvVar {
	return exposureEnv(identity.EnvExposeCaller, crew)
}

// gatewayAccessRules are the rights a gateway needs to verify callers: create TokenReviews
// and SubjectAccessReviews. A crew that declares no access gets none.
func gatewayAccessRules(access *kubemootv1alpha1.DiscussionAccess) []rbacv1.PolicyRule {
	if access == nil {
		return nil
	}
	if !access.TokenReviewEnabled() {
		return append(leaseAccessRules(access), hopAccessRules(access)...)
	}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"tokenreviews"}, Verbs: []string{verbCreate}},
		{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"subjectaccessreviews"}, Verbs: []string{verbCreate}},
	}
}

// hopAccessRules are the rights of a gateway that verifies no Kubernetes token but accepts a
// trusted hop's assertion under the kubernetesRBAC rule: it asks Kubernetes whether the
// asserted user may use the crew. No other rule calls the Kubernetes API for the asker.
func hopAccessRules(access *kubemootv1alpha1.DiscussionAccess) []rbacv1.PolicyRule {
	if len(access.TrustedHops) == 0 || access.Askers == nil || access.Askers.KubernetesRBAC == nil {
		return nil
	}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"subjectaccessreviews"}, Verbs: []string{verbCreate}},
	}
}

// leaseAccessRules are the rights of a gateway that verifies no Kubernetes token but keeps
// people's tokens: it checks the tool gateway's ServiceAccount token with a TokenReview.
func leaseAccessRules(access *kubemootv1alpha1.DiscussionAccess) []rbacv1.PolicyRule {
	if !passesTokens(access) {
		return nil
	}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"tokenreviews"}, Verbs: []string{verbCreate}},
	}
}

// serviceTokenAuthentication is the Authentication condition of a crew that accepts a
// service's token for the person asking, and false when it declares none.
func serviceTokenAuthentication(crew *kubemootv1alpha1.Crew, access *kubemootv1alpha1.DiscussionAccess) bool {
	if access == nil || len(access.ServiceTokens) == 0 {
		return false
	}
	names := make([]string, 0, len(access.ServiceTokens))
	for _, v := range access.ServiceTokens {
		names = append(names, v.Name)
	}
	msg := "A caller's own token from " + strings.Join(names, ", ") + " is verified by use, and conversations belong to that person."
	if access.TokenReviewEnabled() {
		msg += " Kubernetes tokens are verified too."
	}
	if access.AnonymousAllowed() {
		msg += " Callers with no token are also accepted, as anonymous."
	}
	setCrewCondition(crew, conditionTypeAuthentication, metav1.ConditionTrue, reasonServiceToken, msg)
	return true
}

// reconcileAuthentication records the Authentication condition: False with reason
// Anonymous while the gateway accepts callers it cannot name, so that state is visible,
// True when it verifies callers' tokens, and False with reason Closed when it accepts no
// caller at all. It follows what the gateway does, not only what the spec says.
func reconcileAuthentication(crew *kubemootv1alpha1.Crew, hosts identity.PolicyHosts) {
	if crew.Spec.Discussion != nil && !crew.Spec.Discussion.IsEnabled() {
		meta.RemoveStatusCondition(&crew.Status.Conditions, conditionTypeAuthentication)
		return
	}
	access := discussionAccess(crew)
	if hopsAuthentication(crew, access, hosts) {
		return
	}
	switch {
	case access == nil || (!access.TokenReviewEnabled() && access.AnonymousAllowed()):
		setCrewCondition(crew, conditionTypeAuthentication, metav1.ConditionFalse, reasonAnonymous,
			"The discussion gateway accepts anonymous callers and verifies no one. "+
				"Set spec.discussion.access with tokenReview to verify callers.")
	case !access.TokenReviewEnabled():
		setCrewCondition(crew, conditionTypeAuthentication, metav1.ConditionFalse, reasonClosed,
			"The discussion gateway verifies no token and accepts no anonymous caller, so it refuses every request.")
	case access.AnonymousAllowed():
		setCrewCondition(crew, conditionTypeAuthentication, metav1.ConditionTrue, reasonTokenReview,
			"Kubernetes tokens are verified, each caller needs get on the Crew, and conversations belong to "+
				"their caller; callers with no token are also accepted, as anonymous.")
	default:
		setCrewCondition(crew, conditionTypeAuthentication, metav1.ConditionTrue, reasonTokenReview,
			"Every caller presents a Kubernetes token, each needs get on the Crew, and conversations belong to their caller.")
	}
}
