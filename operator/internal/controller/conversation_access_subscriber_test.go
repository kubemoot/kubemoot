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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/conversationexport"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	subscriberIssuer = "oreilly"
	hashKeySecretNm  = "access-hash"
	ownerSubject     = "acct-1"
	otherSubject     = "acct-2"
	kubeUserSubject  = "dash-user"
)

var testHashKey = []byte("install-hash-key")

// keyedCrewClient holds crew ns/c with a hashKeySecretRef, and the Secret when secret is not nil.
func keyedCrewClient(ref *kubemootv1alpha1.HashKeySecretRef, secret *corev1.Secret) client.Client {
	scheme := runtime.NewScheme()
	_ = kubemootv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	crew := &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "c"},
		Spec: kubemootv1alpha1.CrewSpec{Discussion: &kubemootv1alpha1.DiscussionConfig{
			Access: &kubemootv1alpha1.DiscussionAccess{HashKeySecretRef: ref},
		}},
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crew)
	if secret != nil {
		b = b.WithObjects(secret)
	}
	return b.Build()
}

func hashKeySecretOf(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: hashKeySecretNm}, Data: data}
}

func personVia(subject string) *identity.Identity {
	return &identity.Identity{Issuer: subscriberIssuer, Subject: subject}
}

// recordedOwner is what the gateway records for a subscriber: PrincipalOf with the keyed hasher.
func recordedOwner() bus.Principal {
	return identity.PrincipalOf(identity.Hasher{Key: testHashKey}, personVia(ownerSubject))
}

func exportAs(c client.Client, owners fakeOwners, id *identity.Identity) error {
	s := &ReportServer{Client: c, Owners: owners, Auth: fakeAuth{}}
	ctx := withReportCaller(context.Background(), reportCaller{identity: id})
	return s.mayExportConversation(ctx, crewscope.Scope{Namespace: "ns", Crew: "c"}, convOne)
}

func TestSubscriberOwnerMayExportTheirConversation(t *testing.T) {
	c := keyedCrewClient(&kubemootv1alpha1.HashKeySecretRef{Name: hashKeySecretNm}, hashKeySecretOf(map[string][]byte{defaultHashKeySecretKey: testHashKey}))
	owners := fakeOwners{owners: map[string]bus.Principal{convOne: recordedOwner()}}
	if err := exportAs(c, owners, personVia(ownerSubject)); err != nil {
		t.Fatalf("the owner was refused: %v", err)
	}
}

func TestSubscriberUsesTheSecretKeyNamedByTheCrew(t *testing.T) {
	c := keyedCrewClient(&kubemootv1alpha1.HashKeySecretRef{Name: hashKeySecretNm, Key: "k"}, hashKeySecretOf(map[string][]byte{"k": testHashKey}))
	owners := fakeOwners{owners: map[string]bus.Principal{convOne: recordedOwner()}}
	if err := exportAs(c, owners, personVia(ownerSubject)); err != nil {
		t.Fatalf("the owner was refused: %v", err)
	}
}

func TestAnotherSubscriberMayNotExport(t *testing.T) {
	c := keyedCrewClient(&kubemootv1alpha1.HashKeySecretRef{Name: hashKeySecretNm}, hashKeySecretOf(map[string][]byte{defaultHashKeySecretKey: testHashKey}))
	owners := fakeOwners{owners: map[string]bus.Principal{convOne: recordedOwner()}}
	err := exportAs(c, owners, personVia(otherSubject))
	if !errors.Is(err, conversationexport.ErrRefused) {
		t.Fatalf("another subscriber: %v", err)
	}
}

func TestSubscriberFailsClosedWithoutAReadableHashKey(t *testing.T) {
	ref := &kubemootv1alpha1.HashKeySecretRef{Name: hashKeySecretNm}
	cases := map[string]client.Client{
		"no ref":       keyedCrewClient(nil, nil),
		"no secret":    keyedCrewClient(ref, nil),
		"key missing":  keyedCrewClient(ref, hashKeySecretOf(map[string][]byte{testMismatchName: testHashKey})),
		"key is empty": keyedCrewClient(ref, hashKeySecretOf(map[string][]byte{defaultHashKeySecretKey: {}})),
	}
	// The recorded owner is the unkeyed-hash look-alike a missing key would produce.
	unkeyed := identity.PrincipalOf(identity.Hasher{}, personVia(ownerSubject))
	for name, c := range cases {
		for _, owner := range []bus.Principal{recordedOwner(), unkeyed} {
			err := exportAs(c, fakeOwners{owners: map[string]bus.Principal{convOne: owner}}, personVia(ownerSubject))
			if !errors.Is(err, conversationexport.ErrRefused) {
				t.Errorf("%s: want a refusal, got %v", name, err)
			}
		}
	}
}

func TestKubernetesCallerNeedsNoHashKey(t *testing.T) {
	c := keyedCrewClient(nil, nil)
	k8s := &identity.Identity{Issuer: identity.IssuerKubernetes, Subject: kubeUserSubject}
	if err := exportAs(c, fakeOwners{owners: map[string]bus.Principal{convOne: identity.PrincipalOf(identity.Hasher{}, k8s)}}, k8s); err != nil {
		t.Fatalf("the Kubernetes owner was refused: %v", err)
	}
}

func TestHashKeySecretDefaultsTheKeyName(t *testing.T) {
	if _, _, ok := hashKeySecret(nil); ok {
		t.Error("nil access has no hash key")
	}
	if _, _, ok := hashKeySecret(&kubemootv1alpha1.DiscussionAccess{}); ok {
		t.Error("access without a ref has no hash key")
	}
	name, key, ok := hashKeySecret(&kubemootv1alpha1.DiscussionAccess{HashKeySecretRef: &kubemootv1alpha1.HashKeySecretRef{Name: "n"}})
	if !ok || name != "n" || key != defaultHashKeySecretKey {
		t.Errorf("got %q %q %v", name, key, ok)
	}
}
