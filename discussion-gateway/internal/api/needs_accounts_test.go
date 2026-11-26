package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

const startedStamp = "2026-11-26T10:00:00Z"

func TestAccountsSinceKeepsOnlyRefusalsFromTheTurnOn(t *testing.T) {
	rec, _ := json.Marshal(bus.NeedsAccounts{Refused: map[string]string{
		"before": "2026-11-26T09:59:59.9Z",
		"same":   "2026-11-26T10:00:00Z",
		"sameS":  "2026-11-26T10:00:00.7Z",
		"after":  "2026-11-26T10:05:00.5Z",
		"junk":   "not a time",
	}})
	if got := accountsSince(rec, startedStamp); !reflect.DeepEqual(got, []string{"after", "same", "sameS"}) {
		t.Fatalf("accounts: %v", got)
	}
	for name, c := range map[string]struct {
		value   string
		started string
	}{
		"unreadable record": {"{", startedStamp},
		"no start":          {string(rec), ""},
		"empty record":      {"{}", startedStamp},
	} {
		if got := accountsSince([]byte(c.value), c.started); len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// noteAs records, as the lease path does, a refusal at the given time.
func (c controlSetup) noteAs(t *testing.T, account string, at time.Time) {
	t.Helper()
	key := bus.ConversationNeedsKey("ns", "pilot", convCtl)
	if retry, err := noteAccount(context.Background(), c.kv, key, account, at); retry || err != nil {
		t.Fatalf("note: %v %v", retry, err)
	}
}

func TestTheStateNamesTheAccountsARunningTurnWasRefused(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, true)
	if _, state := c.stateOf(t, convCtl); len(state.Turns) != 1 || len(state.Turns[0].NeedsAccounts) != 0 {
		t.Fatalf("before any refusal: %+v", state.Turns)
	}
	c.noteAs(t, "mail", time.Now().Add(time.Second))
	c.noteAs(t, accountName, time.Now().Add(2*time.Second))
	c.noteAs(t, accountName, time.Now().Add(3*time.Second))
	_, state := c.stateOf(t, convCtl)
	if got := state.Turns[0].NeedsAccounts; !reflect.DeepEqual(got, []string{accountName, "mail"}) {
		t.Fatalf("needsAccounts: %v", got)
	}
}

func TestARefusalBeforeTheLatestTurnBeganIsIgnored(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, false)
	c.noteAs(t, accountName, time.Now().Add(-time.Hour))
	if _, state := c.stateOf(t, convCtl); len(state.Turns[0].NeedsAccounts) != 0 {
		t.Fatalf("a refusal of an earlier turn must not show: %+v", state.Turns[0])
	}
}

func TestADeletedConversationLosesItsRefusalNote(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, false)
	c.noteAs(t, accountName, time.Now())
	resp := do(t, http.MethodDelete, c.conversationPath(convCtl, ""), tokenAlice, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, err := c.kv.Get(context.Background(), bus.ConversationNeedsKey("ns", "pilot", convCtl)); err == nil {
		t.Fatal("the note must go with the conversation")
	}
}

type fakeNoter struct {
	conv, account string
	calls         int
	err           error
}

func (f *fakeNoter) NeedsAccount(_ context.Context, conv, account string) error {
	f.calls++
	f.conv, f.account = conv, account
	return f.err
}

type noterOwners struct {
	fakeOwners
	*fakeNoter
}

func TestARefusedAccountLeaseNotesOnlyWhatThePersonCanFix(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		noted bool
	}{
		{"not connected", &identity.LeaseRefusal{Reason: identity.LeaseNoGrant}, true},
		{"needs reconnect", &identity.LeaseRefusal{Reason: identity.LeaseNeedsReconnect}, true},
		{"ended at the service", &identity.LeaseRefusal{Reason: identity.LeaseExpired}, false},
		{"broker down", identity.ErrConnectionCheck, false},
		{"unknown error", errors.New("boom"), false},
	}
	for _, c := range cases {
		noter := &fakeNoter{}
		owners := noterOwners{fakeOwners{owner: doorPerson(), found: true}, noter}
		srv := accountRig(t, owners, &fakeLeaser{err: c.err})
		resp, body := leaseAccount(t, srv, saGood, accountBody)
		if resp.StatusCode == http.StatusOK || strings.Contains(body, accountToken) {
			t.Fatalf("%s: %d %s", c.name, resp.StatusCode, body)
		}
		if (noter.calls == 1) != c.noted || (c.noted && (noter.conv != "c1" || noter.account != accountName)) {
			t.Errorf("%s: noted %d times as %q %q", c.name, noter.calls, noter.conv, noter.account)
		}
	}
}

func TestALeaseForAnotherPersonOrAGoodLeaseNotesNothing(t *testing.T) {
	for name, c := range map[string]struct {
		owners fakeOwners
		leaser *fakeLeaser
	}{
		"not the owner":  {fakeOwners{owner: doorPerson(), found: true}, nil},
		"a granted call": {fakeOwners{owner: doorPerson(), found: true}, &fakeLeaser{lease: goodLease()}},
	} {
		noter := &fakeNoter{}
		owners := noterOwners{c.owners, noter}
		var leaser identity.AccountLeaser = c.leaser
		body := accountBody
		if c.leaser == nil {
			leaser = &fakeLeaser{err: &identity.LeaseRefusal{Reason: identity.LeaseNoGrant}}
			body = strings.Replace(accountBody, accountPerson, "someone-else", 1)
		}
		srv := accountRig(t, owners, leaser)
		leaseAccount(t, srv, saGood, body)
		if noter.calls != 0 {
			t.Errorf("%s: noted %d times", name, noter.calls)
		}
	}
}

func TestANoteThatFailsDoesNotChangeTheRefusal(t *testing.T) {
	noter := &fakeNoter{err: errors.New("nats down")}
	owners := noterOwners{fakeOwners{owner: doorPerson(), found: true}, noter}
	srv := accountRig(t, owners, &fakeLeaser{err: &identity.LeaseRefusal{Reason: identity.LeaseNoGrant}})
	resp, body := leaseAccount(t, srv, saGood, accountBody)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "NO_GRANT") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestTheNoteKeepsAtMostSixteenAccountsAndDropsTheOldest(t *testing.T) {
	c := newControlSetup(t)
	c.seedAnswered(t, true)
	base := time.Now()
	for i := 0; i < maxNotedAccounts+4; i++ {
		c.noteAs(t, fmt.Sprintf("acct-%02d", i), base.Add(time.Duration(i)*time.Second))
	}
	entry, err := c.kv.Get(context.Background(), bus.ConversationNeedsKey("ns", "pilot", convCtl))
	if err != nil {
		t.Fatal(err)
	}
	var rec bus.NeedsAccounts
	if err := json.Unmarshal(entry.Value(), &rec); err != nil || len(rec.Refused) != maxNotedAccounts {
		t.Fatalf("kept %d: %v", len(rec.Refused), err)
	}
	for _, gone := range []string{"acct-00", "acct-03"} {
		if _, kept := rec.Refused[gone]; kept {
			t.Errorf("%s is among the oldest and must go", gone)
		}
	}
	if _, kept := rec.Refused["acct-19"]; !kept {
		t.Error("the newest must stay")
	}
}
