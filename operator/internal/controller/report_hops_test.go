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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	hopDoor     = "liaison"
	hopAlice    = "oidc:alice"
	hopBob      = "oidc:bob"
	hopCrewNS   = "ns"
	hopCrewName = "c"
)

// notKubernetes is the API server's answer to a token that is not one of its own: unauthenticated,
// as the real reviewer says, so the door's assertion gets its turn.
type notKubernetes struct{ fakeAuth }

func (n notKubernetes) Authorize(ctx context.Context, token string, attrs authorizationv1.ResourceAttributes) (*identity.Identity, error) {
	id, err := n.fakeAuth.Authorize(ctx, token, attrs)
	if errors.Is(err, errForbidden) {
		return nil, errUnauthenticated
	}
	return id, err
}

type hopRig struct {
	key    *ecdsa.PrivateKey
	client client.Client
	server *ReportServer
}

func hopPublicPEM(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// newHopRig is a crew whose askers rule is `rule`, trusting one door, with its hash key Secret.
func newHopRig(t *testing.T, rule *kubemootv1alpha1.AskerRule, owners fakeOwners) *hopRig {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	crew := &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Namespace: hopCrewNS, Name: hopCrewName},
		Spec: kubemootv1alpha1.CrewSpec{Discussion: &kubemootv1alpha1.DiscussionConfig{Access: &kubemootv1alpha1.DiscussionAccess{
			TokenReview:      ptr.To(false),
			TrustedHops:      []kubemootv1alpha1.TrustedHop{{Name: hopDoor, PublicKeys: []string{hopPublicPEM(t, key)}}},
			Askers:           rule,
			HashKeySecretRef: &kubemootv1alpha1.HashKeySecretRef{Name: hashKeySecretNm},
		}}},
	}
	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crew,
		hashKeySecretOf(map[string][]byte{defaultHashKeySecretKey: testHashKey})).Build()
	hops := NewHopDirectory(c)
	return &hopRig{key: key, client: c, server: &ReportServer{
		Client: c, Owners: owners, Auth: notKubernetes{fakeAuth{allowed: "k8s-token"}}, Hops: hops,
	}}
}

