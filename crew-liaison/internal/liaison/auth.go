package liaison

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

type credentialKey struct{}

// withCredential carries the caller's token for the crew's gateway with a request: the
// Kubernetes token the caller presented, or the outside service's token the liaison keeps
// for a person who signed in. The gateway, not the liaison, decides what the caller may
// see. The liaison's shared static token and its own door tokens are never carried.
func withCredential(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, credentialKey{}, token)
}

// credentialFrom is the bearer token to present to the gateway for ctx, or "".
func credentialFrom(ctx context.Context) string {
	token, _ := ctx.Value(credentialKey{}).(string)
	return token
}

// ownerOf is the id a ticket records for the caller in ctx: the principal's id, empty for
// an anonymous caller. Tickets with an empty owner are open to every anonymous caller, as
// before identity existed.
func ownerOf(ctx context.Context) string {
	return identity.CallerFrom(ctx).Principal.ID
}

// DoorTokens reads a bearer token the liaison's own sign-in issued. For a door token it
// returns the person and the token an outside service issued for them, which the liaison
// keeps and the chat app never holds. A bearer that is not a door token is
// identity.ErrNoCredential; a door token that is no longer good is identity.ErrInvalid.
type DoorTokens func(r *http.Request, bearer string) (identity.Caller, string, error)

// door is the liaison's gate on /mcp for an install that verifies callers.
type door struct {
	token       string
	auth        *identity.Authenticator
	tokens      DoorTokens
	metadataURL string
	next        http.Handler
}

// requireCaller is the liaison's door. Without a verifying authenticator it is the static
// bearer gate as it always was. With one, a request is a caller when it presents the shared
// static token (an anonymous caller), a token the liaison's own sign-in issued (the person
// who signed in), or a Kubernetes token the authenticator accepts (a principal). A token an
// outside service issued is never accepted here, even one the authenticator can verify: the
// door takes only tokens issued for it. When a shared token is configured, a request that
// presents nothing is refused.
func requireCaller(token string, auth *identity.Authenticator, front *SignInFront, next http.Handler) http.Handler {
	metadataURL := ""
	var tokens DoorTokens
	if front != nil {
		metadataURL, tokens = front.ResourceMetadataURL, front.Tokens
	}
	if auth == nil || !auth.Enforcing() {
		return requireBearer(token, metadataURL, next)
	}
	return &door{token: token, auth: auth, tokens: tokens, metadataURL: metadataURL, next: next}
}

func (d *door) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	presented := identity.BearerToken(r)
	if d.token != "" && presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(d.token)) == 1 {
		d.next.ServeHTTP(w, r.WithContext(identity.WithCaller(r.Context(), identity.AnonymousCaller())))
		return
	}
	if d.token != "" && presented == "" {
		// A shared token is configured, so a request with no credential is refused even
		// when anonymous callers are allowed: anonymous never loosens the shared token.
		refuse(w, identity.ErrRequired, d.metadataURL)
		return
	}
	caller, credential, err := d.callerOf(r, presented)
	if err != nil {
		refuse(w, err, d.metadataURL)
		return
	}
	ctx := identity.WithCaller(r.Context(), caller)
	if !caller.Anonymous() {
		ctx = withCredential(ctx, credential)
	}
	d.next.ServeHTTP(w, r.WithContext(ctx))
}

// callerOf names the caller of r and the token to present to a crew's gateway for them. A
// door token is tried first; what it resolves to is the person and the token kept for them.
// Any other bearer goes to the authenticator, and a person an outside service vouched for
// is refused there: that token was issued for the service, not for this door.
func (d *door) callerOf(r *http.Request, presented string) (identity.Caller, string, error) {
	if d.tokens != nil && presented != "" {
		caller, kept, err := d.tokens(r, presented)
		if !errors.Is(err, identity.ErrNoCredential) {
			return caller, kept, err
		}
	}
	caller, err := d.auth.Authenticate(r)
	if err != nil {
		return identity.Caller{}, "", err
	}
	if caller.Principal.Kind == bus.PrincipalKindSubscriber {
		log.Info("refused a service's own token at the door", "issuer", caller.Principal.Issuer)
		return identity.Caller{}, "", identity.ErrInvalid
	}
	return caller, presented, nil
}

// PersonFrom names the person a service's token belongs to with the door's verifiers, for a
// sign-in that has just received that token or keeps it. r lends its context and peer
// address; its own Authorization header is not used. A token that names nobody is
// identity.ErrInvalid.
func PersonFrom(auth *identity.Authenticator) func(r *http.Request, token string) (identity.Caller, error) {
	return func(r *http.Request, token string) (identity.Caller, error) {
		probe := r.Clone(r.Context())
		probe.Header.Set("Authorization", "Bearer "+token)
		caller, err := auth.Authenticate(probe)
		if err != nil {
			return identity.Caller{}, err
		}
		if caller.Anonymous() || caller.Principal.ID == "" {
			return identity.Caller{}, identity.ErrInvalid
		}
		return caller, nil
	}
}

// refuse answers an authentication error: 401 with a challenge, or 503 when the check failed.
func refuse(w http.ResponseWriter, err error, metadataURL string) {
	code, message, challenge := identity.Refusal(err, "kubemoot-crew-liaison")
	if challenge != "" {
		w.Header().Set("WWW-Authenticate", withMetadata(challenge, metadataURL))
	}
	http.Error(w, message, code)
}

// withMetadata adds the protected-resource metadata address (RFC 9728) to a Bearer
// challenge, so an MCP app knows where to learn how to sign in. "" leaves it as it was.
func withMetadata(challenge, metadataURL string) string {
	if metadataURL == "" || challenge == "" {
		return challenge
	}
	return challenge + `, resource_metadata="` + metadataURL + `"`
}
