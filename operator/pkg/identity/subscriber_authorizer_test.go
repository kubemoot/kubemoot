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
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
)

type recordingAuthorizer struct{ asked int }

func (r *recordingAuthorizer) Allowed(context.Context, *Identity, authorizationv1.ResourceAttributes) (bool, error) {
	r.asked++
	return true, nil
}

func TestSubscriberMayUseTheCrewOnlyThroughAConfiguredVerifier(t *testing.T) {
	cfgs, err := ParseServiceTokens(`[
	  {"name":"oreilly","verifyURL":"https://x.example","subjectClaim":"sub","audiences":["o"]},
	  {"name":"club","verifyURL":"https://y.example","subjectClaim":"sub","audiences":["c"],"authorize":"Subjects","subjects":["member-1"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	a := NewSubscriberAuthorizer(cfgs)
	use := UseAccess("ns", "pilot")
	cases := []struct {
		name string
		id   Identity
		want bool
	}{
		{"any accepted person", Identity{Issuer: testIssuer, Subject: "whoever"}, true},
		{"listed subject", Identity{Issuer: "club", Subject: "member-1"}, true},
		{"unlisted subject", Identity{Issuer: "club", Subject: "member-2"}, false},
		{"issuer with no verifier", Identity{Issuer: "elsewhere", Subject: "x"}, false},
	}
	for _, c := range cases {
		got, err := a.Allowed(context.Background(), &c.id, use)
		if err != nil || got != c.want {
			t.Errorf("%s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
}

func TestSubscriberHasNoOtherAccess(t *testing.T) {
	a := NewSubscriberAuthorizer([]ServiceTokenConfig{{Name: testIssuer, Authorize: AuthorizeAnyAccepted}})
	id := &Identity{Issuer: testIssuer, Subject: "x"}
	for _, attrs := range []authorizationv1.ResourceAttributes{
		ConversationsAccess("ns", "pilot", "get"),
		ConversationsAccess("ns", "pilot", "list"),
		ConversationsAccess("ns", "pilot", "update"),
		ConversationsAccess("ns", "pilot", "delete"),
		{Verb: "get", Resource: "secrets", Name: "x"},
		{Verb: "get", Resource: "crews"},
		{Verb: "delete", Resource: "crews", Name: "pilot"},
	} {
		if ok, _ := a.Allowed(context.Background(), id, attrs); ok {
			t.Errorf("a subscriber was allowed %+v", attrs)
		}
	}
}

func TestRoutedAuthorizerSendsEachKindToItsOwnDecider(t *testing.T) {
	k8s, subs := &recordingAuthorizer{}, &recordingAuthorizer{}
	r := RoutedAuthorizer{Kubernetes: k8s, Subscribers: subs}
	use := UseAccess("ns", "pilot")
	_, _ = r.Allowed(context.Background(), &Identity{Issuer: IssuerKubernetes, Subject: "u"}, use)
	_, _ = r.Allowed(context.Background(), &Identity{Issuer: testIssuer, Subject: "p"}, use)
	if k8s.asked != 1 || subs.asked != 1 {
		t.Fatalf("kubernetes asked %d, subscribers asked %d", k8s.asked, subs.asked)
	}
	// A kind with no authorizer is refused, never allowed.
	none := RoutedAuthorizer{}
	for _, issuer := range []string{IssuerKubernetes, testIssuer} {
		if ok, err := none.Allowed(context.Background(), &Identity{Issuer: issuer}, use); ok || err != nil {
			t.Errorf("%s: %v %v", issuer, ok, err)
		}
	}
}
