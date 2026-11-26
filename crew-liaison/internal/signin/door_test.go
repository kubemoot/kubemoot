package signin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

func TestADoorTokenResolvesToThePersonAndTheirKeptToken(t *testing.T) {
	r := newRig(t, nil)
	_, tok := r.signedIn()
	caller, kept, err := r.resolve(tok.Access)
	if err != nil || caller.Principal.ID != personAnn || kept != accessToken {
		t.Fatalf("resolve: %v %q %v", caller.Principal, kept, err)
	}
	// What is in the store is sealed: neither token can be read from it.
	raw, err := r.store.Get(t.Context(), "account.upstream."+personAnn)
	if err != nil || strings.Contains(string(raw), accessToken) || strings.Contains(string(raw), refreshToken) {
		t.Errorf("the kept record is readable: %v", err)
	}
}

func TestTheDoorAcceptsOnlyItsOwnTokens(t *testing.T) {
	r := newRig(t, nil)
	clientID, tok := r.signedIn()

	// The upstream's own tokens, and every other sealed value, are not door tokens.
	for name, bearer := range map[string]string{
		"upstream access": accessToken, "upstream refresh": refreshToken, "garbage": "nope",
		"door refresh": tok.Refresh, "client id": clientID, "tampered": flip(tok.Access), "empty": "",
	} {
		if _, kept, err := r.resolve(bearer); !errors.Is(err, identity.ErrNoCredential) || kept != "" {
			t.Errorf("%s: %q %v", name, kept, err)
		}
	}

	// A token another door issued is refused even when both doors share sealing keys.
	other := r.cfg
	other.PublicURL = "https://other-door.example.test"
	f, err := New(&other)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.(*front).issue(t.Context(), "sid-elsewhere", session{Subject: personAnn, ClientID: clientID})
	if err != nil {
		t.Fatal(err)
	}
	if _, kept, err := r.resolve(foreign.AccessToken); !errors.Is(err, identity.ErrInvalid) || kept != "" {
		t.Errorf("a token for another door: %q %v", kept, err)
	}

	// An expired door token is refused.
	r.now = r.now.Add(accessLifetime + time.Second)
	if _, kept, err := r.resolve(tok.Access); !errors.Is(err, identity.ErrInvalid) || kept != "" {
		t.Errorf("an expired door token: %q %v", kept, err)
	}
}

func TestEachPersonGetsOnlyTheirOwnKeptToken(t *testing.T) {
	r := newRig(t, nil)
	_, ann := r.signedIn()
	r.up.signsIn(bobAccess, bobRefresh)
	_, bob := r.signedIn()

	if caller, kept, err := r.resolve(ann.Access); err != nil || caller.Principal.ID != personAnn || kept != accessToken {
		t.Errorf("ann: %v %q %v", caller.Principal, kept, err)
	}
	if caller, kept, err := r.resolve(bob.Access); err != nil || caller.Principal.ID != personBob || kept != bobAccess {
		t.Errorf("bob: %v %q %v", caller.Principal, kept, err)
	}

	// A record copied under another person's key does not open: it was sealed for its own.
	raw, _ := r.store.Get(t.Context(), "account.upstream."+personBob)
	if err := r.store.Put(t.Context(), "account.upstream."+personAnn, raw, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, kept, err := r.resolve(ann.Access); !errors.Is(err, identity.ErrInvalid) || kept != "" {
		t.Errorf("ann with bob's record: %q %v", kept, err)
	}
}

func TestAKeptTokenThatNamesAnotherPersonIsRefused(t *testing.T) {
	r := newRig(t, nil)
	_, tok := r.signedIn()
	r.knows(accessToken, personBob)
	if _, kept, err := r.resolve(tok.Access); !errors.Is(err, identity.ErrInvalid) || kept != "" {
		t.Errorf("a kept token naming someone else: %q %v", kept, err)
	}
}

func TestTheKeptTokenIsRenewedWhenItIsDue(t *testing.T) {
	r := newRig(t, nil)
	clientID, tok := r.signedIn()
	// The upstream's access token lasts 900 seconds; the door's is renewed by the app.
	r.now = r.now.Add(14*time.Minute + 30*time.Second)
	var next doorTokens
	decode(t, r.postToken(r.refreshForm(clientID, tok.Refresh)), &next)
	if _, kept, err := r.resolve(next.Access); err != nil || kept != rotatedAccess {
		t.Fatalf("after the upstream token was due: %q %v", kept, err)
	}
	sent := r.up.sent()
	last := sent[len(sent)-1]
	if last.Get(paramGrantType) != grantRefresh || last.Get(grantRefresh) != refreshToken || last.Get(paramClientID) != upstreamClientID {
		t.Errorf("the renewal the upstream got: %v", last)
	}
	// Renewed once: the next request uses the kept token.
	if _, kept, err := r.resolve(next.Access); err != nil || kept != rotatedAccess || len(r.up.sent()) != len(sent) {
		t.Errorf("a second request renewed again: %q %v", kept, err)
	}
}

func TestConcurrentRequestsShareOneRenewal(t *testing.T) {
	r := newRig(t, nil)
	clientID, tok := r.signedIn()
	r.now = r.now.Add(14*time.Minute + 30*time.Second)
	var next doorTokens
	decode(t, r.postToken(r.refreshForm(clientID, tok.Refresh)), &next)
	before := len(r.up.sent())
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, kept, err := r.resolve(next.Access); err != nil || kept != rotatedAccess {
				t.Errorf("a concurrent request: %q %v", kept, err)
			}
		})
	}
	wg.Wait()
	// The fake upstream renews a refresh token once, so a second renewal would have failed.
	if n := len(r.up.sent()) - before; n != 1 {
		t.Errorf("the upstream was asked to renew %d times", n)
	}
}

