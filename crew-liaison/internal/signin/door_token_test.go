package signin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

func TestCodeBecomesASignedAccessTokenAndASealedRefreshToken(t *testing.T) {
	r := newDoorRig(t, nil)
	access, refresh := r.tokens()
	if access == "" || refresh == "" {
		t.Fatalf("tokens: %q %q", access, refresh)
	}
	checkAccessClaims(t, access)
	id, err := r.door.Verify(bearerRequest(access))
	if err != nil || id.Subject != doorUser || id.Issuer != identity.IssuerKubernetes {
		t.Fatalf("verify = %+v, %v", id, err)
	}
	if len(id.Groups) != 1 || id.Groups[0] != groupTeamA {
		t.Errorf("groups = %v", id.Groups)
	}
	if strings.Contains(r.logText(), access) || strings.Contains(r.logText(), refresh) {
		t.Error("a token reached the log")
	}
}

func checkAccessClaims(t *testing.T, access string) {
	t.Helper()
	parsed, err := jwt.ParseSigned(access, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := parsed.UnsafeClaimsWithoutVerification(&claims); err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != doorPublicURL || claims["aud"] != doorPublicURL+"/mcp" || claims["sub"] != doorUser || claims["jti"] == "" {
		t.Errorf("claims = %v", claims)
	}
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat != accessTokenLifetime.Seconds() {
		t.Errorf("lifetime = %v seconds", exp-iat)
	}
}

func TestCodeRefusals(t *testing.T) {
	verifier, challenge := appVerifier()
	cases := map[string]func(r *doorRig) (form map[string]string, code string){
		"another client": func(r *doorRig) (map[string]string, string) {
			return map[string]string{paramClientID: "other"}, r.code(challenge)
		},
		"another redirect": func(r *doorRig) (map[string]string, string) {
			return map[string]string{paramRedirect: evilRedirect}, r.code(challenge)
		},
		"another verifier": func(r *doorRig) (map[string]string, string) {
			return map[string]string{paramCodeVerifier: strings.Repeat("x", 50)}, r.code(challenge)
		},
		"a tampered code": func(r *doorRig) (map[string]string, string) { return nil, r.code(challenge) + "x" },
		"an expired code": func(r *doorRig) (map[string]string, string) {
			c := r.code(challenge)
			r.advance(codeLifetime + time.Second)
			return nil, c
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newDoorRig(t, nil)
			override, code := setup(r)
			form := r.codeForm(code, verifier)
			for k, v := range override {
				form.Set(k, v)
			}
			assertOAuthError(t, r.postToken(form), http.StatusBadRequest, errInvalidGrant)
		})
	}
}

func TestResourceMustBeThisDoorsMCP(t *testing.T) {
	r := newDoorRig(t, nil)
	verifier, challenge := appVerifier()
	form := r.codeForm(r.code(challenge), verifier)
	form.Set(paramResource, "https://other.example.test/mcp")
	assertOAuthError(t, r.postToken(form), http.StatusBadRequest, "invalid_target")
	refresh := r.refreshForm("whatever")
	refresh.Set(paramResource, "https://other.example.test/mcp")
	assertOAuthError(t, r.postToken(refresh), http.StatusBadRequest, "invalid_target")
}

func TestACodeWorksOnceAndItsReuseEndsTheSignIn(t *testing.T) {
	r := newDoorRig(t, nil)
	verifier, challenge := appVerifier()
	code := r.code(challenge)
	first := r.postToken(r.codeForm(code, verifier))
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first use: %d", first.StatusCode)
	}
	var tok tokenResponse
	decode(t, first, &tok)
	assertOAuthError(t, r.postToken(r.codeForm(code, verifier)), http.StatusBadRequest, errInvalidGrant)
	// The refresh token the first use produced belongs to the same sign-in, which is now ended.
	assertOAuthError(t, r.postToken(r.refreshForm(tok.RefreshToken)), http.StatusBadRequest, errInvalidGrant)
}

