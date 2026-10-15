/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package schemaset

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// walkSchema has a subschema under every keyword that holds one, a property named
// properties, and an enum value that is an object, which is data and not a schema.
const walkSchema = `{
  "properties": {
    "properties": {"type": "string"},
    "list": {"type": "array", "items": {"$ref": "item.json"}},
    "kind": {"enum": [{"type": "object"}]}
  },
  "definitions": {"D": {"type": "object"}},
  "additionalProperties": {"type": "integer"},
  "oneOf": [{"$ref": "a.json"}, {"type": "null"}]
}`

func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestChildrenListsSubschemasByKeyword(t *testing.T) {
	children := Children(parse(t, walkSchema))
	got := make([]string, 0, len(children))
	for _, c := range children {
		got = append(got, c.Suffix())
	}
	sort.Strings(got)
	want := []string{"/definitions/D", "/kind", "/list", "/oneOf/0", "/oneOf/1", "/properties", "{}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("children %v, want %v", got, want)
	}
	if len(Children(map[string]any{"enum": []any{map[string]any{"type": jsonObject}}})) != 0 {
		t.Fatal("an enum value was taken for a schema")
	}
}

func TestRewriteReplacesOnlySubschemasAndCopies(t *testing.T) {
	node := parse(t, walkSchema)
	mark := func(c Child) map[string]any {
		out := Rewrite(c.Schema, func(c Child) map[string]any { return map[string]any{"seen": c.Suffix()} })
		out["seen"] = c.Suffix()
		return out
	}
	out := Rewrite(node, mark)
	props := out["properties"].(map[string]any)
	if props["properties"].(map[string]any)["seen"] != "/properties" {
		t.Errorf("the property named properties was not rewritten: %v", props["properties"])
	}
	if props["list"].(map[string]any)["items"].(map[string]any)["seen"] != "[]" {
		t.Errorf("items below a property were not rewritten: %v", props["list"])
	}
	if out["oneOf"].([]any)[1].(map[string]any)["seen"] != "/oneOf/1" {
		t.Errorf("oneOf was not rewritten: %v", out["oneOf"])
	}
	if _, touched := props["kind"].(map[string]any)["enum"].([]any)[0].(map[string]any)["seen"]; touched {
		t.Error("an enum value was rewritten")
	}
	if _, changed := node["properties"].(map[string]any)["properties"].(map[string]any)["seen"]; changed {
		t.Error("Rewrite changed its input")
	}
}

func TestRefsFindsEveryReference(t *testing.T) {
	got := Refs(parse(t, walkSchema))
	sort.Strings(got)
	if want := []string{"a.json", "item.json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("refs %v, want %v", got, want)
	}
}
