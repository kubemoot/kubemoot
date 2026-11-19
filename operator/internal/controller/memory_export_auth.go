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
	"net/http"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"

	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// Errors a memoryAuthorizer returns; the handler maps each to a status.
var (
	errUnauthenticated = errors.New("a valid bearer token is required")
	errForbidden       = errors.New("the caller may not read this crew")
)

// memoryAuthorizer decides whether the caller of a memory export may read one crew.
type memoryAuthorizer interface {
	Authorize(ctx context.Context, bearerToken string, scope crewscope.Scope) error
}

// reviewAuthorizer authenticates the bearer token with a TokenReview and asks the API
// server, with a SubjectAccessReview, whether that identity may get the Crew. The
// report service is plain HTTP reachable by any pod, so the token proves who is asking
// and RBAC (the same rules that govern reading the Crew) decides.
type reviewAuthorizer struct {
	client kubernetes.Interface
}

// NewReviewAuthorizer returns the TokenReview and SubjectAccessReview authorizer.
func NewReviewAuthorizer(c kubernetes.Interface) memoryAuthorizer {
	return reviewAuthorizer{client: c}
}

func (a reviewAuthorizer) Authorize(ctx context.Context, token string, scope crewscope.Scope) error {
	if token == "" {
		return errUnauthenticated
	}
	tr, err := a.client.AuthenticationV1().TokenReviews().Create(ctx,
		&authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("token review: %w", err)
	}
	if !tr.Status.Authenticated {
		return errUnauthenticated
	}
	sar, err := a.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, accessReview(tr.Status.User, scope), metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("subject access review: %w", err)
	}
	if !sar.Status.Allowed {
		return errForbidden
	}
	return nil
}

// accessReview asks whether user may get the Crew the export is of.
func accessReview(user authenticationv1.UserInfo, scope crewscope.Scope) *authorizationv1.SubjectAccessReview {
	extra := map[string]authorizationv1.ExtraValue{}
	for k, v := range user.Extra {
		extra[k] = authorizationv1.ExtraValue(v)
	}
	return &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: user.Username, UID: user.UID, Groups: user.Groups, Extra: extra,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: scope.Namespace, Verb: verbGet, Group: kubemootv1alpha1.GroupVersion.Group, Resource: "crews", Name: scope.Crew,
		},
	}}
}

// bearerToken is the token of an "Authorization: Bearer <token>" header, or "".
func bearerToken(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
