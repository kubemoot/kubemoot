/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
)

const (
	fieldBody       = "body"
	fieldIdentifier = "identifier"
	fieldMediaType  = "media_type"
	fieldCreatedAt  = "created_at"
	fieldProvenance = "provenance"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSchemaAcceptsTheSpecsOwnExamples(t *testing.T) {
	if err := ValidateMemory(readTestdata(t, "appendix-a-memory.json")); err != nil {
		t.Errorf("Appendix A memory rejected: %v", err)
	}
	if err := ValidateManifest(readTestdata(t, "appendix-a-manifest.json")); err != nil {
		t.Errorf("Appendix A manifest rejected: %v", err)
	}
}

func TestSchemaAcceptsTheSmallestConformingMemory(t *testing.T) {
	const minimal = `{"identifier":"MEMORY.md","media_type":"text/markdown","created_at":"2026-10-14T00:00:00Z","body":"The user prefers concise answers."}`
	if err := ValidateMemory([]byte(minimal)); err != nil {
		t.Errorf("Appendix C memory rejected: %v", err)
	}
}

// memoryWith returns a valid memory with one field replaced (or removed when
// the value is nil).
func memoryWith(t *testing.T, field string, value any) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readTestdata(t, "appendix-a-memory.json"), &m); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		delete(m, field)
	} else {
		m[field] = value
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSchemaRejectsAMemoryMissingARequiredField(t *testing.T) {
	for _, field := range []string{fieldIdentifier, fieldMediaType, fieldCreatedAt, fieldBody} {
		if err := ValidateMemory(memoryWith(t, field, nil)); err == nil {
			t.Errorf("memory without %s accepted", field)
		}
	}
}

func TestSchemaRejectsWrongTypesAndFormats(t *testing.T) {
	cases := map[string]struct {
		field string
		value any
	}{
		"identifier number":     {"identifier", 7},
		"empty identifier":      {"identifier", ""},
		"media type no slash":   {"media_type", "text"},
		"created_at not date":   {"created_at", "yesterday"},
		"created_at no zone":    {"created_at", "2026-10-14 10:00"},
		"updated_at number":     {"updated_at", 5},
		"scope string":          {"scope", "crew:a"},
		"scope entry number":    {"scope", []any{1}},
		"tags string":           {"tags", "x"},
		"version bare":          {"version", 2},
		"version no comparator": {"version", map[string]any{"value": 2}},
		"provenance object":     {fieldProvenance, map[string]any{}},
		"provenance no kind":    {fieldProvenance, []any{map[string]any{"id": "a"}}},
		"provenance bad kind":   {fieldProvenance, []any{map[string]any{"id": "a", "kind": "robot"}}},
		"provenance bad at":     {fieldProvenance, []any{map[string]any{"id": "a", "kind": "agent", "at": "soon"}}},
		"evidence number":       {"evidence", []any{3}},
		"body number":           {fieldBody, 3},
		"stub string":           {"stub", "true"},
	}
	for name, c := range cases {
		if err := ValidateMemory(memoryWith(t, c.field, c.value)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSchemaKeepsUnknownFields(t *testing.T) {
	b := memoryWith(t, "x-vendor", map[string]any{"anything": []any{1, 2}})
	if err := ValidateMemory(b); err != nil {
		t.Errorf("a memory with an extra field was rejected: %v", err)
	}
}

func TestSchemaRejectsBadInput(t *testing.T) {
	for _, bad := range []string{``, `not json`, `[]`, `"text"`, `null`} {
		if err := ValidateMemory([]byte(bad)); err == nil {
			t.Errorf("ValidateMemory(%q) accepted", bad)
		}
	}
}

func manifestWith(t *testing.T, field string, value any) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readTestdata(t, "appendix-a-manifest.json"), &m); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		delete(m, field)
	} else {
		m[field] = value
	}
	b, _ := json.Marshal(m)
	return b
}

func TestManifestSchemaRejectsMissingOrWrongFields(t *testing.T) {
	for _, field := range []string{"protocol_version", "profiles", "origin", "exported_at", "selection", "intent", "media_types"} {
		if err := ValidateManifest(manifestWith(t, field, nil)); err == nil {
			t.Errorf("manifest without %s accepted", field)
		}
	}
	bad := map[string]any{"exported_at": "later", "intent": "sell", "selection": "some", "profiles": "minimal", "origin": ""}
	for field, v := range bad {
		if err := ValidateManifest(manifestWith(t, field, v)); err == nil {
			t.Errorf("manifest with %s=%v accepted", field, v)
		}
	}
}

// TestExportConformsToTheSchema is the conformance check for the exporter:
// every document it renders, for a spread of facts, passes the schema, has the
// minimal profile's required fields and ISO 8601 timestamps, and has a unique
// identifier.
func TestExportConformsToTheSchema(t *testing.T) {
	odd := fact("hardware", "gpu0", "RTX 5090")
	odd.LearnedBy = ""
	noRev := fact("net", "vlan", "50")
	noRev.Revision, noRev.Recorded = 0, noRev.Recorded.Truncate(0)
	exp := buildExport(t, fact("hardware", "gpu1", "RTX 4090"), odd, noRev,
		fact("../x", "MEMORY", "a"), fact("t", "Foo", "upper"), fact("t", "foo", "lower"))
	files, err := Files(exp, true)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for rel, data := range files {
		if !strings.HasPrefix(rel, MemoriesDir+"/") {
			continue
		}
		checkMemoryDocument(t, rel, data, seen)
	}
	if len(seen) != len(exp.Memories) {
		t.Errorf("%d identifiers for %d memories", len(seen), len(exp.Memories))
	}
}

func checkMemoryDocument(t *testing.T, rel string, data []byte, seen map[string]bool) {
	t.Helper()
	if err := ValidateMemory(data); err != nil {
		t.Errorf("%s: %v", rel, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	for _, f := range []string{fieldIdentifier, fieldMediaType, fieldCreatedAt, fieldBody} {
		if _, ok := m[f]; !ok {
			t.Errorf("%s lacks required field %s", rel, f)
		}
	}
	id, _ := m["identifier"].(string)
	if seen[id] {
		t.Errorf("identifier %s repeated", id)
	}
	seen[id] = true
	if !strings.HasSuffix(rel, id+".json") {
		t.Errorf("%s is not named for its identifier %s", rel, id)
	}
}

func TestFixtureExportOfRealShapedFacts(t *testing.T) {
	res, err := crewmemory.ReadFacts(shapedReader{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := Build(scope, res, Options{ExportedAt: exportedAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Files(exp, true); err != nil {
		t.Fatal(err)
	}
	if len(exp.Memories) != 2 || len(exp.Skipped) != 1 {
		t.Errorf("memories = %d, skipped = %+v", len(exp.Memories), exp.Skipped)
	}
}

// shapedReader returns entries shaped like the agent runtime's values.
type shapedReader struct{}

func (shapedReader) ListKVKeys(string) ([]string, error) {
	return []string{"crew-a.pilot.hardware.gpu0", "crew-a.pilot.hardware.gpu1", "crew-a.pilot.hardware.broken", "crew-a.other.x.y"}, nil
}

func (shapedReader) GetKVEntry(_, key string) (crewmemory.Entry, error) {
	values := map[string]string{
		"crew-a.pilot.hardware.gpu0":   `{"value":"RTX 5090, 32 GB","learnedBy":"gpu-analyst","learnedAt":"2026-10-30T10:00:00Z","usedAt":"2026-10-31T08:00:00Z"}`,
		"crew-a.pilot.hardware.gpu1":   `{"value":"RTX 4090"}`,
		"crew-a.pilot.hardware.broken": `not json`,
	}
	return crewmemory.Entry{Value: []byte(values[key]), Revision: 9, Created: recorded}, nil
}