func TestRefreshRotatesAndAReplayedTokenEndsTheSignIn(t *testing.T) {
	r := newDoorRig(t, nil)
	_, refresh1 := r.tokens()
	second := r.postToken(r.refreshForm(refresh1))
	if second.StatusCode != http.StatusOK {
		t.Fatalf("rotation: %d %s", second.StatusCode, body(t, second))
	}
	var tok2 tokenResponse
	decode(t, second, &tok2)
	if tok2.RefreshToken == refresh1 || tok2.AccessToken == "" {
		t.Fatal("the refresh token did not rotate")
	}
	// The old token shown again is a copy: refused, and the new one dies with it.
	assertOAuthError(t, r.postToken(r.refreshForm(refresh1)), http.StatusBadRequest, errInvalidGrant)
	assertOAuthError(t, r.postToken(r.refreshForm(tok2.RefreshToken)), http.StatusBadRequest, errInvalidGrant)
}

func TestRefreshHonoursItsClientAndItsTwoLifetimes(t *testing.T) {
	r := newDoorRig(t, nil)
	_, refresh := r.tokens()
	other := r.refreshForm(refresh)
	other.Set(paramClientID, "someone-else")
	assertOAuthError(t, r.postToken(other), http.StatusBadRequest, errInvalidGrant)

	// Used steadily, a sign-in still ends at its absolute time: rotation does not extend it.
	cur := refresh
	for elapsed := time.Duration(0); elapsed+24*time.Hour < r.cfg.RefreshMaxAge; elapsed += 24 * time.Hour {
		r.advance(24 * time.Hour)
		resp := r.postToken(r.refreshForm(cur))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("after %s: %d", elapsed, resp.StatusCode)
		}
		var tok tokenResponse
		decode(t, resp, &tok)
		cur = tok.RefreshToken
	}
	r.advance(48 * time.Hour)
	assertOAuthError(t, r.postToken(r.refreshForm(cur)), http.StatusBadRequest, errInvalidGrant)
}

func TestAnIdleRefreshTokenExpires(t *testing.T) {
	r := newDoorRig(t, func(c *DoorConfig) { c.RefreshMaxAge = 90 * 24 * time.Hour })
	_, refresh := r.tokens()
	r.advance(refreshIdleLifetime + time.Hour)
	assertOAuthError(t, r.postToken(r.refreshForm(refresh)), http.StatusBadRequest, errInvalidGrant)
}

func TestVerifyRefusals(t *testing.T) {
	r := newDoorRig(t, nil)
	access, _ := r.tokens()

	if _, err := r.door.Verify(bearerRequest("")); !errors.Is(err, identity.ErrNoCredential) {
		t.Errorf("no token: %v", err)
	}
	for name, token := range map[string]string{"an opaque token": "abc", "a kubernetes style JWT": "eyJhbGciOiJSUzI1NiJ9.e30.c2ln"} {
		if _, err := r.door.Verify(bearerRequest(token)); !errors.Is(err, identity.ErrNoCredential) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.door.Verify(bearerRequest(access[:len(access)-3] + "AAA")); !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("tampered: %v", err)
	}
	r.advance(accessTokenLifetime + time.Minute)
	if _, err := r.door.Verify(bearerRequest(access)); !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("expired: %v", err)
	}
}

