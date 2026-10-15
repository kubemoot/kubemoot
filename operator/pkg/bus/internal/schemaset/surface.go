/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package schemaset

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Fact is what the contract says about one place in a schema: its JSON types, the
// values it may take, whether its object requires it, the schema it refers to, and
// the constraints on its value, by keyword.
type Fact struct {
	Type        string            `json:"type,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Ref         string            `json:"ref,omitempty"`
	Constraints map[string]string `json:"constraints,omitempty"`
}

// constraintKeywords narrow what a value may be. additionalProperties counts only as
// true or false; as a schema it is a place of its own.
var constraintKeywords = []string{
	"const", "format", "pattern", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"minLength", "maxLength", "minItems", "maxItems", "uniqueItems", "minProperties", "maxProperties",
	kwAdditionalProperties,
}

// Lock is the surface of one contract version: every place in every schema file, by
// path, with its facts. Descriptions are not part of it.
type Lock struct {
	Version string          `json:"version"`
	Surface map[string]Fact `json:"surface"`
}

// LockOf is the lock of the set's current schema.
func LockOf(s *Set) Lock {
	surface := map[string]Fact{}
	for _, name := range s.Names() {
		walkSurface(s.Schema(name), name+"#", false, surface)
	}
	return Lock{Version: s.Version, Surface: surface}
}

// walkSurface records the facts of schema node at path and of every schema below it.
func walkSurface(node map[string]any, path string, required bool, out map[string]Fact) {
	out[path] = factOf(node, required)
	req := map[string]bool{}
	for _, r := range asList(node["required"]) {
		req[fmt.Sprint(r)] = true
	}
	for _, c := range Children(node) {
		walkSurface(c.Schema, path+c.Suffix(), c.IsProperty() && req[c.Name], out)
	}
}

// factOf reads the facts of one schema node.
func factOf(node map[string]any, required bool) Fact {
	f := Fact{Required: required, Constraints: constraintsOf(node)}
	f.Ref, _ = node["$ref"].(string)
	switch t := node["type"].(type) {
	case string:
		f.Type = t
	case []any:
		f.Type = strings.Join(sortedStrings(t), ",")
	}
	if enum := asList(node["enum"]); len(enum) > 0 {
		f.Enum = sortedStrings(enum)
	}
	return f
}

// constraintsOf reads the constraint keywords of node, each value as JSON.
func constraintsOf(node map[string]any) map[string]string {
	var out map[string]string
	for _, kw := range constraintKeywords {
		v, ok := node[kw]
		if _, isSchema := v.(map[string]any); !ok || (kw == kwAdditionalProperties && isSchema) {
			continue
		}
		data, err := json.Marshal(v)
		if err != nil {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[kw] = string(data)
	}
	return out
}

func sortedStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, fmt.Sprint(v))
	}
	sort.Strings(out)
	return out
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// Changes compares an older surface with a newer one. A breaking change is one a
// reader or writer of the older version can trip over: a place removed, a type or
// reference changed, a place becoming required or optional, an enum value removed, a
// constraint added or changed, or a required field added to an object the older
// version has. An additive change is a new optional place, a new object (with what
// it requires), a new enum value, or a constraint removed. A strict reader that
// refuses unknown enum values must take a new value as breaking; readers of this
// contract ignore what they do not know.
func Changes(older, newer map[string]Fact) (breaking, additive []string) {
	for path, was := range older {
		now, ok := newer[path]
		if !ok {
			breaking = append(breaking, path+": removed")
			continue
		}
		b, a := factChanges(path, was, now)
		breaking, additive = append(breaking, b...), append(additive, a...)
	}
	for path, now := range newer {
		if _, ok := older[path]; ok {
			continue
		}
		if now.Required && hasParent(older, path) {
			breaking = append(breaking, path+": added as required")
		} else {
			additive = append(additive, path+": added")
		}
	}
	sort.Strings(breaking)
	sort.Strings(additive)
	return breaking, additive
}

// factChanges compares the facts of one place present in both surfaces.
func factChanges(path string, was, now Fact) (breaking, additive []string) {
	if was.Type != now.Type {
		breaking = append(breaking, fmt.Sprintf("%s: type %q became %q", path, was.Type, now.Type))
	}
	if was.Ref != now.Ref {
		breaking = append(breaking, fmt.Sprintf("%s: reference %q became %q", path, was.Ref, now.Ref))
	}
	if was.Required != now.Required {
		breaking = append(breaking, fmt.Sprintf("%s: required %t became %t", path, was.Required, now.Required))
	}
	b, a := constraintChanges(path, was.Constraints, now.Constraints)
	breaking, additive = append(breaking, b...), append(additive, a...)
	removed, added := diffSets(was.Enum, now.Enum)
	if len(removed) > 0 {
		breaking = append(breaking, fmt.Sprintf("%s: enum values removed %v", path, removed))
	}
	if len(added) > 0 {
		additive = append(additive, fmt.Sprintf("%s: enum values added %v", path, added))
	}
	return breaking, additive
}

// hasParent reports whether the object holding the property at path is in surface.
func hasParent(surface map[string]Fact, path string) bool {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return false
	}
	_, ok := surface[path[:i]]
	return ok
}

// constraintChanges compares the constraints of one place: a constraint added or
// changed narrows what is valid, one removed widens it.
func constraintChanges(path string, was, now map[string]string) (breaking, additive []string) {
	for kw, v := range now {
		old, ok := was[kw]
		switch {
		case !ok:
			breaking = append(breaking, fmt.Sprintf("%s: %s %s added", path, kw, v))
		case old != v:
			breaking = append(breaking, fmt.Sprintf("%s: %s %s became %s", path, kw, old, v))
		}
	}
	for kw, v := range was {
		if _, ok := now[kw]; !ok {
			additive = append(additive, fmt.Sprintf("%s: %s %s removed", path, kw, v))
		}
	}
	return breaking, additive
}

// diffSets lists the values only in a and the values only in b.
func diffSets(a, b []string) (onlyA, onlyB []string) {
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, v := range a {
		inA[v] = true
	}
	for _, v := range b {
		inB[v] = true
		if !inA[v] {
			onlyB = append(onlyB, v)
		}
	}
	for _, v := range a {
		if !inB[v] {
			onlyA = append(onlyA, v)
		}
	}
	return onlyA, onlyB
}

// CheckUpdate reports whether the lock may move from older to newer: a breaking
// change needs a higher major version. It returns the breaking changes it refuses.
func CheckUpdate(older, newer Lock) error {
	breaking, _ := Changes(older.Surface, newer.Surface)
	if len(breaking) == 0 {
		return nil
	}
	oldMajor, err := major(older.Version)
	if err != nil {
		return err
	}
	newMajor, err := major(newer.Version)
	if err != nil {
		return err
	}
	if newMajor > oldMajor {
		return nil
	}
	return fmt.Errorf(
		"breaking changes need a new major contract version (info.version in %s is %s, the lock's is %s):\n  %s",
		AsyncAPIFile, newer.Version, older.Version, strings.Join(breaking, "\n  "))
}

// major is the major number of a version X.Y.Z.
func major(version string) (int, error) {
	head, _, _ := strings.Cut(version, ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0, fmt.Errorf("contract version %q is not X.Y.Z", version)
	}
	return n, nil
}

// Equal reports whether two locks describe the same version and surface.
func Equal(a, b Lock) bool {
	return a.Version == b.Version && reflect.DeepEqual(a.Surface, b.Surface)
}
