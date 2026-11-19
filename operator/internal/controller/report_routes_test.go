/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// guardedPaths are the report service endpoints and the access each equals.
var guardedPaths = map[string]authorizationv1.ResourceAttributes{
	"/componentstatuses":        {Verb: "list", Group: kubemootv1alpha1.GroupVersion.Group, Resource: "modelservers"},
	"/report/tenant/nightly":    {Namespace: "tenant", Verb: verbGet, Group: kubemootv1alpha1.GroupVersion.Group, Resource: "crewfitnesssuites", Name: "nightly"},
	"/memory-export/tenant/pgm": {Namespace: "tenant", Verb: verbGet, Group: kubemootv1alpha1.GroupVersion.Group, Resource: "crews", Name: "pgm"},
}

func routesWithReviews(authenticated, allowed bool) (*ReportServer, *authorizationv1.SubjectAccessReview) {
	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	c, asked := reviewClient(authenticated, allowed)
	return &ReportServer{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Memory: fakeOpener{reader: crewFacts()},
		Auth:   NewReviewAuthorizer(c),
	}, asked
}

func serve(s *ReportServer, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func TestEveryReportEndpointAsksForTheAccessItEquals(t *testing.T) {
	for path, want := range guardedPaths {
		s, asked := routesWithReviews(true, true)
		serve(s, path, headerAuthorization, "Bearer "+sampleToken)
		if asked.Spec.ResourceAttributes == nil || *asked.Spec.ResourceAttributes != want {
			t.Errorf("%s: access review = %+v, want %+v", path, asked.Spec.ResourceAttributes, want)
		}
	}
}

func TestEveryReportEndpointAnswers401WithoutAValidToken(t *testing.T) {
	for path := range guardedPaths {
		s, _ := routesWithReviews(false, true)
		for name, header := range map[string][]string{
			"no header":      nil,
			"a rejected one": {headerAuthorization, "Bearer " + sampleToken},
		} {
			rec := serve(s, path, header...)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s, %s: status = %d, want 401 with WWW-Authenticate", path, name, rec.Code)
			}
		}
	}
}

func TestEveryReportEndpointAnswers403ToAnIdentityWithoutAccess(t *testing.T) {
	for path := range guardedPaths {
		s, _ := routesWithReviews(true, false)
		if rec := serve(s, path, headerAuthorization, "Bearer "+sampleToken); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", path, rec.Code)
		}
	}
}

func TestEveryReportEndpointFailsClosedWithoutAnAuthorizer(t *testing.T) {
	for path := range guardedPaths {
		s := &ReportServer{}
		if rec := serve(s, path, headerAuthorization, "Bearer "+sampleToken); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", path, rec.Code)
		}
	}
}

func TestComponentStatusesServedToAnAdmittedCaller(t *testing.T) {
	s, _ := routesWithReviews(true, true)
	if rec := serve(s, "/componentstatuses", headerAuthorization, "Bearer "+sampleToken); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHealthzNeedsNoToken(t *testing.T) {
	s, _ := routesWithReviews(false, false)
	if rec := serve(s, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
