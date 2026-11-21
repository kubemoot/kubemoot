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
	"net/http"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"

	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// Errors a reportAuthorizer returns; the middleware maps each to a status.
var (
	errUnauthenticated = errors.New("a valid bearer token is required")
	errForbidden       = errors.New("the caller may not read this resource")
)

// reportAuthorizer decides whether the caller of a report service endpoint may read
// what the endpoint returns, described as the Kubernetes resource access it equals.
type reportAuthorizer interface {
	// Authorize returns the caller's verified identity when it may make the access.
	Authorize(ctx context.Context, bearerToken string, attrs authorizationv1.ResourceAttributes) (*identity.Identity, error)
	// Allowed asks whether an identity already verified by Authorize may make another access.
	Allowed(ctx context.Context, id *identity.Identity, attrs authorizationv1.ResourceAttributes) (bool, error)
}

// reviewAuthorizer authenticates the bearer token with a TokenReview and asks the API
// server, with a SubjectAccessReview, whether that identity may make the access an
// endpoint stands for. The report service is plain HTTP reachable by any pod, so the
// token proves who is asking and RBAC (the same rules that govern reading the
// underlying resource) decides.
type reviewAuthorizer struct {
	client kubernetes.Interface
}

// NewReviewAuthorizer returns the TokenReview and SubjectAccessReview authorizer.
func NewReviewAuthorizer(c kubernetes.Interface) reportAuthorizer {
	return reviewAuthorizer{client: c}
}

func (a reviewAuthorizer) Authorize(ctx context.Context, token string, attrs authorizationv1.ResourceAttributes) (*identity.Identity, error) {
	rv := identity.Reviewer{Client: a.client}
	id, err := rv.Review(ctx, token)
	switch {
	case errors.Is(err, identity.ErrNoCredential), errors.Is(err, identity.ErrInvalid):
		return nil, errUnauthenticated
	case err != nil:
		return nil, err
	}
	allowed, err := rv.Allowed(ctx, id, attrs)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errForbidden
	}
	return id, nil
}

// Allowed asks the API server whether id may make the access.
func (a reviewAuthorizer) Allowed(ctx context.Context, id *identity.Identity, attrs authorizationv1.ResourceAttributes) (bool, error) {
	return identity.Reviewer{Client: a.client}.Allowed(ctx, id, attrs)
}

// What each endpoint of the report service returns, as the access it stands for.

// crewAccess is the access a crew's memory export equals: get that Crew.
func crewAccess(scope crewscope.Scope) authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{
		Namespace: scope.Namespace, Verb: verbGet, Group: kubemootv1alpha1.GroupVersion.Group, Resource: "crews", Name: scope.Crew,
	}
}

// suiteAccess is the access a fitness report equals: get that CrewFitnessSuite. The
// report path names the suite, so the report is as readable as the suite it is built from.
func suiteAccess(namespace, name string) authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{
		Namespace: namespace, Verb: verbGet, Group: kubemootv1alpha1.GroupVersion.Group, Resource: "crewfitnesssuites", Name: name,
	}
}

// componentStatusAccess is the access the component status equals: list ModelServers in
// every namespace. The status reports the operator, NATS and how many ModelServers are
// ready, and the ModelServer count is the one part of it that comes from cluster objects,
// so a caller who may list them already sees all the status holds. No narrower
// namespaced or named access fits, because the status counts across namespaces.
func componentStatusAccess() authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{Verb: "list", Group: kubemootv1alpha1.GroupVersion.Group, Resource: "modelservers"}
}

// accessFor names the access an endpoint's request equals, or an error for a request
// that names nothing valid (answered 400, before any token is checked).
type accessFor func(r *http.Request) (authorizationv1.ResourceAttributes, error)

// requireAccess is the one gate every report service endpoint but /healthz goes through.
// It answers the request itself, and does not call next, unless the caller presents a
// token whose identity may make the access the endpoint equals: no authorizer configured
// (fail closed, 503), no valid token (401), an identity without access (403), or a review
// that failed (503).
func (s *ReportServer) requireAccess(access accessFor, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		attrs, err := access(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s.Auth == nil {
			http.Error(w, "the report service has no authorizer configured", http.StatusServiceUnavailable)
			return
		}
		id, err := s.Auth.Authorize(r.Context(), identity.BearerToken(r), attrs)
		switch {
		case err == nil:
			next(w, r.WithContext(withReportCaller(r.Context(), reportCaller{
				identity: id, reason: strings.TrimSpace(r.Header.Get(identity.HeaderAccessReason)),
			})))
		case errors.Is(err, errUnauthenticated):
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, err.Error()+": send the caller's service account token as 'Authorization: Bearer <token>'", http.StatusUnauthorized)
		case errors.Is(err, errForbidden):
			http.Error(w, err.Error(), http.StatusForbidden)
		default:
			reportLog.Error(err, "report access check failed", "path", r.URL.Path)
			http.Error(w, "could not check the caller's access", http.StatusServiceUnavailable)
		}
	}
}
