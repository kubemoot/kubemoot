package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const (
	accountToken  = "access-token-of-the-person"
	accountPerson = "person-key-1"
	accountName   = "library"
	noStore       = "no-store"
)

type fakeOwners struct {
	owner bus.Principal
	found bool
	err   error
}

func (f fakeOwners) Owner(context.Context, string) (bus.Principal, bool, error) {
	return f.owner, f.found, f.err
}

type fakeLeaser struct {
	lease     identity.AccountLease
	err       error
	principal string
	account   string
}

func (f *fakeLeaser) Lease(_ context.Context, principalID, account string) (identity.AccountLease, error) {
	f.principal, f.account = principalID, account
	return f.lease, f.err
}

func doorPerson() bus.Principal {
	return bus.Principal{Kind: bus.PrincipalKindSubscriber, Issuer: "hop:liaison", ID: accountPerson}
}

func goodLease() identity.AccountLease {
	return identity.AccountLease{Token: accountToken, ExpiresAt: time.Now().Add(time.Hour), TokenHosts: []string{"api.example.org"}}
}

func accountRig(t *testing.T, owners OwnerReader, leaser identity.AccountLeaser) *httptest.Server {
	t.Helper()
	rv := identity.Reviewer{Client: serviceAccounts(), Audiences: []string{BrokerAudience}}
	mux := http.NewServeMux()
	NewLeaseServer(nil, rv, brokerSA).WithAccounts(leaser, owners).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const accountBody = `{"conversationId":"c1","account":"library"}`

func leaseAccount(t *testing.T, srv *httptest.Server, token, body string) (*http.Response, string) {
	t.Helper()
	resp := do(t, http.MethodPost, srv.URL+leasePath, token, body)
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestAnAccountLeaseIsMadeForTheConversationsOwnerNotTheCaller(t *testing.T) {
	leaser := &fakeLeaser{lease: goodLease()}
	srv := accountRig(t, fakeOwners{owner: doorPerson(), found: true}, leaser)
	resp, body := leaseAccount(t, srv, saGood, accountBody)
	var got leaseResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil || resp.StatusCode != http.StatusOK || got.Token != accountToken ||
		len(got.TokenHosts) != 1 || got.TokenHosts[0] != "api.example.org" || got.ExpiresAt == "" {
		t.Fatalf("%d %s %v", resp.StatusCode, body, err)
	}
	if leaser.principal != accountPerson || leaser.account != accountName {
		t.Fatalf("asked for %q %q", leaser.principal, leaser.account)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != noStore {
		t.Fatalf("Cache-Control %q", cc)
	}
}

func TestAnAccountLeaseRefusalsCarryTheirReasonAndNoToken(t *testing.T) {
	cases := []struct {
		name   string
		leaser *fakeLeaser
		want   int
		reason string
	}{
		{"not connected", &fakeLeaser{err: &identity.LeaseRefusal{Reason: identity.LeaseNoGrant}}, http.StatusNotFound, "NO_GRANT"},
		{"needs reconnect", &fakeLeaser{err: &identity.LeaseRefusal{Reason: identity.LeaseNeedsReconnect}}, http.StatusConflict, "NEEDS_RECONNECT"},
		{"ended at the service", &fakeLeaser{err: &identity.LeaseRefusal{Reason: identity.LeaseExpired}}, http.StatusGone, "EXPIRED"},
		{"broker down", &fakeLeaser{err: identity.ErrConnectionCheck}, http.StatusServiceUnavailable, "UNAVAILABLE"},
		{"unknown error", &fakeLeaser{err: errors.New("boom " + accountToken)}, http.StatusServiceUnavailable, "UNAVAILABLE"},
		{"already ended", &fakeLeaser{lease: identity.AccountLease{Token: accountToken, ExpiresAt: time.Now().Add(-time.Minute), TokenHosts: []string{"h"}}},
			http.StatusGone, "EXPIRED"},
	}
	for _, c := range cases {
		srv := accountRig(t, fakeOwners{owner: doorPerson(), found: true}, c.leaser)
		resp, body := leaseAccount(t, srv, saGood, accountBody)
		var got leaseReason
		if err := json.Unmarshal([]byte(body), &got); err != nil || resp.StatusCode != c.want || got.Reason != c.reason ||
			strings.Contains(body, accountToken) {
			t.Errorf("%s: %d %s", c.name, resp.StatusCode, body)
		}
	}
}

func TestAnAccountLeaseFailsClosedWhenTheOwnerIsNotAConnectedPerson(t *testing.T) {
	cases := map[string]struct {
		owners OwnerReader
		want   int
	}{
		"no owner record":     {fakeOwners{}, http.StatusNotFound},
		"anonymous owner":     {fakeOwners{owner: bus.Principal{Kind: bus.PrincipalKindAnonymous}, found: true}, http.StatusNotFound},
		"a Kubernetes user":   {fakeOwners{owner: bus.Principal{Kind: bus.PrincipalKindKubernetes, ID: accountPerson}, found: true}, http.StatusNotFound},
		"a person with no id": {fakeOwners{owner: bus.Principal{Kind: bus.PrincipalKindSubscriber}, found: true}, http.StatusNotFound},
		"owner unreadable":    {fakeOwners{err: errors.New("nats down")}, http.StatusServiceUnavailable},
	}
	for name, c := range cases {
		leaser := &fakeLeaser{lease: goodLease()}
		srv := accountRig(t, c.owners, leaser)
		resp, body := leaseAccount(t, srv, saGood, accountBody)
		if resp.StatusCode != c.want || strings.Contains(body, accountToken) || leaser.principal != "" {
			t.Errorf("%s: %d %s (broker asked for %q)", name, resp.StatusCode, body, leaser.principal)
		}
	}
}

func TestAnAccountLeaseNeedsABrokerAndTheRightCaller(t *testing.T) {
	none := accountRig(t, nil, nil)
	if resp, _ := leaseAccount(t, none, saGood, accountBody); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no broker: %d", resp.StatusCode)
	}
	srv := accountRig(t, fakeOwners{owner: doorPerson(), found: true}, &fakeLeaser{lease: goodLease()})
	for name, token := range map[string]string{"another ServiceAccount": saOtherSA, "no audience": saNoAudience, "missing": ""} {
		if resp, body := leaseAccount(t, srv, token, accountBody); resp.StatusCode < 400 || strings.Contains(body, accountToken) {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
}

func TestAnAccountLeaseRequestIsValidated(t *testing.T) {
	srv := accountRig(t, fakeOwners{owner: doorPerson(), found: true}, &fakeLeaser{lease: goodLease()})
	for name, body := range map[string]string{
		"bad conversation": `{"conversationId":"a.b","account":"library"}`,
		"bad account":      `{"conversationId":"c1","account":"Library!"}`,
		"long account":     `{"conversationId":"c1","account":"` + strings.Repeat("a", 64) + `"}`,
		"both kinds":       `{"conversationId":"c1","account":"library","verifier":"oreilly"}`,
		"unknown field":    `{"conversationId":"c1","account":"library","crew":"other"}`,
	} {
		if resp, _ := leaseAccount(t, srv, saGood, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
}

func TestAnAccountLeaseNeverLogsTheTokenOrTheErrorText(t *testing.T) {
	logs := captureLogs(t)
	srv := accountRig(t, fakeOwners{owner: doorPerson(), found: true}, &fakeLeaser{lease: goodLease()})
	leaseAccount(t, srv, saGood, accountBody)
	failing := accountRig(t, fakeOwners{owner: doorPerson(), found: true}, &fakeLeaser{err: identity.ErrConnectionCheck})
	leaseAccount(t, failing, saGood, accountBody)
	if strings.Contains(logs(), accountToken) || strings.Contains(logs(), saGood) {
		t.Fatalf("a token is in the logs: %s", logs())
	}
}

func TestAnAccountLeaseRefusesACallerThatNamesThePerson(t *testing.T) {
	srv := accountRig(t, fakeOwners{owner: doorPerson(), found: true}, &fakeLeaser{lease: goodLease()})
	body := `{"conversationId":"c1","account":"library","principalId":"someone-else"}`
	if resp, _ := leaseAccount(t, srv, saGood, body); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a claimed principal: %d", resp.StatusCode)
	}
}

func TestTheOwnerReaderFindsTheOwnerRecordOfThisCrewsConversations(t *testing.T) {
	nc, js := jetStreamServer(t)
	o := newOwnedSetupOn(t, nc, js, false)
	value, _ := json.Marshal(bus.ConversationOwner{Owner: doorPerson()})
	if _, err := o.kv.Put(context.Background(), bus.ConversationOwnerKey("ns", "pilot", "c1"), value); err != nil {
		t.Fatal(err)
	}
	owners, err := NewHandler(nc, "ns").ConversationOwners("pilot")
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := owners.Owner(context.Background(), "c1")
	if err != nil || !found || got.ID != accountPerson {
		t.Fatalf("%v %v %v", got, found, err)
	}
	if _, found, err := owners.Owner(context.Background(), "nobody"); found || err != nil {
		t.Errorf("a conversation with no record: %v %v", found, err)
	}
	other, _ := NewHandler(nc, "ns").ConversationOwners("another")
	if _, found, _ := other.Owner(context.Background(), "c1"); found {
		t.Error("another crew's reader must not see this crew's conversation")
	}
	if _, err := NewHandler(nc, "ns").ConversationOwners("bad.name"); err == nil {
		t.Error("a crew name that is not one token must be refused")
	}
}
