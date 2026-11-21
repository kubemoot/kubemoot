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
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// crewClient is a client holding the crew ns/c, which verifies callers when access is set.
func crewClient(access *kubemootv1alpha1.DiscussionAccess) client.Client {
	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	crew := &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "c"},
		Spec:       kubemootv1alpha1.CrewSpec{Discussion: &kubemootv1alpha1.DiscussionConfig{Access: access}},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(crew).Build()
}

// fakeOwners answers owner lookups from a table keyed by conversation id.
type fakeOwners struct {
	owners map[string]bus.Principal
	err    error
}

func (f fakeOwners) ConversationOwner(_ context.Context, _ crewscope.Scope, id string) (bus.Principal, bool, error) {
	owner, ok := f.owners[id]
	return owner, ok, f.err
}

// callerPrincipal is the principal of the caller fakeAuth admits: the Kubernetes user "dashboard".
func callerPrincipal() bus.Principal {
	return bus.Principal{Kind: bus.PrincipalKindKubernetes, ID: identity.Hasher{}.ID(identity.IssuerKubernetes, "dashboard")}
}

func someoneElse() bus.Principal {
	return bus.Principal{Kind: bus.PrincipalKindKubernetes, ID: identity.Hasher{}.ID(identity.IssuerKubernetes, "someone-else")}
}

// verifyingServer is the report service for a crew that verifies callers.
func verifyingServer(owners fakeOwners, operator bool) *ReportServer {
	s := convServer(twoConversations())
	s.Client = crewClient(&kubemootv1alpha1.DiscussionAccess{})
	s.Owners = owners
	s.Auth = fakeAuth{allowed: goodToken, operator: operator}
	return s
}

func TestTheOwnerMayExportTheirConversation(t *testing.T) {
	s := verifyingServer(fakeOwners{owners: map[string]bus.Principal{convOne: callerPrincipal()}}, false)
	if rec := asDashboardConv(s, "?id=one"); rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnotherCallerMayNotExportAConversation(t *testing.T) {
	s := verifyingServer(fakeOwners{owners: map[string]bus.Principal{convOne: someoneElse()}}, false)
	if rec := asDashboardConv(s, "?id=one&report=true"); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	// A conversation with no owner record belongs to no one, so only an operator may export it.
	s = verifyingServer(fakeOwners{}, false)
	if rec := asDashboardConv(s, "?id=one&report=true"); rec.Code != http.StatusForbidden {
		t.Fatalf("no record: status %d", rec.Code)
	}
}

func TestAnOperatorNeedsAReasonToExportAnotherCallersConversationAndIsAudited(t *testing.T) {
	var lines []string
	prev := exportAuditLog
	exportAuditLog = funcr.New(func(prefix, args string) { lines = append(lines, prefix+args) }, funcr.Options{})
	t.Cleanup(func() { exportAuditLog = prev })
	s := verifyingServer(fakeOwners{owners: map[string]bus.Principal{convOne: someoneElse()}}, true)

	rec := asDashboardConv(s, "?id=one&report=true")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), identity.HeaderAccessReason) {
		t.Fatalf("without a reason: %d %s", rec.Code, rec.Body.String())
	}
	rec = getConversations(s, "?id=one&report=true", headerAuthorization, "Bearer "+goodToken, identity.HeaderAccessReason, "data-subject request 7")
	if rec.Code != http.StatusOK {
		t.Fatalf("with a reason: %d %s", rec.Code, rec.Body.String())
	}
	audit := strings.Join(lines, "\n")
	for _, want := range []string{"kubemoot.conversation.access", "export", "data-subject request 7", someoneElse().ID} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit record lacks %q: %s", want, audit)
		}
	}
}

func TestExportOnACrewThatVerifiesFailsClosed(t *testing.T) {
	failing := fakeOwners{err: errors.New("nats down")}
	cases := map[string]*ReportServer{
		"owners unreadable": verifyingServer(failing, true),
		"no owner reader":   func() *ReportServer { s := verifyingServer(fakeOwners{}, true); s.Owners = nil; return s }(),
	}
	want := map[string]int{"owners unreadable": http.StatusInternalServerError, "no owner reader": http.StatusForbidden}
	for name, s := range cases {
		if rec := asDashboardConv(s, "?id=one&report=true"); rec.Code != want[name] {
			t.Errorf("%s: status %d, want %d", name, rec.Code, want[name])
		}
	}
}