// assertion is what the door signs for a person and this crew's gateway.
func (r *hopRig) assertion(t *testing.T, user, ns, crew string, now func() time.Time) string {
	t.Helper()
	signer, err := identity.NewHopSigner(r.key, hopDoor, now)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.Assert(identity.HopAudience(ns, crew), user, nil)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (r *hopRig) authorize(token string) (*identity.Identity, error) {
	req := httptest.NewRequest(http.MethodGet, "/conversation-export/ns/c", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return r.server.authorizeCaller(req, crewAccess(crewscope.Scope{Namespace: hopCrewNS, Crew: hopCrewName}), true)
}

var anySignedInRule = &kubemootv1alpha1.AskerRule{AnySignedIn: &kubemootv1alpha1.AnySignedInAskers{}}

// recordedHopOwner is what the gateway records for the person the hop asserted.
func recordedHopOwner(user string) bus.Principal {
	return identity.PrincipalOf(identity.Hasher{Key: testHashKey}, &identity.Identity{Issuer: identity.HopIssuerPrefix + hopDoor, Subject: user})
}

func TestAPersonTheDoorAssertedForThisCrewIsIdentified(t *testing.T) {
	r := newHopRig(t, anySignedInRule, fakeOwners{})
	id, err := r.authorize(r.assertion(t, hopAlice, hopCrewNS, hopCrewName, nil))
	if err != nil || id.Subject != hopAlice || id.Issuer != identity.HopIssuerPrefix+hopDoor {
		t.Fatalf("identity: %+v %v", id, err)
	}
}

func TestNoOtherAssertionIsAccepted(t *testing.T) {
	r := newHopRig(t, anySignedInRule, fakeOwners{})
	stranger, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	strangerSigner, _ := identity.NewHopSigner(stranger, hopDoor, nil)
	forged, _ := strangerSigner.Assert(identity.HopAudience(hopCrewNS, hopCrewName), hopAlice, nil)
	past := func() time.Time { return time.Now().Add(-time.Hour) }
	cases := map[string]string{
		"no token":                               "",
		"a token that is not ours":               "not-a-token",
		"signed for another crew":                r.assertion(t, hopAlice, hopCrewNS, "other", nil),
		"signed for another namespace":           r.assertion(t, hopAlice, "elsewhere", hopCrewName, nil),
		"expired":                                r.assertion(t, hopAlice, hopCrewNS, hopCrewName, past),
		"signed by a key the crew does not list": forged,
	}
	for name, token := range cases {
		if id, err := r.authorize(token); !errors.Is(err, errUnauthenticated) || id != nil {
			t.Errorf("%s: %v %v", name, id, err)
		}
	}
}

func TestACrewThatTrustsNoHopOrMapsPeopleToKubernetesKnowsNoDoorPerson(t *testing.T) {
	rbac := newHopRig(t, &kubemootv1alpha1.AskerRule{KubernetesRBAC: &kubemootv1alpha1.KubernetesRBACAskers{}}, fakeOwners{})
	if _, err := rbac.authorize(rbac.assertion(t, hopAlice, hopCrewNS, hopCrewName, nil)); !errors.Is(err, errUnauthenticated) {
		t.Errorf("a kubernetesRBAC crew: %v", err)
	}
	open := newHopRig(t, anySignedInRule, fakeOwners{})
	token := open.assertion(t, hopAlice, hopCrewNS, hopCrewName, nil)
	crew := &kubemootv1alpha1.Crew{}
	if err := open.client.Get(context.Background(), client.ObjectKey{Namespace: hopCrewNS, Name: hopCrewName}, crew); err != nil {
		t.Fatal(err)
	}
	crew.Spec.Discussion.Access.TrustedHops = nil
	if err := open.client.Update(context.Background(), crew); err != nil {
		t.Fatal(err)
	}
	if _, err := open.authorize(token); !errors.Is(err, errUnauthenticated) {
		t.Errorf("a crew that trusts no hop: %v", err)
	}
	missing := NewHopDirectory(open.client)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if _, err := missing.Authorize(req, crewAccess(crewscope.Scope{Namespace: hopCrewNS, Crew: "no-such-crew"})); !errors.Is(err, errUnauthenticated) {
		t.Errorf("a crew that does not exist: %v", err)
	}
}

func TestAKubernetesTokenIsNeverTriedAsAnAssertion(t *testing.T) {
	r := newHopRig(t, anySignedInRule, fakeOwners{})
	id, err := r.authorize("k8s-token")
	if err != nil || id.Issuer != identity.IssuerKubernetes {
		t.Fatalf("a Kubernetes caller keeps its own route: %+v %v", id, err)
	}
}

func exportAsHopPerson(t *testing.T, r *hopRig, user string) error {
	t.Helper()
	id, err := r.authorize(r.assertion(t, user, hopCrewNS, hopCrewName, nil))
	if err != nil {
		t.Fatal(err)
	}
	ctx := withReportCaller(context.Background(), reportCaller{identity: id})
	return r.server.mayExportConversation(ctx, crewscope.Scope{Namespace: hopCrewNS, Crew: hopCrewName}, convOne)
}

func TestADoorPersonExportsTheirOwnConversationAndNobodyElses(t *testing.T) {
	owners := fakeOwners{owners: map[string]bus.Principal{convOne: recordedHopOwner(hopAlice)}}
	r := newHopRig(t, anySignedInRule, owners)
	if err := exportAsHopPerson(t, r, hopAlice); err != nil {
		t.Fatalf("the owner was refused: %v", err)
	}
	if err := exportAsHopPerson(t, r, hopBob); !errors.Is(err, conversationexport.ErrRefused) {
		t.Fatalf("another door person: %v", err)
	}
}

// anyoneMayAuth is the real authorizer over an API server that says yes to every access review.
func anyoneMayAuth() (reportAuthorizer, *int) {
	asked := 0
	kube := kubefake.NewSimpleClientset()
	kube.PrependReactor("create", "subjectaccessreviews", func(k.Action) (bool, runtime.Object, error) {
		asked++
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	return NewReviewAuthorizer(kube), &asked
}

func TestADoorPersonIsNeverAnOperator(t *testing.T) {
	// Even where the API server would let anyone read every conversation, a person the door
	// asserted is not handed to it as a user name, so they cannot read another person's.
	owners := fakeOwners{owners: map[string]bus.Principal{convOne: recordedHopOwner(hopAlice)}}
	r := newHopRig(t, anySignedInRule, owners)
	auth, asked := anyoneMayAuth()
	id, err := r.authorize(r.assertion(t, hopBob, hopCrewNS, hopCrewName, nil))
	if err != nil {
		t.Fatal(err)
	}
	r.server.Auth = auth
	ctx := withReportCaller(context.Background(), reportCaller{identity: id, reason: "support"})
	scope := crewscope.Scope{Namespace: hopCrewNS, Crew: hopCrewName}
	if err := r.server.mayExportConversation(ctx, scope, convOne); !errors.Is(err, conversationexport.ErrRefused) {
		t.Fatalf("another person's conversation: %v", err)
	}
	if *asked != 0 {
		t.Fatalf("the API server was asked about a door person %d times", *asked)
	}
}

func TestADoorPersonCannotReachTheRoutesThatAreNotNarrowedToTheirOwn(t *testing.T) {
	r := newHopRig(t, anySignedInRule, fakeOwners{})
	token := r.assertion(t, hopAlice, hopCrewNS, hopCrewName, nil)
	for _, rt := range r.server.routeTable() {
		if rt.subscribers {
			continue
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetPathValue("namespace", hopCrewNS)
		req.SetPathValue("crew", hopCrewName)
		req.SetPathValue("name", hopCrewName)
		req.Header.Set("Authorization", "Bearer "+token)
		attrs, err := rt.access(req)
		if err != nil {
			continue
		}
		if id, err := r.server.authorizeCaller(req, attrs, rt.subscribers); err == nil {
			t.Errorf("%s admitted a door person as %+v", rt.pattern, id)
		}
	}
}
