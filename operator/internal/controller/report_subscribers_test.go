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
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	kubeToken     = "kubernetes-token"
	rejectedToken = "rejected-person"
	exportPath    = "/conversation-export/ns/c?id=one&report=true"
	claimPath     = "upstream_claims.sub"
)

// tokenExpiry is fixed so one person's token is the same string on every call.
var tokenExpiry = time.Now().Add(time.Hour).Unix()

// personToken is a JWT naming subject at upstream_claims.sub, as a service issues it. The
// signature is not checked here; the service's answer is the proof.
func personToken(subject string) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(map[string]any{
		"upstream_claims": map[string]any{"sub": subject}, "exp": tokenExpiry,
	})
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + enc.EncodeToString(payload) + ".c2ln"
}

// subscriberRig is the report service for a crew whose people are verified by a service.
type subscriberRig struct {
	server  *ReportServer
	service *httptest.Server
	asked   atomic.Int32 // SubjectAccessReviews made
	called  atomic.Int32 // calls to the service
}

// newSubscriberRig holds crew ns/c with one service token verifier, its hash key Secret
// unless noKey, and the owner of conversation "one" as the person subject ownerSubject.
func newSubscriberRig(t *testing.T, noKey bool) *subscriberRig {
	t.Helper()
	rig := &subscriberRig{}
	rig.service = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.called.Add(1)
		if strings.Contains(r.Header.Get("Authorization"), personToken(rejectedToken)) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rig.service.Close)

	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	crew := &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "c"},
		Spec: kubemootv1alpha1.CrewSpec{Discussion: &kubemootv1alpha1.DiscussionConfig{Access: &kubemootv1alpha1.DiscussionAccess{
			ServiceTokens: []kubemootv1alpha1.ServiceTokenVerifier{{
				Name: subscriberIssuer, VerifyURL: rig.service.URL, SubjectClaim: claimPath,
			}},
			HashKeySecretRef: &kubemootv1alpha1.HashKeySecretRef{Name: hashKeySecretNm},
		}}},
	}
	objs := []client.Object{crew}
	if !noKey {
		objs = append(objs, hashKeySecretOf(map[string][]byte{defaultHashKeySecretKey: testHashKey}))
	}
	reader := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		tr := a.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		ok := tr.Spec.Token == kubeToken
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: ok, User: authenticationv1.UserInfo{Username: kubeUserSubject},
		}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		rig.asked.Add(1)
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})

	directory := NewSubscriberDirectory(reader)
	directory.client = rig.service.Client()
	rig.server = convServer(twoConversations())
	rig.server.Client = reader
	rig.server.Auth = NewReviewAuthorizer(kube)
	rig.server.Subscribers = directory
	rig.server.Owners = fakeOwners{owners: map[string]bus.Principal{convOne: recordedOwner()}}
	return rig
}

func (r *subscriberRig) get(path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	r.server.routes().ServeHTTP(rec, req)
	return rec
}

func asPerson(r *subscriberRig, subject, path string) *httptest.ResponseRecorder {
	return r.get(path, headerAuthorization, "Bearer "+personToken(subject))
}

