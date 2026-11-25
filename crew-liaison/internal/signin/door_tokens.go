package signin

import (
	"crypto/ecdsa"
	"errors"
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// accessTokenType is the `typ` of a door access token (RFC 9068), so a token of another kind
// signed by the same key is never read as one.
const accessTokenType = "at+jwt"

// tokenSkew is the clock difference tolerated when a token's times are checked.
const tokenSkew = 5 * time.Second

// person is who signed in: the user name as Kubernetes RBAC will know it, and their groups.
type person struct {
	User   string   `json:"u"`
	Groups []string `json:"g,omitempty"`
	Name   string   `json:"n,omitempty"`
}

// accessClaims is the payload of a door access token beyond the registered claims.
type accessClaims struct {
	jwt.Claims
	Groups   []string `json:"groups,omitempty"`
	ClientID string   `json:"client_id,omitempty"`
}

// tokenKeys signs access tokens with the first key and verifies with all of them.
type tokenKeys struct {
	signer jose.Signer
	byKID  map[string]*ecdsa.PublicKey
}

func newTokenKeys(keys []*ecdsa.PrivateKey) (*tokenKeys, error) {
	tk := &tokenKeys{byKID: map[string]*ecdsa.PublicKey{}}
	for i, k := range keys {
		kid, err := identity.KeyThumbprint(&k.PublicKey)
		if err != nil {
			return nil, err
		}
		tk.byKID[kid] = &k.PublicKey
		if i > 0 {
			continue
		}
		opts := (&jose.SignerOptions{}).WithType(accessTokenType).WithHeader(jose.HeaderKey("kid"), kid)
		if tk.signer, err = jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: k}, opts); err != nil {
			return nil, err
		}
	}
	return tk, nil
}

// issueAccess signs a token for who, for the resource, valid until now plus the lifetime.
func (d *door) issueAccess(who person, clientID, jti string, now time.Time) (string, error) {
	return jwt.Signed(d.keys.signer).Claims(accessClaims{
		Claims: jwt.Claims{
			Issuer: d.cfg.PublicURL, Subject: who.User, Audience: jwt.Audience{d.cfg.Resource()}, ID: jti,
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
			Expiry: jwt.NewNumericDate(now.Add(accessTokenLifetime)),
		},
		Groups: who.Groups, ClientID: clientID,
	}).Serialize()
}

// Verify is the door's check of a bearer token on /mcp. A token that is not a door token (not
// a JWT, or issued by someone else) is ErrNoCredential, so the next kind of credential is
// tried. A door token that is expired, for another audience, or not signed by a door key is
// ErrInvalid and is never downgraded.
func (d *door) Verify(r *http.Request) (*identity.Identity, error) {
	raw := identity.BearerToken(r)
	if raw == "" {
		return nil, identity.ErrNoCredential
	}
	parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return nil, identity.ErrNoCredential
	}
	var peek jwt.Claims
	if parsed.UnsafeClaimsWithoutVerification(&peek) != nil || peek.Issuer != d.cfg.PublicURL {
		return nil, identity.ErrNoCredential
	}
	claims, err := d.verifyAccess(parsed)
	if err != nil {
		d.cfg.Logger.V(1).Info("door token refused", "reason", err.Error())
		return nil, identity.ErrInvalid
	}
	return &identity.Identity{Issuer: identity.IssuerKubernetes, Subject: claims.Subject, Groups: claims.Groups}, nil
}

var errTokenRefused = errors.New("not valid")

func (d *door) verifyAccess(parsed *jwt.JSONWebToken) (accessClaims, error) {
	var claims accessClaims
	h := parsed.Headers
	if len(h) != 1 || h[0].ExtraHeaders[jose.HeaderType] != accessTokenType {
		return claims, errTokenRefused
	}
	key, ok := d.keys.byKID[h[0].KeyID]
	if !ok || parsed.Claims(key, &claims) != nil {
		return claims, errTokenRefused
	}
	expected := jwt.Expected{Issuer: d.cfg.PublicURL, AnyAudience: []string{d.cfg.Resource()}, Time: d.now()}
	err := claims.ValidateWithLeeway(expected, tokenSkew)
	if err != nil || claims.Subject == "" || claims.ID == "" || claims.Expiry == nil {
		return claims, errTokenRefused
	}
	return claims, nil
}
