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

// A case the redaction changes holds a secret shape; one it leaves alone does not.
// Cases that name exact secrets test the redactor's second argument, which the
// screen does not take, so they are skipped.
func TestFindAgreesWithTheSharedRedactionCases(t *testing.T) {
	checked := 0
	for _, c := range loadCases(t) {
		if len(c.Secrets) > 0 {
			continue
		}
		checked++
		_, found := Find(c.Input)
		if want := c.Input != c.Expected; found != want {
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
	} {
		if name, found := Find(text); found {
			t.Errorf("Find(%q) = %q, want no match", text, name)
		}
	}
}
