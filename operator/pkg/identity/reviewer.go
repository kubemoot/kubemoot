/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Authorizer asks whether an identity may make an access, described as the Kubernetes
// resource access it equals, so plain RBAC decides.
type Authorizer interface {
	Allowed(ctx context.Context, id *Identity, attrs authorizationv1.ResourceAttributes) (bool, error)
}

// Reviewer checks Kubernetes tokens with a TokenReview and accesses with a
// SubjectAccessReview. The operator's report endpoints and the doors of a crew use it, so
// "the caller's token proves who is asking and RBAC decides" is one implementation.
type Reviewer struct {
	Client kubernetes.Interface
	// Audiences, when set, are the audiences a token must be bound to: a token that is
	// valid for the API server but was minted for another audience is not accepted, so a
	// door never takes a token that could be replayed against the API server.
	Audiences []string
}

// Review authenticates a bearer token. A token the API server does not accept is ErrInvalid.
func (r Reviewer) Review(ctx context.Context, token string) (*Identity, error) {
	if token == "" {
		return nil, ErrNoCredential
	}
	tr, err := r.Client.AuthenticationV1().TokenReviews().Create(ctx,
		&authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: r.Audiences}},
		metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("token review: %w", err)
	}
	if !tr.Status.Authenticated || !audienceAccepted(r.Audiences, tr.Status.Audiences) {
		return nil, ErrInvalid
	}
	u := tr.Status.User
	extra := make(map[string][]string, len(u.Extra))
	for k, v := range u.Extra {
		extra[k] = v
	}
	return &Identity{Issuer: IssuerKubernetes, Subject: u.Username, UID: u.UID, Groups: u.Groups, Extra: extra}, nil
}

// Allowed asks the API server whether id may make the access attrs describes.
func (r Reviewer) Allowed(ctx context.Context, id *Identity, attrs authorizationv1.ResourceAttributes) (bool, error) {
	extra := make(map[string]authorizationv1.ExtraValue, len(id.Extra))
	for k, v := range id.Extra {
		extra[k] = authorizationv1.ExtraValue(v)
	}
	sar, err := r.Client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: id.Subject, UID: id.UID, Groups: id.Groups, Extra: extra, ResourceAttributes: &attrs,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("subject access review: %w", err)
	}
	return sar.Status.Allowed, nil
}

// TokenReviewVerifier is the verifier of Kubernetes bearer tokens.
type TokenReviewVerifier struct {
	Reviewer Reviewer
}

// Verify reviews the request's bearer token.
func (v TokenReviewVerifier) Verify(r *http.Request) (*Identity, error) {
	return v.Reviewer.Review(r.Context(), BearerToken(r))
}

const verbGet = "get"

// UseAccess is the access of using a crew at all: get that Crew, as the report endpoints
// require to read its memory.
func UseAccess(namespace, crew string) authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{
		Namespace: namespace, Verb: verbGet, Group: kubemootv1alpha1.GroupVersion.Group, Resource: "crews", Name: crew,
	}
}

// audienceAccepted reports whether the token is bound to one of the wanted audiences. With
// none wanted, any is accepted. The API server already checks this; the door checks the
// answer again so a server that ignores spec.audiences cannot widen what it accepts.
func audienceAccepted(wanted, granted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, w := range wanted {
		if slices.Contains(granted, w) {
			return true
		}
	}
	return false
}

// ConversationsSubresource is the crews subresource whose get and list verbs let an
// operator read other callers' conversations.
const ConversationsSubresource = "conversations"

// ConversationsAccess is the access of reading other callers' conversations of a crew: get
// to read one, list to list them all, on crews/conversations. The resource is virtual: RBAC
// rules may name a subresource the API server does not serve, so no aggregated API is needed.
// Nobody holds it unless a binding to the ClusterRole kubemoot-conversation-reader, or a
// rule of the installer's own, grants it.
func ConversationsAccess(namespace, crew, verb string) authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{
		Namespace: namespace, Verb: verb, Group: kubemootv1alpha1.GroupVersion.Group,
		Resource: "crews", Subresource: ConversationsSubresource, Name: crew,
	}
}
