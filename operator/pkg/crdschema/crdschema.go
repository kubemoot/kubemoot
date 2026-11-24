/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package crdschema turns the generated CustomResourceDefinitions into JSON Schema
// (draft-07), one schema per kind and one combined schema that picks the schema of
// each YAML document by its kind. Editors (Red Hat YAML in VS Code, any LSP client)
// and CI (kubeconform, ajv) validate Kubemoot manifests offline with them; no
// cluster is needed. The schemas are a release asset of every Kubemoot release.
package crdschema

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	draft07 = "http://json-schema.org/draft-07/schema#"
	// CombinedName is the file name of the schema that covers every kind.
	CombinedName = "kubemoot_v1alpha1.json"

	kwConst      = "const"
	kwKind       = "kind"
	kwProperties = "properties"
	kwAddlProps  = "additionalProperties"
	kwType       = "type"
	typeObject   = "object"
	typeString   = "string"
)

// Schema is the JSON Schema of one kind at one API version.
type Schema struct {
	Kind       string
	APIVersion string // group/version, as a manifest writes it
	Version    string // the version part of APIVersion
	File       string // file name, <kind lower case>_<version>.json
	Doc        map[string]any
}

// Marshal is the schema as indented JSON with sorted keys and a final newline.
func (s Schema) Marshal() ([]byte, error) { return marshal(s.Doc) }

