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
	"net/http/httptest"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const sampleToken = "tkn"

// reviewClient answers token and access reviews the way an API server would.
func reviewClient(authenticated, allowed bool) (*fake.Clientset, *authorizationv1.SubjectAccessReview) {
	c := fake.NewSimpleClientset()
	var asked authorizationv1.SubjectAccessReview
	c.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: authenticated,
			User:          authenticationv1.UserInfo{Username: "system:serviceaccount:kubemoot:dashboard", Groups: []string{"g"}},
		}}, nil
	})
	c.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		asked = *a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	return c, &asked
}

func TestReviewAuthorizerAdmitsAnIdentityThatMayGetTheCrew(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	c, asked := reviewClient(true, true)
	if err := NewReviewAuthorizer(c).Authorize(context.Background(), sampleToken, crewAccess(scope)); err != nil {
		t.Fatal(err)
	}
	attrs := asked.Spec.ResourceAttributes
	if asked.Spec.User != "system:serviceaccount:kubemoot:dashboard" || attrs.Verb != verbGet || attrs.Group != kubemootv1alpha1.GroupVersion.Group ||
		attrs.Resource != "crews" || attrs.Namespace != "ns" || attrs.Name != "c" {
		t.Errorf("access review = %+v / %+v", asked.Spec, attrs)
	}
}

func TestReviewAuthorizerRefuses(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	attrs := crewAccess(scope)
	cases := map[string]struct {
		authenticated, allowed bool
		token                  string
		want                   error
	}{
		"no token":                 {true, true, "", errUnauthenticated},
		"a token the API rejects":  {false, true, sampleToken, errUnauthenticated},
		"an identity without RBAC": {true, false, sampleToken, errForbidden},
	}
	for name, c := range cases {
		client, _ := reviewClient(c.authenticated, c.allowed)
		if err := NewReviewAuthorizer(client).Authorize(context.Background(), c.token, attrs); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

func TestReviewAuthorizerReportsAFailedReviewAsNeitherAdmitNorRefuse(t *testing.T) {
	scope, _ := crewscope.New("ns", "c")
	c := fake.NewSimpleClientset()
	c.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver down")
	})
	err := NewReviewAuthorizer(c).Authorize(context.Background(), sampleToken, crewAccess(scope))
	if err == nil || errors.Is(err, errUnauthenticated) || errors.Is(err, errForbidden) {
		t.Errorf("err = %v, want a review failure", err)
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{"Bearer " + sampleToken: sampleToken, "bearer " + sampleToken: sampleToken, "Basic " + sampleToken: "", "Bearer": "", "": "", sampleToken: ""}
	for header, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set(headerAuthorization, header)
		}
		if got := identity.BearerToken(r); got != want {
			t.Errorf("BearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}
