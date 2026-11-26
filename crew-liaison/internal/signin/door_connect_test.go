package signin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

func (r *connectRig) kept(user string) (accounts.Token, error) {
	tok, _, err := r.vault.Get(context.Background(), r.svc.PrincipalID(user), acctLibrary)
	return tok, err
}

func TestConnectFlowKeepsTheTokensAndShowsNoneOfThem(t *testing.T) {
	r := newConnectRig(t)
	b := newBrowser(t, r.srv.URL)
	page := r.openLink(b, r.linkPath(userJane, acctLibrary), janeClaims)
	text := body(t, page)
	assertConsentPage(t, page.StatusCode, text)
	if _, err := r.kept(userJane); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("opening the page must keep nothing")
	}
	toService := location(t, r.press(b, text))
	assertUpstreamRequest(t, r, toService)
	done := r.serviceReturn(b, toService, nil)
	result := body(t, done)
	if done.StatusCode != http.StatusOK || !strings.Contains(result, "Connected") {
		t.Fatalf("the result: %d %s", done.StatusCode, result)
	}
	assertKeptAfterConnect(t, r)
	for _, leak := range []string{upstreamAccess, upstreamRefresh, connectUpCode} {
		if strings.Contains(result, leak) || strings.Contains(r.logText(), leak) {
			t.Errorf("%q appears in a page or a log", leak)
		}
	}
	if done.Header.Get("Cache-Control") != noStore || done.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("the result page headers: %v", done.Header)
	}
}

func assertConsentPage(t *testing.T, status int, text string) {
	t.Helper()
	for _, want := range []string{"Example Library", libraryCrew, "30 days", "Search the library"} {
		if status != http.StatusOK || !strings.Contains(text, want) {
			t.Fatalf("the consent page lacks %q: %d %s", want, status, text)
		}
	}
}

func assertUpstreamRequest(t *testing.T, r *connectRig, to *url.URL) {
	t.Helper()
	q := to.Query()
	want := map[string]string{
		"client_id": installClient, "redirect_uri": doorPublicURL + pathCallback, "code_challenge_method": methodS256,
		paramScopeKey: "search offline_access", "resource": "",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("upstream %s = %q, want %q", k, q.Get(k), v)
		}
	}
	if !strings.HasPrefix(to.String(), r.up.srv.URL+"/authorize") || q.Get("code_challenge") == "" || q.Get("state") == "" {
		t.Fatalf("the upstream request: %s", to)
	}
}

func assertKeptAfterConnect(t *testing.T, r *connectRig) {
	t.Helper()
	tok, err := r.kept(userJane)
	if err != nil || tok.Access != upstreamAccess || tok.Refresh != upstreamRefresh || len(tok.Crews) != 1 || tok.KeepUntil.IsZero() {
		t.Fatalf("the kept token: %+v %v", tok, err)
	}
	if r.up.exchanged.Get("code_verifier") == "" || r.up.exchanged.Get("redirect_uri") != doorPublicURL+pathCallback {
		t.Fatalf("the exchange: %v", r.up.exchanged)
	}
}

func TestAnotherPersonOpeningTheLinkIsRefused(t *testing.T) {
	r := newConnectRig(t)
	b := newBrowser(t, r.srv.URL)
	page := r.openLink(b, r.linkPath(userJane, acctLibrary), bobClaims)
	if page.StatusCode != http.StatusForbidden || !strings.Contains(body(t, page), "another person") {
		t.Fatalf("bob opening jane's link: %d", page.StatusCode)
	}
	if _, err := r.kept(otherUser); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("nothing may be kept for bob")
	}
	if !strings.Contains(r.logText(), "another person") {
		t.Error("the refusal must be logged")
	}
}

func TestBadLinksAreRefused(t *testing.T) {
	r := newConnectRig(t)
	good := r.linkPath(userJane, acctLibrary)
	cases := map[string]string{
		"tampered":        good + "x",
		"no handle":       pathConnect + acctLibrary,
		"another account": strings.Replace(good, acctLibrary, "mail", 1),
	}
	for name, path := range cases {
		b := newBrowser(t, r.srv.URL)
		if page := r.openLink(b, path, janeClaims); page.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, page.StatusCode)
		}
	}
	b := newBrowser(t, r.srv.URL)
	expired := r.linkPath(userJane, acctLibrary)
	r.advance(handleLifetime + time.Minute)
	if page := r.openLink(b, expired, janeClaims); page.StatusCode != http.StatusBadRequest {
		t.Errorf("expired: %d", page.StatusCode)
	}
}

