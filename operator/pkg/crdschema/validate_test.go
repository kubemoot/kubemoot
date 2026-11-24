/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crdschema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"
)

// combinedValidator compiles the combined schema of the repository's CRDs.
func combinedValidator(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schemas, err := ReadDir("../../config/crd/bases")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Combined(schemas))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	if err := c.AddResource("kubemoot.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("kubemoot.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func documents(t *testing.T, text string) []any {
	t.Helper()
	var out []any
	for _, part := range strings.Split(text, "\n---") {
		var v any
		if err := yaml.Unmarshal([]byte(part), &v); err != nil {
			t.Fatal(err)
		}
		if v != nil {
			out = append(out, v)
		}
	}
	return out
}

// The hello crew the quickstart applies is real input a Kubemoot schema must accept.
func TestQuickstartCrewValidates(t *testing.T) {
	s := combinedValidator(t)
	files, _ := filepath.Glob("../../../quickstart/crew/*.yaml")
	if len(files) == 0 {
		t.Fatal("no quickstart manifests found")
	}
	for _, f := range files {
		text, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, d := range documents(t, string(text)) {
			if err := s.Validate(d); err != nil {
				t.Errorf("%s document %d: %v", filepath.Base(f), i, err)
			}
		}
	}
}

func TestMisspelledAndMissingFieldsFail(t *testing.T) {
	s := combinedValidator(t)
	bad := map[string]string{
		"misspelled spec field": "apiVersion: kubemoot.ai/v1alpha1\nkind: Crew\nmetadata: {name: a}\nspec:\n  descriptionz: x\n",
		"wrong apiVersion":      "apiVersion: kubemoot.ai/v1\nkind: Crew\nmetadata: {name: a}\nspec: {}\n",
		"no metadata":           "apiVersion: kubemoot.ai/v1alpha1\nkind: Crew\nspec: {}\n",
	}
	for name, text := range bad {
		if err := s.Validate(documents(t, text)[0]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	other := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: a}\ndata: {k: v}\n"
	if err := s.Validate(documents(t, other)[0]); err != nil {
		t.Errorf("a kind of another group is checked: %v", err)
	}
}
