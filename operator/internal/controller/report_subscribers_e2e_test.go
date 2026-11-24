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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"

	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

// claimAsTheGatewayDoes records the person as the owner of the conversation the way the
// crew's gateway does: the principal comes from the same verifier chain (the person's own
// token, accepted by the service, named with the crew's keyed hasher) and is written as the
// owner record in the conversations bucket.
func claimAsTheGatewayDoes(t *testing.T, rig *subscriberRig, kv nats.KeyValue, token, conversation string) string {
	t.Helper()
	c, err := rig.server.Subscribers.crewChain(context.Background(), types.NamespacedName{Namespace: "ns", Name: "c"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set(headerAuthorization, "Bearer "+token)
	caller, err := c.auth.Authenticate(req)
	if err != nil {
		t.Fatalf("the service did not accept the person: %v", err)
	}
	if caller.Principal.Kind != bus.PrincipalKindSubscriber {
		t.Fatalf("the owner is %+v, want a subscriber", caller.Principal)
	}
	value, _ := json.Marshal(bus.ConversationOwner{Owner: caller.Principal})
	if _, err := kv.Create(bus.ConversationOwnerKey("ns", "c", conversation), value); err != nil {
		t.Fatal(err)
	}
	return string(value)
}

// ownedConversationRig is the report service of a crew whose owner record for conversation
// "one" is in a real NATS bucket, claimed the way the gateway claims it from the person's token.
func ownedConversationRig(t *testing.T) (*subscriberRig, *kubemootnats.Publisher) {
	t.Helper()
	env := natstest.Start(t, natstest.Options{JetStream: true})
	nc, err := nats.Connect(env.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bus.ConversationsBucket})
	if err != nil {
		t.Fatal(err)
	}
	pub := kubemootnats.NewPublisher(env.URL)
	t.Cleanup(pub.Close)
	rig := newSubscriberRig(t, false)
	rig.server.Owners = pub
	record := claimAsTheGatewayDoes(t, rig, kv, personToken(ownerSubject), convOne)
	if strings.Contains(record, ownerSubject) || strings.Contains(record, personToken(ownerSubject)) {
		t.Fatalf("the owner record holds the subject or the token: %s", record)
	}
	return rig, pub
}

// TestAPersonOwnsAConversationAndOnlyTheyExportIt follows one person through the report
// service with a real NATS owner record and the real verifier chain: they ask (the owner is
// claimed from their own token) and export their conversation; another person with a token
// the service also accepts, a caller with no token, and an unverified identity header cannot.
func TestAPersonOwnsAConversationAndOnlyTheyExportIt(t *testing.T) {
	rig, pub := ownedConversationRig(t)
	owner, found, err := pub.ConversationOwner(context.Background(), crewscope.Scope{Namespace: "ns", Crew: "c"}, convOne)
	if err != nil || !found {
		t.Fatalf("owner found=%v err=%v", found, err)
	}
	if owner.ID != recordedOwner().ID {
		t.Fatalf("the stored owner %q is not the crew's keyed hash of the person %q", owner.ID, recordedOwner().ID)
	}
	rec := asPerson(rig, ownerSubject, exportPath)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"conversationId":"one"`) {
		t.Fatalf("the owner's export: status %d: %s", rec.Code, rec.Body.String())
	}
	if zip := asPerson(rig, ownerSubject, "/conversation-export/ns/c?id=one"); zip.Code != http.StatusOK {
		t.Fatalf("the owner's zip: status %d", zip.Code)
	}
}

func TestNobodyElseExportsAPersonsConversation(t *testing.T) {
	rig, _ := ownedConversationRig(t)
	if rec := asPerson(rig, otherSubject, exportPath); rec.Code != http.StatusForbidden {
		t.Fatalf("another person's export: status %d", rec.Code)
	}
	if rec := rig.get(exportPath); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", rec.Code)
	}
	if rec := rig.get(exportPath, "X-Remote-User", ownerSubject); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unverified identity header: status %d", rec.Code)
	}
	if rig.asked.Load() != 0 {
		t.Fatal("a person's subject must never be handed to the API server as a user name")
	}
}
