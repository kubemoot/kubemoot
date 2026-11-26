package liaison

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// requireDoor puts the door's own access tokens in front of the other credentials on /mcp. A
// door token that checks out makes its holder the caller and goes straight to direct; one that
// does not (expired, another audience, an unknown key) is refused and never tried as anything
// else. A request with no door token goes to fallback (the shared token and Kubernetes
// tokens) when the install has one, and is otherwise refused: with the door's identity on, an
// open /mcp is never the default.
func requireDoor(front *SignInFront, fallbackExists bool, direct, fallback http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := front.Verifier.Verify(r)
		switch {
		case err == nil:
			ctx := identity.WithCaller(r.Context(), identity.Caller{
				Principal: identity.PrincipalOf(identity.Hasher{}, id), Identity: id,
			})
			direct.ServeHTTP(w, r.WithContext(withAsserter(ctx, front.Hop, id)))
		case errors.Is(err, identity.ErrNoCredential) && fallbackExists:
			fallback.ServeHTTP(w, r)
		case errors.Is(err, identity.ErrNoCredential):
			refuse(w, identity.ErrInvalid, front.ResourceMetadataURL)
		default:
			refuse(w, err, front.ResourceMetadataURL)
		}
	})
}

type asserterKey struct{}

// asserter signs, for one request, the assertion of the person the door signed in.
type asserter struct {
	signer *identity.HopSigner
	user   string
	groups []string
}

// withAsserter carries who the door signed in, so the call to a crew's gateway can assert
// them. Without a hop key the context is unchanged and no credential reaches the gateway.
func withAsserter(ctx context.Context, signer *identity.HopSigner, id *identity.Identity) context.Context {
	if signer == nil {
		return ctx
	}
	return context.WithValue(ctx, asserterKey{}, asserter{signer: signer, user: id.Subject, groups: id.Groups})
}

// credentialFor is the bearer token to present to the gateway of one crew for ctx: a fresh
// assertion for a person the door signed in, else the token the caller presented ("" for none).
// The assertion names this crew's gateway alone and lives a minute. A person who cannot be
// asserted (a name or group the gateway would refuse) is an error: the question is not sent
// with no credential, which a crew that accepts anonymous callers would answer as anonymous.
func credentialFor(ctx context.Context, namespace, crew string) (string, error) {
	a, ok := ctx.Value(asserterKey{}).(asserter)
	if !ok {
		return credentialFrom(ctx), nil
	}
	token, err := a.signer.Assert(identity.HopAudience(namespace, crew), a.user, a.groups)
	if err != nil {
		return "", fmt.Errorf("you cannot be asserted to crew %s/%s: %w", namespace, crew, err)
	}
	return token, nil
}

// doorUserOf is the user name the door signed in for this request, and false for a caller the
// door did not sign in (a service token, a Kubernetes token): only a door person has accounts.
func doorUserOf(ctx context.Context) (string, bool) {
	a, ok := ctx.Value(asserterKey{}).(asserter)
	if !ok || a.user == "" {
		return "", false
	}
	return a.user, true
}
