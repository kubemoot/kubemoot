package main

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/go-logr/logr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

func TestEnvHelpers(t *testing.T) {
	t.Setenv("LIAISON_TEST_STR", "x")
	t.Setenv("LIAISON_TEST_INT", "7")
	t.Setenv("LIAISON_TEST_BAD", "no")
	t.Setenv("LIAISON_TEST_ZERO", "0")
	if envOr("LIAISON_TEST_STR", "d") != "x" || envOr("LIAISON_TEST_MISSING", "d") != "d" {
		t.Fatal("envOr")
	}
	if envInt("LIAISON_TEST_INT", 1) != 7 || envInt("LIAISON_TEST_BAD", 1) != 1 || envInt("LIAISON_TEST_ZERO", 1) != 1 || envInt("LIAISON_TEST_MISSING", 3) != 3 {
		t.Fatal("envInt")
	}
}

func TestSignInIsOffWithoutAnUpstream(t *testing.T) {
	front, err := signInFront(logr.Discard(), identity.AnonymousAuthenticator(), tokenstore.NewMemory(nil))
	if front != nil || err != nil {
		t.Fatalf("no upstream set: %v %v", front, err)
	}
}

// A sign-in that could not say who signed in would issue door tokens for nobody, so the
// liaison refuses to start with one.
func TestSignInNeedsAVerifierThatNamesThePerson(t *testing.T) {
	t.Setenv("LIAISON_SIGNIN_UPSTREAM", "https://auth.example.test/authorize")
	t.Setenv("LIAISON_SIGNIN_UPSTREAM_TOKEN_URL", "https://auth.example.test/token")
	t.Setenv("LIAISON_SIGNIN_UPSTREAM_CLIENT_ID", "liaison")
	t.Setenv("LIAISON_SIGNIN_PUBLIC_URL", "https://liaison.example.test")
	t.Setenv("LIAISON_SIGNIN_SEAL_KEYS", "k1:"+base64.StdEncoding.EncodeToString(make([]byte, 32)))
	store := tokenstore.NewMemory(nil)
	for name, auth := range map[string]*identity.Authenticator{"none": nil, "anonymous only": identity.AnonymousAuthenticator()} {
		if _, err := signInFront(logr.Discard(), auth, store); !errors.Is(err, errSignInNeedsVerifier) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// With callers verified, the front is built and reads its own tokens.
	front, err := signInFront(logr.Discard(), identity.NewAuthenticator(identity.Hasher{}, false), store)
	if err != nil || front == nil || front.Tokens == nil || front.ResourceMetadataURL == "" {
		t.Fatalf("with a verifying door: %v %v", front, err)
	}
}
