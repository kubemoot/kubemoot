package identity

import (
	"testing"
	"time"
)

func TestTokenExpiryReadsTheExpOfAJWTAndNothingElse(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	if got, ok := TokenExpiry(jwt(t, map[string]any{expClaim: exp.Unix()})); !ok || !got.Equal(exp) {
		t.Fatalf("got %v %v, want %v", got, ok, exp)
	}
	for name, token := range map[string]string{
		"not a JWT":    "garbage",
		"empty":        "",
		"no exp":       jwt(t, map[string]any{"sub": "x"}),
		"exp a word":   jwt(t, map[string]any{expClaim: "soon"}),
		"exp zero":     jwt(t, map[string]any{expClaim: 0}),
		"exp negative": jwt(t, map[string]any{expClaim: -5}),
	} {
		if _, ok := TokenExpiry(token); ok {
			t.Errorf("%s: an expiry was read", name)
		}
	}
}