func marshal(doc map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

type crd struct {
	Spec struct {
		Group string `json:"group"`
		Names struct {
			Kind string `json:"kind"`
		} `json:"names"`
		Versions []struct {
			Name   string `json:"name"`
			Served bool   `json:"served"`
			Schema struct {
				OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
			} `json:"schema"`
		} `json:"versions"`
	} `json:"spec"`
}

// FromCRD converts one CustomResourceDefinition document into one Schema per served version.
func FromCRD(doc []byte) ([]Schema, error) {
	var c crd
	if err := yaml.Unmarshal(doc, &c); err != nil {
		return nil, fmt.Errorf("not a CustomResourceDefinition: %w", err)
	}
	kind := c.Spec.Names.Kind
	if kind == "" || c.Spec.Group == "" {
		return nil, fmt.Errorf("the CustomResourceDefinition names no group or kind")
	}
	var out []Schema
	for _, v := range c.Spec.Versions {
		if !v.Served || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		apiVersion := c.Spec.Group + "/" + v.Name
		out = append(out, Schema{
			Kind:       kind,
			APIVersion: apiVersion,
			Version:    v.Name,
			File:       strings.ToLower(kind) + "_" + v.Name + ".json",
			Doc:        rootSchema(kind, apiVersion, v.Schema.OpenAPIV3Schema),
		})
	}
	return out, nil
}

// rootSchema is the schema of one object of the kind: the converted OpenAPI schema with
// apiVersion and kind pinned and a typed metadata.
func rootSchema(kind, apiVersion string, openapi map[string]any) map[string]any {
	root := convert(openapi)
	root["$schema"] = draft07
	root["title"] = kind
	props, _ := root[kwProperties].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["apiVersion"] = map[string]any{kwConst: apiVersion, kwType: typeString}
	props[kwKind] = map[string]any{kwConst: kind, kwType: typeString}
	props["metadata"] = metadataSchema()
	root[kwProperties] = props
	root["required"] = []any{"apiVersion", kwKind, "metadata"}
	root[kwAddlProps] = false
	return root
}

// metadataSchema is the part of ObjectMeta a manifest writes; the rest is the cluster's.
func metadataSchema() map[string]any {
	str := map[string]any{kwType: typeString}
	strMap := map[string]any{kwType: typeObject, kwAddlProps: str}
	return map[string]any{
		kwType: typeObject,
		kwProperties: map[string]any{
			"name":         str,
			"generateName": str,
			"namespace":    str,
			"labels":       strMap,
			"annotations":  strMap,
			"finalizers":   map[string]any{kwType: "array", "items": str},
		},
		kwAddlProps: true,
	}
}

// dropped are the Kubernetes extensions a JSON Schema validator has no use for.
var dropped = []string{
	"x-kubernetes-validations", "x-kubernetes-list-type", "x-kubernetes-list-map-keys",
	"x-kubernetes-map-type", "x-kubernetes-embedded-resource",
}

// subSchemaMaps are the keywords whose value maps names to schemas.
var subSchemaMaps = []string{kwProperties, "patternProperties", "definitions"}

// subSchemaLists are the keywords whose value lists schemas.
var subSchemaLists = []string{"allOf", "anyOf", "oneOf"}

// convert rewrites one OpenAPI v3 schema node, and every node below it, as draft-07.
func convert(node map[string]any) map[string]any {
	out := make(map[string]any, len(node))
	for k, v := range node {
		out[k] = v
	}
	for _, k := range dropped {
		delete(out, k)
	}
	applyIntOrString(out)
	applyPreserveUnknown(out)
	applyNullable(out)
	convertChildren(out)
	closeObject(out)
	return out
}

func applyIntOrString(n map[string]any) {
	if v, _ := n["x-kubernetes-int-or-string"].(bool); v {
		delete(n, "type")
		n["anyOf"] = []any{map[string]any{"type": "integer"}, map[string]any{kwType: typeString}}
	}
	delete(n, "x-kubernetes-int-or-string")
}

// applyPreserveUnknown opens an object the CRD keeps unknown fields of; closeObject
// closes every other object that lists properties.
func applyPreserveUnknown(n map[string]any) {
	if v, _ := n["x-kubernetes-preserve-unknown-fields"].(bool); v {
		if _, set := n[kwAddlProps]; !set {
			n[kwAddlProps] = true
		}
	}
	delete(n, "x-kubernetes-preserve-unknown-fields")
}

func applyNullable(n map[string]any) {
	if v, _ := n["nullable"].(bool); v {
		if t, ok := n["type"].(string); ok {
			n["type"] = []any{t, "null"}
		}
		if e, ok := n["enum"].([]any); ok {
			n["enum"] = append(append([]any(nil), e...), nil)
		}
	}
	delete(n, "nullable")
}

func convertChildren(n map[string]any) {
	for _, k := range subSchemaMaps {
		if m, ok := n[k].(map[string]any); ok {
			n[k] = convertAll(m)
		}
	}
	for _, k := range []string{"items", kwAddlProps, "not"} {
		if m, ok := n[k].(map[string]any); ok {
			n[k] = convert(m)
		}
	}
	for _, k := range subSchemaLists {
		if l, ok := n[k].([]any); ok {
			n[k] = convertList(l)
		}
	}
}

func convertAll(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for name, v := range m {
		out[name] = convertValue(v)
	}
	return out
}

func convertList(l []any) []any {
	out := make([]any, len(l))
	for i, v := range l {
		out[i] = convertValue(v)
	}
	return out
}

// convertValue converts v when it is a schema and passes anything else through.
func convertValue(v any) any {
	if sub, ok := v.(map[string]any); ok {
		return convert(sub)
	}
	return v
}

// closeObject makes a misspelled field an error: an object that lists its properties
// and does not say what else it holds accepts nothing else.
func closeObject(n map[string]any) {
	if _, ok := n[kwProperties]; !ok {
		return
	}
	if _, set := n[kwAddlProps]; !set {
		n[kwAddlProps] = false
	}
}

// Combined is one schema for every kind: each document is checked against the schema
// of its kind and a document of any other kind passes, so one mapping covers a crew
// repository's whole set of files.
func Combined(schemas []Schema) map[string]any {
	sorted := append([]Schema(nil), schemas...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].File < sorted[j].File })
	defs := make(map[string]any, len(sorted))
	rules := make([]any, 0, len(sorted))
	for _, s := range sorted {
		def := make(map[string]any, len(s.Doc))
		for k, v := range s.Doc {
			if k != "$schema" {
				def[k] = v
			}
		}
		name := s.Kind + "_" + s.Version
		defs[name] = def
		rules = append(rules, map[string]any{
			"if": map[string]any{
				kwProperties: map[string]any{kwKind: map[string]any{kwConst: s.Kind}},
				"required":   []any{kwKind},
			},
			"then": map[string]any{"$ref": "#/definitions/" + name},
		})
	}
	return map[string]any{
		"$schema":     draft07,
		"title":       "Kubemoot kubemoot.ai manifests",
		"description": "Every Kubemoot kind; a document is checked against the schema of its kind.",
		"type":        "object",
		"allOf":       rules,
		"definitions": defs,
	}
}

// ReadDir converts every CustomResourceDefinition file (*.yaml) of dir.
func ReadDir(dir string) ([]Schema, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no CustomResourceDefinition files (*.yaml) in %s", dir)
	}
	sort.Strings(files)
	var all []Schema
	for _, f := range files {
		doc, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s, err := FromCRD(doc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		all = append(all, s...)
	}
	return all, nil
}

// WriteDir writes each schema and the combined schema into out, and returns the file names.
func WriteDir(schemas []Schema, out string) ([]string, error) {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(schemas)+1)
	for _, s := range schemas {
		b, err := s.Marshal()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(out, s.File), b, 0o644); err != nil {
			return nil, err
		}
		names = append(names, s.File)
	}
	b, err := marshal(Combined(schemas))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, CombinedName), b, 0o644); err != nil {
		return nil, err
	}
	return append(names, CombinedName), nil
}
