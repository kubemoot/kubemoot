package liaison

import (
	"errors"
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
			direct.ServeHTTP(w, r.WithContext(ctx))
		case errors.Is(err, identity.ErrNoCredential) && fallbackExists:
			fallback.ServeHTTP(w, r)
		case errors.Is(err, identity.ErrNoCredential):
			refuse(w, identity.ErrInvalid, front.ResourceMetadataURL)
		default:
			refuse(w, err, front.ResourceMetadataURL)
		}
	})
}