func TestSubscriberExportsTheirOwnConversationWithTheirOwnToken(t *testing.T) {
	rig := newSubscriberRig(t, false)
	rec := asPerson(rig, ownerSubject, exportPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rig.asked.Load() != 0 {
		t.Fatal("a subscriber's subject must never be handed to the API server as a user name")
	}
}

func TestAnotherSubscriberIsRefusedTheConversation(t *testing.T) {
	rig := newSubscriberRig(t, false)
	if rec := asPerson(rig, otherSubject, exportPath); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubscriberNamedLikeAClusterAdminGainsNoOperatorRights(t *testing.T) {
	rig := newSubscriberRig(t, false)
	// The cluster would allow "system:admin" everything; a subscriber who is named that by
	// the service is still only a subscriber, and the reason header does not change it.
	rec := asPerson(rig, "system:admin", exportPath)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, exportPath, nil)
	req.Header.Set(headerAuthorization, "Bearer "+personToken("system:admin"))
	req.Header.Set(identity.HeaderAccessReason, "operator review")
	out := httptest.NewRecorder()
	rig.server.routes().ServeHTTP(out, req)
	if out.Code != http.StatusForbidden || rig.asked.Load() != 0 {
		t.Fatalf("status %d, access reviews %d", out.Code, rig.asked.Load())
	}
}

func TestKubernetesCallerIsUnaffected(t *testing.T) {
	rig := newSubscriberRig(t, false)
	rig.server.Owners = fakeOwners{owners: map[string]bus.Principal{convOne: identity.PrincipalOf(identity.Hasher{},
		&identity.Identity{Issuer: identity.IssuerKubernetes, Subject: kubeUserSubject})}}
	rec := rig.get(exportPath, headerAuthorization, "Bearer "+kubeToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rig.called.Load() != 0 {
		t.Fatal("a Kubernetes token must never be sent to a service")
	}
	if rig.asked.Load() == 0 {
		t.Fatal("a Kubernetes caller's access is still checked by RBAC")
	}
}

func TestMissingHashKeyFailsClosed(t *testing.T) {
	rig := newSubscriberRig(t, true)
	rec := asPerson(rig, ownerSubject, exportPath)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rig.called.Load() != 0 {
		t.Fatal("without the key nothing is verified")
	}
}

func TestUnverifiedIdentityHeadersAreIgnored(t *testing.T) {
	rig := newSubscriberRig(t, false)
	forged := []string{
		"X-Remote-User", kubeUserSubject, "X-Remote-Group", "system:masters", "X-Forwarded-User", ownerSubject,
		"Impersonate-User", ownerSubject, "Kubemoot-Subject", ownerSubject,
	}
	if rec := rig.get(exportPath, forged...); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token and forged headers: status %d", rec.Code)
	}
	// With another person's valid token the headers do not change who is asking.
	withToken := append([]string{headerAuthorization, "Bearer " + personToken(otherSubject)}, forged...)
	if rec := rig.get(exportPath, withToken...); rec.Code != http.StatusForbidden {
		t.Fatalf("a valid token and forged headers: status %d", rec.Code)
	}
}

func TestSubscribersReachOnlyTheirConversationExport(t *testing.T) {
	rig := newSubscriberRig(t, false)
	for _, path := range []string{"/memory-export/ns/c", "/componentstatuses", "/report/ns/suite"} {
		if rec := asPerson(rig, ownerSubject, path); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}
	if rig.called.Load() != 0 {
		t.Fatal("a route that does not admit subscribers never asks a service")
	}
}

func TestRejectedPersonAndOtherCrewsAreUnauthenticated(t *testing.T) {
	rig := newSubscriberRig(t, false)
	if rec := asPerson(rig, rejectedToken, exportPath); rec.Code != http.StatusUnauthorized {
		t.Errorf("a token the service refuses: status %d", rec.Code)
	}
	if rec := asPerson(rig, ownerSubject, "/conversation-export/ns/missing?id=one"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a crew that does not exist: status %d", rec.Code)
	}
}

func TestACrewWithNoServiceTokensKnowsNoSubscribers(t *testing.T) {
	rig := newSubscriberRig(t, false)
	rig.server.Client = crewClient(&kubemootv1alpha1.DiscussionAccess{})
	rig.server.Subscribers = NewSubscriberDirectory(rig.server.Client)
	if rec := asPerson(rig, ownerSubject, exportPath); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestChainIsKeptAndRebuiltWhenTheKeyChanges(t *testing.T) {
	rig := newSubscriberRig(t, false)
	asPerson(rig, ownerSubject, exportPath)
	asPerson(rig, ownerSubject, exportPath)
	if got := rig.called.Load(); got != 1 {
		t.Fatalf("the verifier's cache should hold across requests, calls = %d", got)
	}
	secret := &corev1.Secret{}
	reader := rig.server.Client
	if err := reader.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: hashKeySecretNm}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data[defaultHashKeySecretKey] = []byte("rotated-key")
	if err := reader.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	// Owners were recorded with the old key, so the rotated key names the person differently.
	if rec := asPerson(rig, ownerSubject, exportPath); rec.Code != http.StatusForbidden {
		t.Fatalf("after a key change the old owner record no longer matches: status %d", rec.Code)
	}
	if got := rig.called.Load(); got != 2 {
		t.Fatalf("a new key builds a new chain with an empty cache, calls = %d", got)
	}
}

func TestSubscriberChecksThatCannotBeMadeAnswer503(t *testing.T) {
	rig := newSubscriberRig(t, false)
	rig.service.Close() // the service cannot be reached
	if rec := asPerson(rig, ownerSubject, exportPath); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("an unreachable service: status %d", rec.Code)
	}
	bad := newSubscriberRig(t, false)
	crew := &kubemootv1alpha1.Crew{}
	_ = bad.server.Client.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "c"}, crew)
	crew.Spec.Discussion.Access.ServiceTokens[0].VerifyURL = "http://plain.example"
	_ = bad.server.Client.Update(context.Background(), crew)
	if rec := asPerson(bad, ownerSubject, exportPath); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("an invalid verifier list: status %d", rec.Code)
	}
}

func TestSubscriberWithNoTokenIsUnauthenticatedBeforeAnythingIsRead(t *testing.T) {
	rig := newSubscriberRig(t, true) // no key: would be 503 if the key were read first
	if rec := rig.get(exportPath); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestChainIsRebuiltWhenTheVerifiersChangeAndDroppedWithTheCrew(t *testing.T) {
	rig := newSubscriberRig(t, false)
	asPerson(rig, ownerSubject, exportPath)
	crew := &kubemootv1alpha1.Crew{}
	ctx := context.Background()
	_ = rig.server.Client.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "c"}, crew)
	crew.Spec.Discussion.Access.ServiceTokens[0].MaxCacheSeconds = 60
	if err := rig.server.Client.Update(ctx, crew); err != nil {
		t.Fatal(err)
	}
	asPerson(rig, ownerSubject, exportPath)
	if got := rig.called.Load(); got != 2 {
		t.Fatalf("changed verifiers build a new chain, calls = %d", got)
	}
	if err := rig.server.Client.Delete(ctx, crew); err != nil {
		t.Fatal(err)
	}
	asPerson(rig, ownerSubject, exportPath)
	if n := len(rig.server.Subscribers.chains); n != 0 {
		t.Fatalf("a deleted crew keeps %d chains", n)
	}
}
