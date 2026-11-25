package signin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// appServer serves a chat app's client metadata document over TLS and counts the requests it gets.
type appServer struct {
	srv  *httptest.Server
	hits atomic.Int64
	// doc builds the document for the address it was fetched at; handler, when set, answers instead.
	doc     func(self string) map[string]any
	handler http.HandlerFunc
}

func newAppServer(t *testing.T) *appServer {
	t.Helper()
	a := &appServer{}
	a.doc = func(self string) map[string]any {
		return map[string]any{paramClientID: self, paramClientName: "Cimd App", keyRedirectURIs: []string{appRedirect}}
	}
	a.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.hits.Add(1)
		if a.handler != nil {
			a.handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(a.doc("https://" + r.Host + r.URL.Path))
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *appServer) clientID() string { return a.srv.URL + "/client.json" }

func allowAll(netip.Addr) bool { return true }

// cimdRig is a door that trusts the app server's certificate and, unlike a real door, may dial
// loopback, which is where the test servers are.
func cimdRig(t *testing.T, a *appServer, mutate func(*DoorConfig)) *doorRig {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(a.srv.Certificate())
	return newDoorRig(t, func(c *DoorConfig) {
		c.TLSConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		c.fetchAllow = allowAll
		if mutate != nil {
			mutate(c)
		}
	})
}

func authorizeStatus(t *testing.T, r *doorRig, clientID string, override url.Values) *http.Response {
	t.Helper()
	_, challenge := appVerifier()
	return newBrowser(t, r.srv.URL).get(r.authorizeQuery(clientID, challenge, override))
}

func TestAnAppIdentifiedByItsMetadataDocumentSignsIn(t *testing.T) {
	app := newAppServer(t)
	r := cimdRig(t, app, nil)
	b := newBrowser(t, r.srv.URL)
	verifier, challenge := appVerifier()
	page := r.signIn(b, app.clientID(), challenge, janeClaims)
	text := body(t, page)
	if page.StatusCode != http.StatusOK || !strings.Contains(text, "Cimd App") || !strings.Contains(text, "app.example.test") {
		t.Fatalf("consent page %d: %s", page.StatusCode, text)
	}
	if !strings.Contains(text, "127.0.0.1") {
		t.Error("the page does not say which address identifies the app")
	}
	form := fieldsOf(t, text)
	form.Set("decision", "allow")
	back := location(t, b.postConsent(form, nil))
	resp := r.postToken(url.Values{
		paramGrantType: {grantAuthCode}, paramClientID: {app.clientID()}, paramCode: {back.Query().Get(paramCode)},
		paramCodeVerifier: {verifier}, paramRedirect: {appRedirect}, paramResource: {r.cfg.Resource()},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token: %d %s", resp.StatusCode, body(t, resp))
	}
	if app.hits.Load() != 1 {
		t.Errorf("the document was fetched %d times for one sign-in", app.hits.Load())
	}
}

func TestConsentIsAskedOncePerMetadataClient(t *testing.T) {
	app := newAppServer(t)
	r := cimdRig(t, app, nil)
	b := newBrowser(t, r.srv.URL)
	_, challenge := appVerifier()
	r.allow(b, r.signIn(b, app.clientID(), challenge, janeClaims))
	again := location(t, b.get(r.authorizeQuery(app.clientID(), challenge, nil)))
	if again.Query().Get(paramCode) == "" {
		t.Fatalf("the second request asked again: %s", again)
	}
}

func TestDynamicRegistrationStillWorksBesideMetadataDocuments(t *testing.T) {
	app := newAppServer(t)
	r := cimdRig(t, app, nil)
	b := newBrowser(t, r.srv.URL)
	_, challenge := appVerifier()
	if page := r.signIn(b, r.register("Registered App"), challenge, janeClaims); page.StatusCode != http.StatusOK {
		t.Fatalf("a registered client was refused: %d", page.StatusCode)
	}
	if app.hits.Load() != 0 {
		t.Error("a registered client caused a fetch")
	}
}

func TestRedirectMustBeOneTheDocumentNames(t *testing.T) {
	app := newAppServer(t)
	r := cimdRig(t, app, nil)
	resp := authorizeStatus(t, r, app.clientID(), url.Values{paramRedirect: {"https://app.example.test/elsewhere"}})
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Errorf("a redirect the document does not name: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestMetadataRefusals: every way a document or its address can be wrong ends in a plain error to the
// browser, never a redirect, since the redirect is not trusted yet.
func TestMetadataRefusals(t *testing.T) {
	cases := map[string]func(a *appServer){
		"another client id inside": func(a *appServer) {
			a.doc = func(string) map[string]any {
				return map[string]any{paramClientID: "https://other.example.test/c", keyRedirectURIs: []string{appRedirect}}
			}
		},
		"no redirect addresses": func(a *appServer) {
			a.doc = func(self string) map[string]any { return map[string]any{paramClientID: self} }
		},
		"a redirect on a host the install does not allow": func(a *appServer) {
			a.doc = func(self string) map[string]any {
				return map[string]any{paramClientID: self, keyRedirectURIs: []string{evilRedirect}}
			}
		},
		"a plain http redirect": func(a *appServer) {
			a.doc = func(self string) map[string]any {
				return map[string]any{paramClientID: self, keyRedirectURIs: []string{"http://app.example.test/cb"}}
			}
		},
		"a client secret": func(a *appServer) {
			a.doc = func(self string) map[string]any {
				return map[string]any{paramClientID: self, keyRedirectURIs: []string{appRedirect}, "client_secret": "x"}
			}
		},
		"a confidential client": func(a *appServer) {
			a.doc = func(self string) map[string]any {
				return map[string]any{paramClientID: self, keyRedirectURIs: []string{appRedirect}, "token_endpoint_auth_method": "client_secret_basic"}
			}
		},
		"not JSON": func(a *appServer) {
			a.handler = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("<html>"))
			}
		},
		"the wrong content type": func(a *appServer) {
			a.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_ = json.NewEncoder(w).Encode(a.doc("https://" + r.Host + r.URL.Path))
			}
		},
		"an error status": func(a *appServer) {
			a.handler = func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusInternalServerError) }
		},
		"a body that is too large": func(a *appServer) {
			a.handler = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"client_name":"` + strings.Repeat("a", maxMetadataBytes) + `"}`))
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			app := newAppServer(t)
			setup(app)
			r := cimdRig(t, app, func(c *DoorConfig) { c.AllowedRedirectHosts = []string{appHost} })
			resp := authorizeStatus(t, r, app.clientID(), nil)
			if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
				t.Fatalf("answered %d, location %q", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
}

func TestAnAppThatAnswersWithARedirectIsNotFollowed(t *testing.T) {
	private := newAppServer(t)
	app := newAppServer(t)
	app.handler = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.srv.URL+"/client.json", http.StatusFound)
	}
	r := cimdRig(t, app, nil)
	if resp := authorizeStatus(t, r, app.clientID(), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("answered %d", resp.StatusCode)
	}
	if private.hits.Load() != 0 {
		t.Errorf("the redirect was followed to an address the door did not choose: %d requests", private.hits.Load())
	}
}

func TestASlowAppIsGivenUp(t *testing.T) {
	var release sync.Once
	gate := make(chan struct{})
	app := newAppServer(t)
	app.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"client_id":`))
		w.(http.Flusher).Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
		}
	}
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	r := cimdRig(t, app, func(c *DoorConfig) { c.fetchTimeout = 150 * time.Millisecond })
	if resp := authorizeStatus(t, r, app.clientID(), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("answered %d", resp.StatusCode)
	}
}

