/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package schemaset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsonString is the JSON Schema type string.
const jsonString = "string"

// jsonObject is the JSON Schema type object.
const jsonObject = "object"

// idString is the id property of testSchema.
const idString = `"id": {"type": "string"}`

// fileA is a schema file name.
const fileA = "a.json"

const testSchema = `{
  "title": "Msg",
  "type": "object",
  "required": ["id"],
  "properties": {
    "id": {"type": "string"},
    "kind": {"type": "string", "enum": ["a", "b"]},
    "tags": {"type": "array", "items": {"type": "string"}},
    "meta": {"$ref": "meta.json"}
  }
}`

// writeSet writes a schema directory with the AsyncAPI version and msg.json.
func writeSet(t *testing.T, version, schema string) *Set {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		AsyncAPIFile: "asyncapi: 3.1.0\ninfo:\n  title: t\n  version: " + version + "\n",
		"msg.json":   schema,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLockRecordsEveryPlace(t *testing.T) {
	lock := LockOf(writeSet(t, "1.0.0", testSchema))
	if lock.Version != "1.0.0" {
		t.Fatalf("version %q", lock.Version)
	}
	want := map[string]Fact{
		"msg.json#":        {Type: jsonObject},
		"msg.json#/id":     {Type: jsonString, Required: true},
		"msg.json#/kind":   {Type: jsonString, Enum: []string{"a", "b"}},
		"msg.json#/tags":   {Type: "array"},
		"msg.json#/tags[]": {Type: jsonString},
		"msg.json#/meta":   {Ref: "meta.json"},
	}
	if !Equal(lock, Lock{Version: "1.0.0", Surface: want}) {
		got, _ := json.MarshalIndent(lock.Surface, "", " ")
		t.Fatalf("surface:\n%s", got)
	}
}

func TestChangesSortsBreakingFromAdditive(t *testing.T) {
	older := LockOf(writeSet(t, "1.0.0", testSchema)).Surface
	cases := []struct {
		name, from, to string
		breaking       bool
	}{
		{"a new optional field", `"id": {"type": "string"},`, `"id": {"type": "string"}, "extra": {"type": "integer"},`, false},
		{"a new enum value", `["a", "b"]`, `["a", "b", "c"]`, false},
		{"a removed field", `"tags": {"type": "array", "items": {"type": "string"}},`, ``, true},
		{"a changed type", idString, `"id": {"type": "integer"}`, true},
		{"a removed enum value", `["a", "b"]`, `["a"]`, true},
		{"a field becoming required", `"required": ["id"]`, `"required": ["id", "kind"]`, true},
		{"a field becoming optional", `"required": ["id"]`, `"required": []`, true},
		{"a changed reference", `"meta.json"`, `"other.json"`, true},
	}
	for _, c := range cases {
		schema := strings.Replace(testSchema, c.from, c.to, 1)
		newer := LockOf(writeSet(t, "1.0.0", schema)).Surface
		breaking, additive := Changes(older, newer)
		if (len(breaking) > 0) != c.breaking {
			t.Errorf("%s: breaking %v, additive %v", c.name, breaking, additive)
		}
		if !c.breaking && len(additive) == 0 {
			t.Errorf("%s: no additive change found", c.name)
		}
	}
}

func TestANewRequiredFieldIsBreaking(t *testing.T) {
	older := LockOf(writeSet(t, "1.0.0", testSchema)).Surface
	schema := strings.Replace(testSchema, `"required": ["id"]`, `"required": ["id", "added"]`, 1)
	schema = strings.Replace(schema, `"id": {"type": "string"},`, `"id": {"type": "string"}, "added": {"type": "string"},`, 1)
	breaking, _ := Changes(older, LockOf(writeSet(t, "1.0.0", schema)).Surface)
	if len(breaking) != 1 || !strings.Contains(breaking[0], "added as required") {
		t.Fatalf("breaking %v", breaking)
	}
}

func TestANewObjectIsAdditiveWithWhatItRequires(t *testing.T) {
	older := LockOf(writeSet(t, "1.0.0", testSchema)).Surface
	schema := strings.Replace(testSchema, `"id": {"type": "string"},`,
		`"id": {"type": "string"}, "box": {"type": "object", "required": ["k"], "properties": {"k": {"type": "string"}}},`, 1)
	newer := LockOf(writeSet(t, "1.0.0", schema)).Surface
	newer["new.json#"] = Fact{Type: jsonObject}
	newer["new.json#/must"] = Fact{Type: jsonString, Required: true}
	breaking, additive := Changes(older, newer)
	if len(breaking) != 0 || len(additive) != 4 {
		t.Fatalf("breaking %v, additive %v", breaking, additive)
	}
}

func TestConstraintsNarrowingIsBreakingAndWideningAdditive(t *testing.T) {
	older := LockOf(writeSet(t, "1.0.0", testSchema)).Surface
	cases := []struct {
		name, from, to string
		breaking       bool
	}{
		{"a pattern added", idString, `"id": {"type": "string", "pattern": "^a"}`, true},
		{"a const object added", idString, `"id": {"type": "string", "const": {"a": 1}}`, true},
		{"closed to unknown fields", `"title": "Msg",`, `"title": "Msg", "additionalProperties": false,`, true},
	}
	for _, c := range cases {
		narrowed := LockOf(writeSet(t, "1.0.0", strings.Replace(testSchema, c.from, c.to, 1))).Surface
		if breaking, _ := Changes(older, narrowed); (len(breaking) > 0) != c.breaking {
			t.Errorf("%s: breaking %v", c.name, breaking)
		}
		breaking, additive := Changes(narrowed, older)
		if len(breaking) != 0 || len(additive) != 1 {
			t.Errorf("%s removed: breaking %v, additive %v", c.name, breaking, additive)
		}
	}
	changed := strings.Replace(testSchema, idString, `"id": {"type": "string", "pattern": "^b"}`, 1)
	patterned := strings.Replace(testSchema, idString, `"id": {"type": "string", "pattern": "^a"}`, 1)
	if breaking, _ := Changes(LockOf(writeSet(t, "1.0.0", patterned)).Surface,
		LockOf(writeSet(t, "1.0.0", changed)).Surface); len(breaking) != 1 {
		t.Errorf("a changed pattern: breaking %v", breaking)
	}
	schemaAdditional := strings.Replace(testSchema, `"title": "Msg",`, `"title": "Msg", "additionalProperties": {"type": "string"},`, 1)
	if f := LockOf(writeSet(t, "1.0.0", schemaAdditional)).Surface["msg.json#"]; f.Constraints != nil {
		t.Errorf("an additionalProperties schema was taken for a constraint: %v", f.Constraints)
	}
}

func TestCheckUpdateNeedsAMajorVersionForABreakingChange(t *testing.T) {
	older := LockOf(writeSet(t, "1.2.0", testSchema))
	broken := strings.Replace(testSchema, idString, `"id": {"type": "integer"}`, 1)
	if err := CheckUpdate(older, LockOf(writeSet(t, "1.3.0", broken))); err == nil {
		t.Fatal("a breaking change under the same major version was accepted")
	}
	if err := CheckUpdate(older, LockOf(writeSet(t, "2.0.0", broken))); err != nil {
		t.Fatalf("a breaking change with a new major version was refused: %v", err)
	}
	added := strings.Replace(testSchema, `["a", "b"]`, `["a", "b", "c"]`, 1)
	if err := CheckUpdate(older, LockOf(writeSet(t, "1.2.0", added))); err != nil {
		t.Fatalf("an additive change was refused: %v", err)
	}
	if err := CheckUpdate(Lock{Version: "one", Surface: older.Surface}, LockOf(writeSet(t, "2.0.0", broken))); err == nil {
		t.Fatal("a malformed version was accepted")
	}
}

func TestLoadRejectsAMissingVersionAndBadJSON(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("a directory without an AsyncAPI document was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, AsyncAPIFile), []byte("info:\n  title: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("an empty info.version was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}

func TestDigestChangesWithNameAndContent(t *testing.T) {
	base := Digest(map[string][]byte{fileA: []byte("{}")})
	for _, other := range []map[string][]byte{
		{"b.json": []byte("{}")},
		{fileA: []byte("{ }")},
		{fileA: []byte("{}"), "b.json": nil},
	} {
		if Digest(other) == base {
			t.Errorf("digest of %v equals the base", other)
		}
	}
	if Digest(map[string][]byte{fileA: []byte("{}")}) != base {
		t.Error("digest is not stable")
	}
}

func TestDigestMatchesTheDashboardGenerator(t *testing.T) {
	// The same vector is pinned in dashboard/src/lib/types/gen-bus-types.test.ts.
	got := Digest(map[string][]byte{fileA: []byte("{}"), AsyncAPIFile: []byte("info: {}")})
	if want := "f62b4a2f2d92fcc5594f1fd182ceb78f784f1328380487a400779a9d33bc7cf7"; got != want {
		t.Fatalf("digest %s, want %s", got, want)
	}
}

func TestIsComposition(t *testing.T) {
	if !IsComposition(map[string]any{"allOf": []any{}}) {
		t.Error("allOf alone is a composition")
	}
	if IsComposition(map[string]any{"allOf": []any{}, "properties": map[string]any{}}) {
		t.Error("allOf with properties is not a composition")
	}
	if IsComposition(map[string]any{"type": "object"}) {
		t.Error("a plain object is not a composition")
	}
}