func TestAnAccountNobodyOffersIsNotAvailable(t *testing.T) {
	r := newConnectRig(t)
	b := newBrowser(t, r.srv.URL)
	path := r.linkPath(userJane, "mail")
	if page := r.openLink(b, path, janeClaims); page.StatusCode != http.StatusNotFound {
		t.Fatalf("an account that is not offered: %d", page.StatusCode)
	}
}

func TestConnectPostRefusals(t *testing.T) {
	r := newConnectRig(t)
	cases := map[string]struct {
		mutate  func(url.Values)
		headers map[string]string
		want    int
	}{
		"no csrf":        {func(f url.Values) { f.Del("csrf") }, nil, http.StatusForbidden},
		"another csrf":   {func(f url.Values) { f.Set("csrf", "x") }, nil, http.StatusForbidden},
		"another handle": {func(f url.Values) { f.Set("req", f.Get("req")+"x") }, nil, http.StatusForbidden},
		"from elsewhere": {nil, map[string]string{fetchSite: crossSite}, http.StatusForbidden},
		"wrong origin":   {nil, map[string]string{"Origin": evilOrigin}, http.StatusForbidden},
	}
	for name, c := range cases {
		b := newBrowser(t, r.srv.URL)
		page := r.openLink(b, r.linkPath(userJane, acctLibrary), janeClaims)
		form := fieldsOf(t, body(t, page))
		if c.mutate != nil {
			c.mutate(form)
		}
		if resp := b.do(http.MethodPost, pathConnect+acctLibrary, form, c.headers); resp.StatusCode != c.want {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	// No session at all: a post is refused, never sent to sign in.
	if resp := newBrowser(t, r.srv.URL).do(http.MethodPost, pathConnect+acctLibrary, url.Values{"req": {"x"}}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a post without a session: %d", resp.StatusCode)
	}
}

func TestALinkStartsOneConnection(t *testing.T) {
	r := newConnectRig(t)
	b := newBrowser(t, r.srv.URL)
	page := r.openLink(b, r.linkPath(userJane, acctLibrary), janeClaims)
	text := body(t, page)
	if first := r.press(b, text); first.StatusCode != http.StatusFound {
		t.Fatalf("first: %d", first.StatusCode)
	}
	if second := r.press(b, text); second.StatusCode != http.StatusBadRequest {
		t.Fatalf("the same link again: %d", second.StatusCode)
	}
}

func TestCallbackRefusals(t *testing.T) {
	r := newConnectRig(t)
	start := func() (*browser, *url.URL) {
		b := newBrowser(t, r.srv.URL)
		page := r.openLink(b, r.linkPath(userJane, acctLibrary), janeClaims)
		return b, location(t, r.press(b, body(t, page)))
	}
	b, to := start()
	if resp := r.serviceReturn(newBrowser(t, r.srv.URL), to, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a callback from another browser: %d", resp.StatusCode)
	}
	if resp := r.serviceReturn(b, to, url.Values{paramIss: {evilOrigin}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a callback from another issuer: %d", resp.StatusCode)
	}
	b, to = start()
	if resp := b.get(pathCallback + "?" + url.Values{paramCode: {connectUpCode}, paramState: {to.Query().Get(paramState) + "x"}}.Encode()); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a tampered state: %d", resp.StatusCode)
	}
	if _, err := r.kept(userJane); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("a refused callback must keep nothing")
	}
}

func TestCallbackByADifferentPersonIsRefused(t *testing.T) {
	r := newConnectRig(t)
	b := newBrowser(t, r.srv.URL)
	page := r.openLink(b, r.linkPath(userJane, acctLibrary), janeClaims)
	to := location(t, r.press(b, body(t, page)))
	// The same browser, now signed in as bob, receives jane's return.
	other := newBrowser(t, r.srv.URL)
	for _, c := range b.cookies {
		if strings.HasPrefix(c.Name, stateCookiePrefix) {
			other.cookies[c.Name] = c
		}
	}
	signIn := r.openLink(other, r.linkPath(otherUser, acctLibrary), bobClaims)
	if signIn.StatusCode != http.StatusOK {
		t.Fatalf("bob's own link: %d", signIn.StatusCode)
	}
	if resp := r.serviceReturn(other, to, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("jane's return in bob's session: %d", resp.StatusCode)
	}
	if _, err := r.kept(userJane); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("nothing may be kept for jane")
	}
}