func TestFetchesAreRateLimited(t *testing.T) {
	app := newAppServer(t)
	r := cimdRig(t, app, func(c *DoorConfig) { c.fetchBurst = 2 })
	var refused int
	for i := 0; i < 6; i++ {
		if authorizeStatus(t, r, app.clientID(), nil).StatusCode == http.StatusBadRequest {
			refused++
		}
	}
	if app.hits.Load() != 2 || refused != 4 {
		t.Errorf("%d fetches reached the app and %d were refused, want 2 and 4", app.hits.Load(), refused)
	}
}

// TestTheDoorDoesNotReachPrivateAddresses uses the door's real address rule: no override, so a
// name that resolves to a private address, an address written out, and a mixed answer are all
// refused before any connection.
func TestTheDoorDoesNotReachPrivateAddresses(t *testing.T) {
	answers := map[string][]string{
		"private.example.test": {"10.0.0.5"}, "loopback.example.test": {"127.0.0.1"}, "metadata.example.test": {"169.254.169.254"},
		"shared.example.test": {"100.64.0.1"}, "v6.example.test": {"fd00::1"}, "mapped.example.test": {"::ffff:192.168.1.1"},
		"mixed.example.test": {"93.184.216.34", "10.0.0.5"},
	}
	var dialed atomic.Int64
	lookup := func(_ any, host string) []netip.Addr {
		out := make([]netip.Addr, 0, len(answers[host]))
		for _, a := range answers[host] {
			out = append(out, netip.MustParseAddr(a))
		}
		return out
	}
	app := newAppServer(t)
	r := cimdRig(t, app, func(c *DoorConfig) {
		c.fetchAllow = nil // the real rule
		c.fetchLookup = func(_ context.Context, host string) ([]netip.Addr, error) {
			dialed.Add(1)
			return lookup(nil, host), nil
		}
	})
	for _, id := range []string{
		"https://private.example.test/c.json", "https://loopback.example.test/c.json", "https://metadata.example.test/c.json",
		"https://shared.example.test/c.json", "https://v6.example.test/c.json", "https://mapped.example.test/c.json",
		"https://mixed.example.test/c.json", "https://10.0.0.1/c.json", "https://127.0.0.1:1/c.json",
		"https://[::1]/c.json", "https://169.254.169.254/latest/meta-data/x", "https://[::ffff:10.0.0.1]/c.json",
	} {
		if resp := authorizeStatus(t, r, id, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: answered %d", id, resp.StatusCode)
		}
	}
	if app.hits.Load() != 0 {
		t.Errorf("a request reached a server it should not have: %d", app.hits.Load())
	}
}

