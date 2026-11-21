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
	return env
}

// gatewayAccessRules are the rights a gateway needs to verify callers: create TokenReviews
// and SubjectAccessReviews. A crew that declares no access gets none.
func gatewayAccessRules(access *kubemootv1alpha1.DiscussionAccess) []rbacv1.PolicyRule {
	if access == nil || !access.TokenReviewEnabled() {
		return nil
	}
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"tokenreviews"}, Verbs: []string{"create"}},
		{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"subjectaccessreviews"}, Verbs: []string{"create"}},
	}
}

// reconcileAuthentication records the Authentication condition: False with reason
// Anonymous while the gateway accepts callers it cannot name, so that state is visible,
// True when it verifies callers' tokens, and False with reason Closed when it accepts no
// caller at all. It follows what the gateway does, not only what the spec says.
func reconcileAuthentication(crew *kubemootv1alpha1.Crew) {
	if crew.Spec.Discussion != nil && !crew.Spec.Discussion.IsEnabled() {
		meta.RemoveStatusCondition(&crew.Status.Conditions, conditionTypeAuthentication)
		return
	}
	access := discussionAccess(crew)
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
			"Kubernetes tokens are verified and each caller needs get on the Crew; callers with no token "+
				"are also accepted, as anonymous.")
	default:
		setCrewCondition(crew, conditionTypeAuthentication, metav1.ConditionTrue, reasonTokenReview,
			"Every caller presents a Kubernetes token, and each needs get on the Crew.")
	}
}