func TestTheServiceRefusingOrFailingKeepsNothing(t *testing.T) {
	r := newConnectRig(t)
	b := newBrowser(t, r.srv.URL)
	page := r.openLink(b, r.linkPath(userJane, acctLibrary), janeClaims)
	to := location(t, r.press(b, body(t, page)))
	resp := r.serviceReturn(b, to, url.Values{"error": {errAccessDenied}, paramCode: {""}})
	if text := body(t, resp); !strings.Contains(text, "Not connected") || strings.Contains(text, errAccessDenied) {
		t.Fatalf("the person declined at the service: %s", text)
	}
	r.up.tokenFails = true
	b2 := newBrowser(t, r.srv.URL)
	page2 := r.openLink(b2, r.linkPath(userJane, acctLibrary), janeClaims)
	to2 := location(t, r.press(b2, body(t, page2)))
	if resp := r.serviceReturn(b2, to2, nil); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("the service failing the exchange: %d", resp.StatusCode)
	}
	if _, err := r.kept(userJane); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("nothing may be kept")
	}
}

func TestConnectIsOffWithoutAccounts(t *testing.T) {
	r := newDoorRig(t, nil)
	if _, err := r.door.ConnectURL("p", "a", "c"); !errors.Is(err, ErrConnectOff) {
		t.Fatalf("ConnectURL: %v", err)
	}
	b := newBrowser(t, r.srv.URL)
	for _, path := range []string{pathConnect + "x", pathAccounts} {
		if resp := b.get(path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	if resp := b.do(http.MethodPost, pathRevoke, url.Values{formToken: {"x"}}, nil); resp.StatusCode == http.StatusOK {
		t.Error("the revocation route must not exist when nothing is kept")
	}
}

func TestSafeReturn(t *testing.T) {
	for p, want := range map[string]bool{
		pathAccounts: true, pathConnect + "x?c=y": true, "//evil.example.test": false, evilOrigin: false,
		"/oauth/token": false, "/oauth/connect\\x": false, "": false, "/mcp": false,
	} {
		if safeReturn(p) != want {
			t.Errorf("%q", p)
		}
	}
}

func TestAccountsPageDisconnectAndRevocation(t *testing.T) {
	r := newConnectRig(t)
	b, done := r.connectAs(userJane, janeClaims)
	if done.StatusCode != http.StatusOK {
		t.Fatalf("connect: %d", done.StatusCode)
	}
	list := b.get(pathAccounts)
	text := body(t, list)
	if list.StatusCode != http.StatusOK || !strings.Contains(text, "Example Library") || !strings.Contains(text, "Connected") {
		t.Fatalf("the accounts page: %d %s", list.StatusCode, text)
	}
	if strings.Contains(text, upstreamAccess) || strings.Contains(text, upstreamRefresh) {
		t.Fatal("a token appears on the accounts page")
	}
	resp := b.do(http.MethodPost, pathDisconnect, fieldsOfForm(t, text, pathDisconnect), nil)
	if text := body(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(text, "told to revoke") {
		t.Fatalf("disconnect: %d %s", resp.StatusCode, text)
	}
	assertRevokedAndDeleted(t, r)
}

func assertRevokedAndDeleted(t *testing.T, r *connectRig) {
	t.Helper()
	if len(r.up.revoked) != 1 || r.up.revoked[0] != upstreamRefresh {
		t.Fatalf("revocation: %v", r.up.revoked)
	}
	if _, err := r.kept(userJane); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("the record must be deleted")
	}
}

func TestDisconnectDeletesEvenWhenRevocationFails(t *testing.T) {
	r := newConnectRig(t)
	r.up.revokeFails = true
	b, _ := r.connectAs(userJane, janeClaims)
	form := fieldsOfForm(t, body(t, b.get(pathAccounts)), pathDisconnect)
	resp := b.do(http.MethodPost, pathDisconnect, form, nil)
	if text := body(t, resp); !strings.Contains(text, "could not be reached") {
		t.Fatalf("the person must be told: %s", text)
	}
	if _, err := r.kept(userJane); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("the record must be deleted whether or not the service answered")
	}
}

func TestDisconnectRefusals(t *testing.T) {
	r := newConnectRig(t)
	b, _ := r.connectAs(userJane, janeClaims)
	form := fieldsOfForm(t, body(t, b.get(pathAccounts)), pathDisconnect)
	bad := url.Values{"account": {"mail"}, "csrf": {form.Get("csrf")}}
	if resp := b.do(http.MethodPost, pathDisconnect, bad, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a token made for another account: %d", resp.StatusCode)
	}
	if resp := b.do(http.MethodPost, pathDisconnect, form, map[string]string{fetchSite: crossSite}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("from another site: %d", resp.StatusCode)
	}
	if resp := newBrowser(t, r.srv.URL).do(http.MethodPost, pathDisconnect, form, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a session: %d", resp.StatusCode)
	}
	if _, err := r.kept(userJane); err != nil {
		t.Fatal("a refused disconnect must keep the record")
	}
}

func TestAccountsPageNeedsTheDoorSessionAndOffersConnect(t *testing.T) {
	r := newConnectRig(t)
	b := newBrowser(t, r.srv.URL)
	page := r.openLink(b, pathAccounts, janeClaims)
	text := body(t, page)
	if page.StatusCode != http.StatusOK || !strings.Contains(text, "Not connected") || !strings.Contains(text, pathConnect+acctLibrary+"?c=") {
		t.Fatalf("the page behind sign-in: %d %s", page.StatusCode, text)
	}
}

func TestSignOutDeletesEveryConnection(t *testing.T) {
	r := newConnectRig(t)
	b, _ := r.connectAs(userJane, janeClaims)
	text := body(t, b.get(pathAccounts))
	resp := b.do(http.MethodPost, pathSignOut, fieldsOfForm(t, text, pathSignOut), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), "Signed out") {
		t.Fatalf("sign out: %d", resp.StatusCode)
	}
	if _, err := r.kept(userJane); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("sign-out must delete the connections")
	}
	if resp := b.get(pathAccounts); resp.StatusCode != http.StatusFound {
		t.Fatalf("the session must be gone: %d", resp.StatusCode)
	}
	if resp := b.do(http.MethodPost, pathSignOut, url.Values{"csrf": {"x"}}, nil); resp.StatusCode == http.StatusOK {
		t.Fatal("a post with no session must not sign anyone out")
	}
}

