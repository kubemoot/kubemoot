/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"errors"
	"testing"
)

const chainURL = "https://api.example/mcp"

func chainConfig(name string) ServiceTokenConfig {
	return ServiceTokenConfig{Name: name, VerifyURL: chainURL, SubjectClaim: "sub"}
}

func TestServiceTokenChainNeedsTheKey(t *testing.T) {
	if _, err := NewServiceTokenChain([]ServiceTokenConfig{chainConfig(testIssuer)}, nil, nil); !errors.Is(err, ErrNoHashKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceTokenChainBuildsOneVerifierPerServiceAndKeysTheHasher(t *testing.T) {
	chain, err := NewServiceTokenChain([]ServiceTokenConfig{chainConfig(testIssuer)}, testKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.Verifiers) != 1 || !chain.Hasher.Keyed() || chain.Authorizer == nil {
		t.Fatalf("chain = %+v", chain)
	}
}

func TestServiceTokenChainRefusesWhatTheCRDRefuses(t *testing.T) {
	bad := chainConfig(testIssuer)
	bad.VerifyURL = "http://plain.example"
	cases := map[string][]ServiceTokenConfig{
		"a plain http url":             {bad},
		"a name used twice":            {chainConfig("a"), chainConfig("a")},
		"two services with no aud":     {chainConfig("a"), chainConfig("b")},
		"the reserved kubernetes name": {chainConfig(IssuerKubernetes)},
	}
	for name, configs := range cases {
		if _, err := NewServiceTokenChain(configs, testKey, nil); !errors.Is(err, ErrServiceTokenConfig) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestServiceTokenChainDoesNotChangeTheCallersList(t *testing.T) {
	configs := []ServiceTokenConfig{chainConfig(testIssuer)}
	if _, err := NewServiceTokenChain(configs, testKey, nil); err != nil {
		t.Fatal(err)
	}
	if configs[0].Authorize != "" || configs[0].MaxCacheSeconds != 0 {
		t.Fatalf("defaults leaked into the caller's list: %+v", configs[0])
	}
}

func TestNormalizeServiceTokensValidatesAndFillsDefaults(t *testing.T) {
	list := []ServiceTokenConfig{chainConfig(testIssuer)}
	if err := NormalizeServiceTokens(list); err != nil || list[0].Authorize != AuthorizeAnyAccepted || list[0].MaxCacheSeconds != 300 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	bad := chainConfig(testIssuer)
	bad.SubjectClaim = "not a path"
	if err := NormalizeServiceTokens([]ServiceTokenConfig{bad}); !errors.Is(err, ErrServiceTokenConfig) {
		t.Fatalf("err = %v", err)
	}
}
