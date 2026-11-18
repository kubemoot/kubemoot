/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package secretscreen

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type redactCase struct {
	Name     string   `json:"name"`
	Input    string   `json:"input"`
	Secrets  []string `json:"secrets"`
	Expected string   `json:"expected"`
}

func loadCases(t *testing.T) []redactCase {
	t.Helper()
	raw, err := os.ReadFile("../../../testdata/redact-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []redactCase
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("redact-cases.json: %v (%d cases)", err, len(cases))
	}
	return cases
}

func TestRedactMatchesTheSharedCases(t *testing.T) {
	for _, c := range loadCases(t) {
		if got := Redact(c.Input, c.Secrets...); got != c.Expected {
			t.Errorf("%s: %q -> %q, want %q", c.Name, c.Input, got, c.Expected)
		}
	}
}

// tighter lists the shared cases the export screen deliberately does not flag
// although redaction changes them, with the reason.
var tighter = map[string]string{
	"token assignment": "a value under eight characters is not a credential worth leaving a fact out for",
}

// Find agrees with the shared cases except where it is deliberately tighter. Cases
// that name exact secrets test Redact's second argument, which Find does not take.
func TestFindAgreesWithTheSharedCasesExceptWhereTighter(t *testing.T) {
	checked := 0
	for _, c := range loadCases(t) {
		if len(c.Secrets) > 0 {
			continue
		}
		checked++
		_, found := Find(c.Input)
		want := c.Input != c.Expected
		if _, ok := tighter[c.Name]; ok {
			want = false
		}
		if found != want {
			t.Errorf("%s: Find(%q) found = %v, want %v", c.Name, c.Input, found, want)
		}
	}
	if checked == 0 {
		t.Fatal("no shared case was checked")
	}
}

func TestFindNamesTheShapeAndNeverTheSecret(t *testing.T) {
	cases := map[string]string{
		"password: hunter2hunter2":                          "key, token, secret or password assignment",
		"call with Bearer abcdefgh12345678":                 "bearer or basic token",
		"stored ghp_abcdefghijklmnopqrstuvwxyz0123 for ci":  "GitHub token",
		"deploy key AKIAABCDEFGHIJKLMNOP":                   "AWS access key id",
		"digest 0123456789abcdef0123456789abcdef0123456789": "long hexadecimal string",
	}
	for text, want := range cases {
		got, found := Find(text)
		if !found || got != want {
			t.Errorf("Find(%q) = %q, %v, want %q", text, got, found, want)
		}
		if strings.Contains(got, "hunter2") || strings.Contains(got, "abcdefgh") {
			t.Errorf("shape name %q leaks the text", got)
		}
	}
}

func TestFindLeavesOrdinaryMemoryAlone(t *testing.T) {
	for _, text := range []string{
		"", "RTX 5090, 32 GB", "the node rebooted at 02:10 after the kernel update",
		"tokens used 12 of 4096", "the cluster has three control-plane nodes",
		"fixed in commit 5fd045c5a492fd51aca4a65cdf817d5a9f37c8a1 on main",
		"image ghcr.io/kubemoot/operator@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"digest sha1:0123456789abcdef0123456789abcdef01234567",
		"basic authentication enabled on the registry",
		"max_tokens: 4096", "secret: none", "password: unset",
		"uses sk-learn-preprocessing-module for scaling",
	} {
		if name, found := Find(text); found {
			t.Errorf("Find(%q) = %q, want no match", text, name)
		}
	}
}

func TestFindStillCatchesTheTightenedShapesWhenTheyLookReal(t *testing.T) {
	for _, text := range []string{
		"Basic dXNlcjpwYXNz",
		"Bearer Abcdefghijkl",
		"registry login: Basic abcdefghijklmnopqrstuvwxyz",
		"api key 5fd045c5a492fd51aca4a65cdf817d5a9f37c8a1",
		"secret=\"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"",
		"token: s3cr3tvalue",
		"OPENAI_API_KEY is sk-proj-abc123def456ghi789",
	} {
		if _, found := Find(text); !found {
			t.Errorf("Find(%q) found nothing, want a credential", text)
		}
	}
}
