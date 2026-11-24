/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const flagCRDs = "-crds"

func TestRunWritesTheSchemasAndListsThem(t *testing.T) {
	out := t.TempDir()
	var stdout bytes.Buffer
	if err := run([]string{flagCRDs, "../../config/crd/bases", "-out", out}, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "kubemoot_v1alpha1.json") {
		t.Fatalf("the combined schema is not listed: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(out, "crew_v1alpha1.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunRefusesBadInput(t *testing.T) {
	for name, args := range map[string][]string{
		"no -out":      {flagCRDs, "../../config/crd/bases"},
		"missing dir":  {flagCRDs, filepath.Join(t.TempDir(), "none"), "-out", t.TempDir()},
		"unknown flag": {"-nope"},
	} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