func TestAppRevocationEndsTheSignInAndDeletesTheConnections(t *testing.T) {
	r := newConnectRig(t)
	_, done := r.connectAs(doorUser, janeShort)
	if done.StatusCode != http.StatusOK {
		t.Fatal("connect")
	}
	_, refresh := r.tokens()
	resp := r.postRevoke(url.Values{formToken: {refresh}, paramClientID: {doorClient}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if _, err := r.kept(doorUser); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("the connections must be deleted when the app signs out")
	}
	if again := r.postToken(r.refreshForm(refresh)); again.StatusCode != http.StatusBadRequest {
		t.Fatalf("a revoked sign-in refreshed: %d", again.StatusCode)
	}
	// A token that is not valid, or another app's, is answered 200 and changes nothing.
	_, _ = r.connectAs(doorUser, janeShort)
	for name, form := range map[string]url.Values{
		"garbage":     {formToken: {"nope"}, paramClientID: {doorClient}},
		"another app": {formToken: {refresh}, paramClientID: {"other"}},
	} {
		if resp := r.postRevoke(form); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if _, err := r.kept(doorUser); err != nil {
		t.Fatal("an invalid revocation must delete nothing")
	}
}

func (r *connectRig) postRevoke(form url.Values) *http.Response {
	r.t.Helper()
	return newBrowser(r.t, r.srv.URL).do(http.MethodPost, pathRevoke, form, nil)
}

func TestTheVaultKeepsNothingWhenItsStoreIsAway(t *testing.T) {
	r := newConnectRig(t)
	if _, err := r.vault.Save(context.Background(), "", acctLibrary, accounts.Token{}, tokenstore.MustNotExist); err == nil {
		t.Fatal("a record with no person")
	}
}

func TestMetadataAdvertisesRevocationOnlyWhenAccountsAreOn(t *testing.T) {
	on := newConnectRig(t)
	off := newDoorRig(t, nil)
	for name, c := range map[string]struct {
		srv  string
		want string
	}{"on": {on.srv.URL, doorPublicURL + pathRevoke}, "off": {off.srv.URL, ""}} {
		resp, err := http.Get(c.srv + pathAuthServer)
		if err != nil {
			t.Fatal(err)
		}
		var meta map[string]any
		decode(t, resp, &meta)
		got, _ := meta["revocation_endpoint"].(string)
		if got != c.want {
			t.Errorf("%s: revocation_endpoint = %q, want %q", name, got, c.want)
		}
	}
}

func TestAFailedDeletionIsNeverReportedAsSignedOut(t *testing.T) {
	r := newConnectRig(t)
	b, _ := r.connectAs(userJane, janeClaims)
	text := body(t, b.get(pathAccounts))
	r.store.failDeletes.Store(true)
	resp := b.do(http.MethodPost, pathSignOut, fieldsOfForm(t, text, pathSignOut), nil)
	if page := body(t, resp); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(page, "Try again") ||
		strings.Contains(page, "Signed out") {
		t.Fatalf("sign out with a failing store: %d %s", resp.StatusCode, page)
	}
	if resp := b.get(pathAccounts); resp.StatusCode != http.StatusOK {
		t.Fatalf("the session must stay so the person can try again: %d", resp.StatusCode)
	}
	r.store.failDeletes.Store(false)
	again := b.do(http.MethodPost, pathSignOut, fieldsOfForm(t, body(t, b.get(pathAccounts)), pathSignOut), nil)
	if again.StatusCode != http.StatusOK {
		t.Fatalf("the retry: %d", again.StatusCode)
	}
	if _, err := r.kept(userJane); !errors.Is(err, accounts.ErrNotConnected) {
		t.Fatal("the retry must delete")
	}
}

func TestAnAppRevocationThatCannotDeleteIsRetryable(t *testing.T) {
	r := newConnectRig(t)
	if done := func() *http.Response {
		_, d := r.connectAs(doorUser, janeShort)
		return d
	}(); done.StatusCode != http.StatusOK {
		t.Fatal("connect")
	}
	_, refresh := r.tokens()
	r.store.failDeletes.Store(true)
	resp := r.postRevoke(url.Values{formToken: {refresh}, paramClientID: {doorClient}})
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("revoke with a failing store: %d", resp.StatusCode)
	}
	if again := r.postToken(r.refreshForm(refresh)); again.StatusCode != http.StatusOK {
		t.Fatalf("a sign-in whose revocation failed is not ended: %d", again.StatusCode)
	}
}

var janeShort = map[string]any{claimSub: subJane, claimNameKey: "Jane"}

func TestAConnectReturnWorksOnce(t *testing.T) {
	r := newConnectRig(t)
	b := newBrowser(t, r.srv.URL)
	page := r.openLink(b, r.linkPath(userJane, acctLibrary), janeClaims)
	to := location(t, r.press(b, body(t, page)))
	// The binding cookie is cleared by the first return, so keep a copy to replay with.
	again := newBrowser(t, r.srv.URL)
	for _, c := range b.cookies {
		again.cookies[c.Name] = c
	}
	if first := r.serviceReturn(b, to, nil); first.StatusCode != http.StatusOK {
		t.Fatalf("first: %d", first.StatusCode)
	}
	if second := r.serviceReturn(again, to, nil); second.StatusCode != http.StatusBadRequest {
		t.Fatalf("a return used twice: %d", second.StatusCode)
	}
}
