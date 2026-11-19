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
	"regexp"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
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

// wildcard matches a {name} segment of a route pattern.
var wildcard = regexp.MustCompile(`\{[^}]+\}`)

func TestNoRouteIsServedWithoutPassingTheAccessGate(t *testing.T) {
	s, _ := routesWithReviews(false, true)
	table := s.routeTable()
	if len(table) == 0 {
		t.Fatal("the route table is empty")
	}
	for _, rt := range table {
		method, path, _ := strings.Cut(rt.pattern, " ")
		if rt.pattern == healthzPattern {
			t.Errorf("%s is in the gated table", rt.pattern)
		}
		path = wildcard.ReplaceAllString(path, "x")
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: status = %d, want 401", rt.pattern, rec.Code)
		}
	}
}

func TestSuiteReportFailureCarriesNoCauseToTheClient(t *testing.T) {
	var logged strings.Builder
	saved := reportLog
	reportLog = funcr.New(func(prefix, args string) { logged.WriteString(prefix + args + "\n") }, funcr.Options{})
	defer func() { reportLog = saved }()

	s, _ := routesWithReviews(true, true) // no such suite: the report cannot be built
	rec := serve(s, "/report/tenant/missing", headerAuthorization, "Bearer "+sampleToken)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "missing") || strings.Contains(body, "not found") {
		t.Errorf("the body carries the cause: %q", body)
	}
	if !strings.Contains(logged.String(), "not found") {
		t.Errorf("the cause was not logged: %q", logged.String())
	}
}
