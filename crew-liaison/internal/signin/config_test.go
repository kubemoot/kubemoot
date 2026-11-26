package signin

import (
	"errors"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigOffWithoutUpstream(t *testing.T) {
	cfg, err := ConfigFromEnv(envOf(map[string]string{EnvPublicURL: "https://x.example.test"}), nil)
	if cfg != nil || err != nil {
		t.Errorf("want off, got %v %v", cfg, err)
	}
}

func goodEnv() map[string]string {
	return map[string]string{
		EnvUpstream: "https://auth.example.test/authorize", EnvUpstreamToken: "https://auth.example.test/token",
		EnvUpstreamID: "cid", EnvPublicURL: "https://liaison.example.test/", EnvSealKeys: "k1:abc",
		EnvRedirectHosts: " App.Example.Test, other.example.test ,",
	}
}

func withEnv(base map[string]string, key, value string) map[string]string {
	m := map[string]string{}
	for k, v := range base {
		m[k] = v
	}
	if value == "" {
		delete(m, key)
	} else {
		m[key] = value
	}
	return m
}

func TestConfigFromEnv(t *testing.T) {
	cfg, err := ConfigFromEnv(envOf(goodEnv()), nil)
	if err != nil || cfg.PublicURL != "https://liaison.example.test" ||
		len(cfg.AllowedRedirectHosts) != 2 || cfg.AllowedRedirectHosts[0] != appHost {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, err := ConfigFromEnv(envOf(withEnv(goodEnv(), EnvPublicURL, "http://localhost:8080")), nil); err != nil {
		t.Errorf("loopback http should be allowed: %v", err)
	}
}

func TestConfigKeysFromFile(t *testing.T) {
	m := withEnv(withEnv(goodEnv(), EnvSealKeys, ""), EnvSealKeysFile, "/keys")
	cfg, err := ConfigFromEnv(envOf(m), func(string) ([]byte, error) { return []byte("k9:zzz"), nil })
	if err != nil || cfg.SealKeys != "k9:zzz" {
		t.Errorf("file keys: %v %v", cfg, err)
	}
	if _, err = ConfigFromEnv(envOf(m), func(string) ([]byte, error) { return nil, errors.New("gone") }); err == nil {
		t.Error("an unreadable key file must fail")
	}
}

func TestConfigRefusesIncompleteOrUnsafe(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"no client id":  withEnv(goodEnv(), EnvUpstreamID, ""),
		"no keys":       withEnv(goodEnv(), EnvSealKeys, ""),
		"no public url": withEnv(goodEnv(), EnvPublicURL, ""),
		"no token url":  withEnv(goodEnv(), EnvUpstreamToken, ""),
		"plain http":    withEnv(goodEnv(), EnvPublicURL, "http://liaison.example.test"),
		"relative":      withEnv(goodEnv(), EnvPublicURL, "/x"),
		"fragment":      withEnv(goodEnv(), EnvPublicURL, "https://a.example.test/#f"),
		"http upstream": withEnv(goodEnv(), EnvUpstream, "http://auth.example.test/authorize"),
		"account name":  withEnv(goodEnv(), EnvUpstreamName, "Not.A.Label"),
		"lifetime":      withEnv(goodEnv(), EnvUpstreamKeep, "soon"),
		"no lifetime":   withEnv(goodEnv(), EnvUpstreamKeep, "0s"),
	} {
		if _, err := ConfigFromEnv(envOf(m), nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestNewRejectsBadKeys(t *testing.T) {
	cfg := Config{PublicURL: "https://a.example.test", UpstreamAuthorizeURL: "https://u.example.test/a", UpstreamTokenURL: "https://u.example.test/t", UpstreamClientID: "c", SealKeys: "k:short"}
	if _, err := New(&cfg); err == nil {
		t.Error("a short key must fail")
	}
}

func TestConfigNamesTheUpstreamAccountAndItsLifetime(t *testing.T) {
	cfg, err := ConfigFromEnv(envOf(goodEnv()), nil)
	if err != nil || cfg.UpstreamName != defaultUpstreamName || cfg.UpstreamTokenLifetime != 0 {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	m := withEnv(withEnv(goodEnv(), EnvUpstreamName, "library"), EnvUpstreamKeep, "720h")
	cfg, err = ConfigFromEnv(envOf(m), nil)
	if err != nil || cfg.UpstreamName != "library" || cfg.UpstreamTokenLifetime != 720*time.Hour {
		t.Fatalf("set: %+v %v", cfg, err)
	}
}

func TestNewNeedsAWayToNameThePerson(t *testing.T) {
	cfg := Config{
		PublicURL: "https://a.example.test", UpstreamAuthorizeURL: "https://u.example.test/a",
		UpstreamTokenURL: "https://u.example.test/t", UpstreamClientID: "c", SealKeys: testKey(t, "k1"),
	}
	if _, err := New(&cfg); !errors.Is(err, errNoIdentifier) {
		t.Errorf("a front with no identifier: %v", err)
	}
}
