/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crdschema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const widgetKind = "Widget"

const widgetCRD = `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  group: example.org
  names:
    kind: Widget
  versions:
  - name: v1
    served: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            x-kubernetes-validations:
            - rule: "self.size > 0"
            properties:
              size:
                x-kubernetes-int-or-string: true
              note:
                type: string
                nullable: true
                enum: [a, b]
              labels:
                type: object
                x-kubernetes-preserve-unknown-fields: true
              items:
                type: array
                items:
                  type: object
                  properties:
                    name: {type: string}
  - name: v0
    served: false
    schema:
      openAPIV3Schema: {type: object}
`

func widget(t *testing.T) Schema {
	t.Helper()
	got, err := FromCRD([]byte(widgetCRD))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want only the served version, got %d schemas", len(got))
	}
	return got[0]
}

func at(t *testing.T, doc map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := doc
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			t.Fatalf("no %q on the way to %v in %v", p, path, cur)
		}
		cur = next
	}
	return cur
}

func TestFromCRDNamesTheKindAndFile(t *testing.T) {
	s := widget(t)
	if s.Kind != widgetKind || s.APIVersion != "example.org/v1" || s.Version != "v1" || s.File != "widget_v1.json" {
		t.Fatalf("unexpected identity %+v", s)
	}
	props := at(t, s.Doc, kwProperties)
	if at(t, props, "apiVersion")[kwConst] != "example.org/v1" || at(t, props, kwKind)[kwConst] != widgetKind {
		t.Fatalf("apiVersion and kind are not pinned: %v", props)
	}
}

func TestMisspelledFieldsAreErrors(t *testing.T) {
	s := widget(t)
	if s.Doc[kwAddlProps] != false {
		t.Fatalf("the root accepts unknown fields: %v", s.Doc[kwAddlProps])
	}
	if at(t, s.Doc, kwProperties, "spec")[kwAddlProps] != false {
		t.Fatal("spec accepts unknown fields")
	}
	item := at(t, s.Doc, kwProperties, "spec", kwProperties, "items", "items")
	if item[kwAddlProps] != false {
		t.Fatal("array items accept unknown fields")
	}
}

func TestKubernetesExtensionsBecomeJSONSchema(t *testing.T) {
	spec := at(t, widget(t).Doc, kwProperties, "spec")
	if _, ok := spec["x-kubernetes-validations"]; ok {
		t.Fatal("a CEL rule is left in the schema")
	}
	size := at(t, spec, kwProperties, "size")
	if _, ok := size["anyOf"].([]any); !ok || size["x-kubernetes-int-or-string"] != nil {
		t.Fatalf("int-or-string is not integer or string: %v", size)
	}
	if n := at(t, spec, kwProperties, "note"); n["type"].([]any)[1] != "null" || n["nullable"] != nil || n["enum"].([]any)[2] != nil {
		t.Fatalf("nullable is not a null type: %v", n)
	}
	if l := at(t, spec, kwProperties, "labels"); l[kwAddlProps] != true || l["x-kubernetes-preserve-unknown-fields"] != nil {
		t.Fatalf("preserve-unknown-fields is not an open object: %v", l)
	}
}

func TestCombinedPicksTheSchemaByKind(t *testing.T) {
	s := widget(t)
	c := Combined([]Schema{s})
	rules := c["allOf"].([]any)
	if len(rules) != 1 {
		t.Fatalf("want one rule per kind, got %d", len(rules))
	}
	rule := rules[0].(map[string]any)
	cond := at(t, rule, "if", kwProperties, kwKind)
	if cond[kwConst] != widgetKind || at(t, rule, "then")["$ref"] != "#/definitions/Widget_v1" {
		t.Fatalf("rule does not select Widget: %v", rule)
	}
	def := at(t, c, "definitions", "Widget_v1")
	if _, ok := def["$schema"]; ok {
		t.Fatal("a definition repeats $schema")
	}
}

func TestFromCRDRejectsWhatIsNotACRD(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":     "",
		"no kind":   "spec:\n  group: example.org\n",
		"not yaml":  "{{{",
		"no group":  "spec:\n  names:\n    kind: Widget\n",
		"bad shape": "spec: [1, 2]\n",
	} {
		if _, err := FromCRD([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadDirRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := ReadDir(t.TempDir()); err == nil {
		t.Fatal("an empty directory was accepted")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("spec: [1]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(dir); err == nil || !strings.Contains(err.Error(), "bad.yaml") {
		t.Fatalf("a broken file is not named in the error: %v", err)
	}
}

// The generated CRDs of this repository are the real input: every kind converts, the
// output is valid JSON, and the same input gives byte-identical files.
func TestRealCRDsConvertAndAreDeterministic(t *testing.T) {
	schemas, err := ReadDir("../../config/crd/bases")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range schemas {
		seen[s.Kind] = true
	}
	for _, kind := range []string{"Crew", "Agent", "PromptModule", "CrewFitnessSuite", "MCPServer"} {
		if !seen[kind] {
			t.Errorf("no schema for %s", kind)
		}
	}
	assertStableFiles(t, schemas)
}

func assertStableFiles(t *testing.T, schemas []Schema) {
	t.Helper()
	a, b := t.TempDir(), t.TempDir()
	names, err := WriteDir(schemas, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteDir(schemas, b); err != nil {
		t.Fatal(err)
	}
	if len(names) != len(schemas)+1 || names[len(names)-1] != CombinedName {
		t.Fatalf("want one file per kind plus %s, got %v", CombinedName, names)
	}
	for _, n := range names {
		first, err := os.ReadFile(filepath.Join(a, n))
		if err != nil {
			t.Fatal(err)
		}
		second, _ := os.ReadFile(filepath.Join(b, n))
		if string(first) != string(second) || !json.Valid(first) {
			t.Errorf("%s is not valid, stable JSON", n)
		}
	}
}