func TestATokenTheUpstreamStopsAcceptingIsRenewedOrDeleted(t *testing.T) {
	r := newRig(t, nil)
	_, tok := r.signedIn()
	// The upstream revoked the access token early; the refresh token still works.
	r.knows(accessToken, "")
	if _, kept, err := r.resolve(tok.Access); err != nil || kept != rotatedAccess {
		t.Fatalf("renewal after an early refusal: %q %v", kept, err)
	}
	// Now the upstream refuses that one too and will not renew: the record is deleted.
	r.knows(rotatedAccess, "")
	if _, kept, err := r.resolve(tok.Access); !errors.Is(err, identity.ErrInvalid) || kept != "" {
		t.Fatalf("a token the upstream will not renew: %q %v", kept, err)
	}
	if _, err := r.store.Get(t.Context(), "account.upstream."+personAnn); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("the refused token is still kept: %v", err)
	}
}

func TestAnUpstreamThatCannotBeAskedDeletesNothing(t *testing.T) {
	r := newRig(t, nil)
	_, tok := r.signedIn()
	r.now = r.now.Add(14 * time.Minute)
	r.up.srv.Close()
	_, kept, err := r.resolve(tok.Access)
	if err == nil || errors.Is(err, identity.ErrInvalid) || errors.Is(err, identity.ErrNoCredential) || kept != "" {
		t.Fatalf("upstream down should be a failed check, got %q %v", kept, err)
	}
	if _, err := r.store.Get(t.Context(), "account.upstream."+personAnn); err != nil {
		t.Errorf("the kept token was deleted because the upstream was down: %v", err)
	}
}

func TestAKeptTokenLivesThirtyDaysIdleAndUseStartsItAgain(t *testing.T) {
	r := newRig(t, func(c *Config) { c.UpstreamTokenLifetime = 0 })
	r.up.answer = `"token_type":"Bearer"` // no expiry stated: only the idle time applies
	clientID, tok := r.signedIn()
	refresh := tok.Refresh
	use := func() error {
		var next doorTokens
		decode(t, r.postToken(r.refreshForm(clientID, refresh)), &next)
		refresh = next.Refresh
		_, _, err := r.resolve(next.Access)
		return err
	}
	// Used every 20 days, the sign-in outlives 30 days.
	for range 3 {
		r.now = r.now.Add(20 * 24 * time.Hour)
		if err := use(); err != nil {
			t.Fatalf("a sign-in in use was dropped: %v", err)
		}
	}
	// Left alone for 30 days, the session and the kept token are both gone.
	r.now = r.now.Add(idleLifetime + time.Minute)
	assertOAuthError(t, r.postToken(r.refreshForm(clientID, refresh)), http.StatusBadRequest, errInvalidGrant)
	if _, err := r.store.Get(t.Context(), "account.upstream."+personAnn); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("the idle token is still kept: %v", err)
	}
}

