package signin

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func doorEnv() map[string]string {
	return map[string]string{
		EnvIdentity: IdentityOIDC, EnvPublicURL: "https://liaison.example.test/", EnvSealKeys: "k1:abc",
		EnvOIDCIssuer: "https://idp.example.test", EnvOIDCClientID: "door", EnvOIDCSecretFile: "/secrets/client",
		EnvSigningKeysFile: "/secrets/signing", EnvRedirectHosts: "App.Example.Test",
	}
}

func fakeFiles(path string) ([]byte, error) {
	switch path {
	case "/secrets/client":
		return []byte("s3cret\n"), nil
	case "/secrets/signing":
		return []byte("pem"), nil
	}
	return nil, errors.New("no such file")
}

func TestDoorIsOffUnlessIdentityIsSet(t *testing.T) {
	cfg, err := DoorConfigFromEnv(envOf(goodEnv()), fakeFiles)
	if cfg != nil || err != nil {
		t.Errorf("the upstream front's settings must leave the door off, got %v %v", cfg, err)
	}
}

func TestDoorConfigFromEnv(t *testing.T) {
	cfg, err := DoorConfigFromEnv(envOf(doorEnv()), fakeFiles)
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]any{
		"public address": {cfg.PublicURL, "https://liaison.example.test"},
		"resource":       {cfg.Resource(), "https://liaison.example.test/mcp"},
		"refresh age":    {cfg.RefreshMaxAge, 168 * time.Hour},
		"redirect hosts": {strings.Join(cfg.AllowedRedirectHosts, ","), "app.example.test"},
		"client secret":  {cfg.OIDC.ClientSecret, "s3cret"},
		"scopes":         {cfg.OIDC.Scopes, defaultOIDCScopes},
		"groups claim":   {cfg.OIDC.GroupsClaim, defaultGroupsClaim},
		"user prefix":    {cfg.OIDC.UserPrefix, defaultUserPrefix},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
		}
	}
	cfg, err = DoorConfigFromEnv(envOf(withEnv(doorEnv(), EnvRefreshMaxHours, "24")), fakeFiles)
	if err != nil || cfg.RefreshMaxAge != 24*time.Hour {
		t.Errorf("refresh hours: %v %v", cfg, err)
	}
}

func TestDoorConfigRefusals(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"an unknown identity":                withEnv(doorEnv(), EnvIdentity, "ldap"),
		"the upstream front too":             withEnv(doorEnv(), EnvUpstream, "https://auth.example.test/authorize"),
		"no seal keys":                       withEnv(doorEnv(), EnvSealKeys, ""),
		"no signing keys":                    withEnv(doorEnv(), EnvSigningKeysFile, ""),
		"no client id":                       withEnv(doorEnv(), EnvOIDCClientID, ""),
		"no client secret":                   withEnv(doorEnv(), EnvOIDCSecretFile, ""),
		"no issuer":                          withEnv(doorEnv(), EnvOIDCIssuer, ""),
		"an http issuer":                     withEnv(doorEnv(), EnvOIDCIssuer, "http://idp.example.test"),
		"an http public address":             withEnv(doorEnv(), EnvPublicURL, "http://liaison.example.test"),
		"no public address":                  withEnv(doorEnv(), EnvPublicURL, ""),
		"an unreadable secret":               withEnv(doorEnv(), EnvOIDCSecretFile, "/missing"),
		"a refresh age that is not a number": withEnv(doorEnv(), EnvRefreshMaxHours, "soon"),
		"a refresh age of zero":              withEnv(doorEnv(), EnvRefreshMaxHours, "0"),
		"a refresh age past the cap":         withEnv(doorEnv(), EnvRefreshMaxHours, "99999"),
	} {
		if cfg, err := DoorConfigFromEnv(envOf(env), fakeFiles); err == nil {
			t.Errorf("%s was accepted: %+v", name, cfg)
		}
	}
}

func TestDoorWontStartWithoutUsableKeys(t *testing.T) {
	cfg := newDoorRig(t, nil).cfg
	cfg.SigningKeys = notPEM
	if _, err := NewDoor(&cfg); err == nil {
		t.Error("started with a signing key that is not PEM")
	}
}