func TestPublicAddressRule(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1::1": true, "8.8.8.8": true,
		"10.1.2.3": false, "172.16.0.1": false, "192.168.0.1": false, "127.0.0.1": false, "0.0.0.0": false, "169.254.1.1": false,
		"100.100.100.100": false, "224.0.0.1": false, "255.255.255.255": false, "::1": false, "::": false, "fe80::1": false,
		"fc00::1": false, "::ffff:127.0.0.1": false, "64:ff9b::a00:1": false, "2002:a00:1::": false, "198.18.0.1": false, "2001:0:4136:e378::1": false, "3fff::1": false, "192.88.99.1": false, "240.0.0.1": false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestClientIDAddressRules(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://app.example.test/client.json": true, "https://app.example.test:8443/a/b?x=1": true,
		"http://app.example.test/client.json": false, "https://app.example.test": false, "https://app.example.test/": false,
		"https://user@app.example.test/c": false, "https://app.example.test/c#frag": false, "https://app.example.test/a/../c": false,
		"https://app.example.test/./c": false, "https:///c": false, "https://app.example.test/c\\x": false,
		"https://app.example.test/" + strings.Repeat("a", maxClientIDChars): false,
	} {
		if got := validClientIDURL(raw); got != want {
			t.Errorf("validClientIDURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestADocumentFetchErrorIsNotTheAppsToRead(t *testing.T) {
	app := newAppServer(t)
	app.handler = func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret-internal-detail", http.StatusBadGateway)
	}
	r := cimdRig(t, app, nil)
	resp := authorizeStatus(t, r, app.clientID(), nil)
	if strings.Contains(body(t, resp), "secret-internal-detail") || strings.Contains(r.logText(), "secret-internal-detail") {
		t.Error("what the app's server said was passed on")
	}
}