func TestAKeptTokenNeverOutlivesTheUpstreamsLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		configured time.Duration
		answer     string
		limit      time.Duration
	}{
		"the upstream states it":      {0, `"expires_in":900,"refresh_expires_in":86400`, 24 * time.Hour},
		"the other name for it":       {0, `"expires_in":900,"refresh_token_expires_in":3600`, time.Hour},
		"the install configures it":   {48 * time.Hour, `"expires_in":900`, 48 * time.Hour},
		"the answer beats the config": {48 * time.Hour, `"expires_in":900,"refresh_expires_in":7200`, 2 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, func(c *Config) { c.UpstreamTokenLifetime = tc.configured })
			r.up.answer = tc.answer
			r.signedIn()
			key := "account.upstream." + personAnn
			r.now = r.now.Add(tc.limit - time.Minute)
			if _, err := r.store.Get(t.Context(), key); err != nil {
				t.Fatalf("dropped before the limit: %v", err)
			}
			r.now = r.now.Add(2 * time.Minute)
			if _, err := r.store.Get(t.Context(), key); !errors.Is(err, tokenstore.ErrNotFound) {
				t.Errorf("kept past the upstream's limit: %v", err)
			}
		})
	}
}

func TestWithNoRefreshTokenTheKeptTokenEndsWithTheAccessToken(t *testing.T) {
	r := newRig(t, nil)
	r.up.signsIn(accessToken, "")
	_, tok := r.signedIn()
	if _, kept, err := r.resolve(tok.Access); err != nil || kept != accessToken {
		t.Fatalf("resolve: %q %v", kept, err)
	}
	r.now = r.now.Add(901 * time.Second)
	if _, err := r.store.Get(t.Context(), "account.upstream."+personAnn); !errors.Is(err, tokenstore.ErrNotFound) {
		t.Errorf("kept past the access token's end: %v", err)
	}
}

func TestARefreshTokenWorksOnce(t *testing.T) {
	r := newRig(t, nil)
	clientID, first := r.signedIn()

	var second doorTokens
	resp := r.postToken(r.refreshForm(clientID, first.Refresh))
	decode(t, resp, &second)
	if resp.StatusCode != http.StatusOK || second.Access == "" || second.Refresh == first.Refresh || second.Access == first.Access {
		t.Fatalf("refresh: %d %+v", resp.StatusCode, second)
	}
	if n := len(r.up.sent()); n != 1 {
		t.Errorf("a door refresh asked the upstream (%d calls)", n)
	}
	// The first refresh token is spent. Presenting it again ends the session, so the
	// second one stops working too: someone is replaying a token.
	assertOAuthError(t, r.postToken(r.refreshForm(clientID, first.Refresh)), http.StatusBadRequest, errInvalidGrant)
	assertOAuthError(t, r.postToken(r.refreshForm(clientID, second.Refresh)), http.StatusBadRequest, errInvalidGrant)
}

func TestRefreshRefusals(t *testing.T) {
	r := newRig(t, nil)
	clientID, tok := r.signedIn()
	other := r.registerClient()
	assertOAuthError(t, r.postToken(r.refreshForm(clientID, refreshToken)), http.StatusBadRequest, errInvalidGrant)
	assertOAuthError(t, r.postToken(r.refreshForm(clientID, tok.Access)), http.StatusBadRequest, errInvalidGrant)
	assertOAuthError(t, r.postToken(r.refreshForm(other, tok.Refresh)), http.StatusBadRequest, errInvalidGrant)
	assertOAuthError(t, r.postToken(r.refreshForm("nope", tok.Refresh)), http.StatusUnauthorized, "invalid_client")
	form := r.refreshForm(clientID, tok.Refresh)
	form.Set(paramResource, "https://api.example.test/")
	assertOAuthError(t, r.postToken(form), http.StatusBadRequest, errInvalidTarget)
	// None of those spent the refresh token.
	if resp := r.postToken(r.refreshForm(clientID, tok.Refresh)); resp.StatusCode != http.StatusOK {
		t.Errorf("the refresh token after refused attempts: %d", resp.StatusCode)
	}
}

