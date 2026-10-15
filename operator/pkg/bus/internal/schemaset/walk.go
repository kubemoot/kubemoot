/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package schemaset

import (
	"maps"
	"slices"
	"strconv"
)

// The JSON Schema keywords whose values hold subschemas, as the contract uses them.
const (
	kwProperties           = "properties"
	kwDefinitions          = "definitions"
	kwItems                = "items"
	kwAdditionalProperties = "additionalProperties"
)

// namedKeywords hold a map of subschemas by name; singleKeywords hold one subschema;
// listKeywords hold a list of subschemas.
var (
	namedKeywords  = []string{kwProperties, kwDefinitions}
	singleKeywords = []string{kwItems, kwAdditionalProperties}
	listKeywords   = []string{"allOf", "oneOf", "anyOf"}
)

// Child is one subschema directly below a schema node, and where it sits.
type Child struct {
	// Keyword is the keyword that holds it.
	Keyword string
	// Name is its property or definition name, under properties or definitions.
	Name string
	// Index is its position under allOf, oneOf or anyOf.
	Index  int
	Schema map[string]any
}

// IsProperty reports whether the child is the schema of an object property.
func (c Child) IsProperty() bool { return c.Keyword == kwProperties }

// Suffix extends the parent's surface path to the child: /<property>,
// /definitions/<name>, [] for items, {} for additionalProperties, and
// /<keyword>/<index> for allOf, oneOf and anyOf.
func (c Child) Suffix() string {
	switch c.Keyword {
	case kwProperties:
		return "/" + c.Name
	case kwDefinitions:
		return "/definitions/" + c.Name
	case kwItems:
		return "[]"
	case kwAdditionalProperties:
		return "{}"
	default:
		return "/" + c.Keyword + "/" + strconv.Itoa(c.Index)
	}
}

// Children lists the subschemas directly below node. Values of other keywords, such
// as enum or default, are data and never schemas.
func Children(node map[string]any) []Child {
	var out []Child
	for _, kw := range namedKeywords {
		for name, sub := range asMap(node[kw]) {
			if m, ok := sub.(map[string]any); ok {
				out = append(out, Child{Keyword: kw, Name: name, Schema: m})
			}
		}
	}
	for _, kw := range singleKeywords {
		if m, ok := node[kw].(map[string]any); ok {
			out = append(out, Child{Keyword: kw, Schema: m})
		}
	}
	for _, kw := range listKeywords {
		for i, sub := range asList(node[kw]) {
			if m, ok := sub.(map[string]any); ok {
				out = append(out, Child{Keyword: kw, Index: i, Schema: m})
			}
		}
	}
	return out
}

// Rewrite returns a copy of node with every direct subschema replaced by fn's result.
// fn decides whether to rewrite further down. node is not changed.
func Rewrite(node map[string]any, fn func(Child) map[string]any) map[string]any {
	out := maps.Clone(node)
	for _, kw := range namedKeywords {
		if m := asMap(node[kw]); m != nil {
			out[kw] = maps.Clone(m)
		}
	}
	for _, kw := range listKeywords {
		if l := asList(node[kw]); l != nil {
			out[kw] = slices.Clone(l)
		}
	}
	for _, c := range Children(node) {
		replace(out, c, fn(c))
	}
	return out
}

// replace puts sub where child c sits in out, whose containers Rewrite already copied.
func replace(out map[string]any, c Child, sub map[string]any) {
	switch {
	case slices.Contains(namedKeywords, c.Keyword):
		out[c.Keyword].(map[string]any)[c.Name] = sub
	case slices.Contains(listKeywords, c.Keyword):
		out[c.Keyword].([]any)[c.Index] = sub
	default:
		out[c.Keyword] = sub
	}
}

// Refs lists every $ref in node and the schemas below it.
func Refs(node map[string]any) []string {
	var out []string
	if ref, ok := node["$ref"].(string); ok {
		out = append(out, ref)
	}
	for _, c := range Children(node) {
		out = append(out, Refs(c.Schema)...)
	}
	return out
}