// mint signs claims the door would not write, with the door's own key, to test what Verify refuses.
func (r *doorRig) mint(t *testing.T, mutate func(*accessClaims), typ string) string {
	t.Helper()
	now := r.clock()
	claims := accessClaims{Claims: jwt.Claims{
		Issuer: doorPublicURL, Subject: doorUser, Audience: jwt.Audience{r.cfg.Resource()}, ID: "x",
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
	}}
	mutate(&claims)
	keys, _ := ParseSigningKeys(r.cfg.SigningKeys)
	kid, _ := identity.KeyThumbprint(&keys[0].PublicKey)
	opts := (&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), kid)
	if typ != "" {
		opts = opts.WithType(jose.ContentType(typ))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: keys[0]}, opts)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestVerifyRefusesAnotherAudienceTypeOrKey(t *testing.T) {
	r := newDoorRig(t, nil)
	good := func(c *accessClaims) {}
	if _, err := r.door.Verify(bearerRequest(r.mint(t, good, ""))); !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("a token with no type: %v", err)
	}
	cases := map[string]func(*accessClaims){
		"another audience (a service's token)": func(c *accessClaims) { c.Audience = jwt.Audience{"https://api.example.test"} },
		"no audience":                          func(c *accessClaims) { c.Audience = nil },
		"no subject":                           func(c *accessClaims) { c.Subject = "" },
		"no id":                                func(c *accessClaims) { c.ID = "" },
		"not yet valid":                        func(c *accessClaims) { c.NotBefore = jwt.NewNumericDate(r.clock().Add(time.Hour)) },
	}
	for name, mutate := range cases {
		if _, err := r.door.Verify(bearerRequest(r.mintTyped(t, mutate))); !errors.Is(err, identity.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.door.Verify(bearerRequest(r.mintTyped(t, good))); err != nil {
		t.Errorf("the control token was refused: %v", err)
	}
	// Another issuer's token is not ours to judge.
	other := r.mintTyped(t, func(c *accessClaims) { c.Issuer = "https://elsewhere.example.test" })
	if _, err := r.door.Verify(bearerRequest(other)); !errors.Is(err, identity.ErrNoCredential) {
		t.Errorf("another issuer: %v", err)
	}
}

func (r *doorRig) mintTyped(t *testing.T, mutate func(*accessClaims)) string {
	return r.mint(t, mutate, accessTokenType)
}

func TestSigningKeysRotate(t *testing.T) {
	oldKey := privateKeyPEM(t)
	r := newDoorRig(t, func(c *DoorConfig) { c.SigningKeys = oldKey })
	oldAccess, _ := r.tokens()
	// A new key goes first: it signs, and the old key stays listed so live tokens keep working.
	rotated := newDoorRig(t, func(c *DoorConfig) { c.SigningKeys = privateKeyPEM(t) + oldKey; c.SealKeys = r.cfg.SealKeys })
	if _, err := rotated.door.Verify(bearerRequest(oldAccess)); err != nil {
		t.Fatalf("a token of the old key was refused after rotation: %v", err)
	}
	newAccess, _ := rotated.tokens()
	if _, err := r.door.Verify(bearerRequest(newAccess)); !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("a token of a key this door does not list was accepted: %v", err)
	}
	// Dropping the old key retires its tokens.
	retired := newDoorRig(t, func(c *DoorConfig) { c.SigningKeys = privateKeyPEM(t) })
	if _, err := retired.door.Verify(bearerRequest(oldAccess)); !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("a token of a retired key: %v", err)
	}
}

func TestSigningKeyParsing(t *testing.T) {
	for name, text := range map[string]string{"none": "", "not PEM": notPEM, "a certificate block": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"} {
		if _, err := ParseSigningKeys(text); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := ParseSigningKeys(strings.Repeat(privateKeyPEM(t), maxSigningKeys+1)); err == nil {
		t.Error("too many keys were accepted")
	}
}

func TestOldFrontMetadataIsUnchanged(t *testing.T) {
	r := newRig(t, nil)
	resp := r.get(pathAuthServer)
	var got map[string]any
	decode(t, resp, &got)
	want := []string{keyIssuer, keyAuthEndpoint, keyTokenEndpoint, "registration_endpoint", "response_types_supported",
		"grant_types_supported", "code_challenge_methods_supported", "token_endpoint_auth_methods_supported"}
	if len(got) != len(want) {
		keys, _ := json.Marshal(got)
		t.Errorf("the upstream front's metadata changed: %s", keys)
	}
}

func TestDoorMetadataAdvertisesWhatItDoes(t *testing.T) {
	r := newDoorRig(t, nil)
	req, _ := http.NewRequest(http.MethodGet, r.srv.URL+pathAuthServer, nil)
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	decode(t, resp, &got)
	if got["authorization_response_iss_parameter_supported"] != true || got[keyIssuer] != doorPublicURL {
		t.Errorf("metadata = %v", got)
	}
}