func TestARefreshWithNoKeptTokenEndsTheSession(t *testing.T) {
	r := newRig(t, nil)
	clientID, tok := r.signedIn()
	if err := r.store.Delete(t.Context(), "account.upstream."+personAnn); err != nil {
		t.Fatal(err)
	}
	assertOAuthError(t, r.postToken(r.refreshForm(clientID, tok.Refresh)), http.StatusBadRequest, errInvalidGrant)
	if _, _, err := r.resolve(tok.Access); !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("a door token with no kept token behind it: %v", err)
	}
}

func (r *rig) postRevoke(clientID, token string) *http.Response {
	r.t.Helper()
	form := url.Values{paramClientID: {clientID}, paramToken: {token}}
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+pathRevoke, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r.do(req)
}

func TestSigningOutDeletesTheKeptTokenAtOnce(t *testing.T) {
	for name, pick := range map[string]func(doorTokens) string{
		"with the refresh token": func(d doorTokens) string { return d.Refresh },
		"with the access token":  func(d doorTokens) string { return d.Access },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			clientID, tok := r.signedIn()
			if resp := r.postRevoke(clientID, pick(tok)); resp.StatusCode != http.StatusOK {
				t.Fatalf("revoke: %d", resp.StatusCode)
			}
			if _, err := r.store.Get(t.Context(), "account.upstream."+personAnn); !errors.Is(err, tokenstore.ErrNotFound) {
				t.Errorf("the kept token survived the sign-out: %v", err)
			}
			if _, _, err := r.resolve(tok.Access); !errors.Is(err, identity.ErrInvalid) {
				t.Errorf("the door token after sign-out: %v", err)
			}
			assertOAuthError(t, r.postToken(r.refreshForm(clientID, tok.Refresh)), http.StatusBadRequest, errInvalidGrant)
		})
	}
}

func TestRevokeIgnoresWhatIsNotTheClientsOwnToken(t *testing.T) {
	r := newRig(t, nil)
	clientID, tok := r.signedIn()
	other := r.registerClient()
	for name, resp := range map[string]*http.Response{
		"another client": r.postRevoke(other, tok.Refresh),
		"garbage":        r.postRevoke(clientID, "nope"),
		"the upstream's": r.postRevoke(clientID, accessToken),
	} {
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if _, kept, err := r.resolve(tok.Access); err != nil || kept != accessToken {
		t.Errorf("a revoke that should have done nothing signed the person out: %q %v", kept, err)
	}
}

// downStore is a store that cannot be reached.
type downStore struct{}

var errStoreDown = errors.New("store down")

func (downStore) Put(context.Context, string, []byte, time.Duration) error    { return errStoreDown }
func (downStore) Create(context.Context, string, []byte, time.Duration) error { return errStoreDown }
func (downStore) Get(context.Context, string) ([]byte, error)                 { return nil, errStoreDown }
func (downStore) Delete(context.Context, string) error                        { return errStoreDown }

func TestAStoreThatCannotBeAskedIsNeverAnAcceptance(t *testing.T) {
	r := newRig(t, nil)
	clientID, tok := r.signedIn()
	broken := r.cfg
	broken.Store = downStore{}
	f, err := New(&broken)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, pathDoor, nil)
	_, kept, err := f.Resolve(req, tok.Access)
	if !errors.Is(err, errStoreDown) || kept != "" {
		t.Errorf("resolve with the store down: %q %v", kept, err)
	}
	rec := newRecorder()
	post, _ := http.NewRequest(http.MethodPost, pathToken, strings.NewReader(r.refreshForm(clientID, tok.Refresh).Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.Handler().ServeHTTP(rec, post)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "store down") {
		t.Errorf("refresh with the store down: %d %s", rec.Code, rec.Body.String())
	}
}
