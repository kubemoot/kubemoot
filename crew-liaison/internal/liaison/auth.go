package liaison

import (
	"context"
	"crypto/subtle"
	"net/http"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

type credentialKey struct{}

// withCredential carries the caller's bearer token with a request, so the gateway call made
// for it presents the token the caller presented and the gateway, not the liaison, decides
// what the caller may see. The liaison's shared static token is never carried.
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

// requireCaller is the liaison's door. Without a verifying authenticator it is the static
// bearer gate as it always was. With one, a request is a caller when it presents the shared
// static token (an anonymous caller) or a Kubernetes token the authenticator accepts (a
// principal), and the caller and the token travel with the request. When a shared token
// is configured, a request that presents nothing is refused.
func requireCaller(token string, auth *identity.Authenticator, next http.Handler) http.Handler {
	if auth == nil || !auth.Enforcing() {
		return requireBearer(token, next)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := identity.BearerToken(r)
		if token != "" && presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
			next.ServeHTTP(w, r.WithContext(identity.WithCaller(r.Context(), identity.AnonymousCaller())))
			return
		}
		if token != "" && presented == "" {
			// A shared token is configured, so a request with no credential is refused even
			// when anonymous callers are allowed: anonymous never loosens the shared token.
			refuse(w, identity.ErrRequired)
			return
		}
		caller, err := auth.Authenticate(r)
		if err != nil {
			refuse(w, err)
			return
		}
		ctx := identity.WithCaller(r.Context(), caller)
		if !caller.Anonymous() {
			ctx = withCredential(ctx, presented)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// refuse answers an authentication error: 401 with a challenge, or 503 when the check failed.
func refuse(w http.ResponseWriter, err error) {
	code, message, challenge := identity.Refusal(err, "kubemoot-crew-liaison")
	if challenge != "" {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	http.Error(w, message, code)
}