func TestExportOfAConversationOfACrewThatDoesNotExistIsRefused(t *testing.T) {
	s := verifyingServer(fakeOwners{}, true)
	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	s.Client = fake.NewClientBuilder().WithScheme(scheme).Build()
	if rec := asDashboardConv(s, "?id=one&report=true"); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestExportRefusesIdentityConfusion(t *testing.T) {
	sameIDOtherKind := bus.Principal{Kind: bus.PrincipalKindAnonymous, ID: callerPrincipal().ID}
	otherIssuer := bus.Principal{Kind: bus.PrincipalKindKubernetes,
		ID: identity.Hasher{}.ID("https://idp.example", "dashboard")}
	for name, owner := range map[string]bus.Principal{"another kind with the same id": sameIDOtherKind, "another issuer": otherIssuer} {
		s := verifyingServer(fakeOwners{owners: map[string]bus.Principal{convOne: owner}}, false)
		if rec := asDashboardConv(s, "?id=one&report=true"); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
}

func TestAnOperatorMayExportAConversationWithNoOwnerRecordWithAReason(t *testing.T) {
	var lines []string
	prev := exportAuditLog
	exportAuditLog = funcr.New(func(prefix, args string) { lines = append(lines, prefix+args) }, funcr.Options{})
	t.Cleanup(func() { exportAuditLog = prev })
	s := verifyingServer(fakeOwners{}, true)
	rec := getConversations(s, "?id=one&report=true", headerAuthorization, "Bearer "+goodToken, identity.HeaderAccessReason, "orphaned")
	if rec.Code != http.StatusOK || len(lines) != 1 || !strings.Contains(lines[0], `"owner"=""`) {
		t.Fatalf("%d %v", rec.Code, lines)
	}
	tooLong := strings.Repeat("x", identity.MaxAccessReason+1)
	rec = getConversations(s, "?id=one&report=true", headerAuthorization, "Bearer "+goodToken, identity.HeaderAccessReason, tooLong)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a reason past the bound: %d", rec.Code)
	}
}

func TestAFailedOperatorCheckIsAServerErrorNotARefusalOrAnAllow(t *testing.T) {
	s := verifyingServer(fakeOwners{owners: map[string]bus.Principal{convOne: someoneElse()}}, true)
	s.Auth = fakeAuth{allowed: goodToken, operator: true, allowedErr: errors.New("apiserver down")}
	if rec := asDashboardConv(s, "?id=one&report=true"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestRequireAccessPutsTheVerifiedCallerAndReasonInTheContext(t *testing.T) {
	var got reportCaller
	var ok bool
	s := &ReportServer{Auth: fakeAuth{allowed: goodToken}}
	h := s.requireAccess(func(*http.Request) (authorizationv1.ResourceAttributes, error) {
		return authorizationv1.ResourceAttributes{}, nil
	}, func(_ http.ResponseWriter, r *http.Request) { got, ok = reportCallerFrom(r.Context()) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(headerAuthorization, "Bearer "+goodToken)
	req.Header.Set(identity.HeaderAccessReason, "  because ")
	h(httptest.NewRecorder(), req)
	if !ok || got.identity.Subject != "dashboard" || got.reason != "because" {
		t.Fatalf("%+v %v", got, ok)
	}
	if _, ok := reportCallerFrom(context.Background()); ok {
		t.Fatal("no caller in the context is no caller")
	}
}

func TestReviewAuthorizerAllowedAsksTheAPIServer(t *testing.T) {
	c, asked := reviewClient(true, true)
	id := &identity.Identity{Subject: dashboardUser}
	ok, err := NewReviewAuthorizer(c).Allowed(context.Background(), id, identity.ConversationsAccess("ns", "c", verbGet))
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if attrs := asked.Spec.ResourceAttributes; attrs.Subresource != "conversations" || attrs.Resource != "crews" {
		t.Errorf("%+v", attrs)
	}
}

const dashboardUser = "system:serviceaccount:kubemoot:dashboard"
