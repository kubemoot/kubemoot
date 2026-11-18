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
	fieldProtocol   = "protocol_version"
	fieldValidUntil = "valid_until"
	fieldStub       = "stub"
	fieldScope      = "scope"
	fieldVersion    = "version"
	fieldWeight     = "weight"
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
	if err := ValidateMemory(readTestdata(t, "appendix-b-memory.json")); err != nil {
		t.Errorf("Appendix B memory rejected: %v", err)
	}
}

// The spec's tiers: a memory carries identifier, media_type, created_at and body
// and nothing else is required, so a memory with no protocol_version, version or
// provenance conforms to the minimal profile (sections 1.2, 3.1.5, 3.7).
func TestSchemaRequiresNothingBeyondTheTable(t *testing.T) {
	for _, field := range []string{fieldProtocol, "path", "title", fieldScope, fieldVersion, "checksum", fieldProvenance, "evidence", "links", fieldWeight} {
		if err := ValidateMemory(memoryWith(t, field, nil)); err != nil {
			t.Errorf("memory without optional %s rejected: %v", field, err)
		}
	}
}

func TestSchemaAcceptsEveryShapeOfVersionAndWeight(t *testing.T) {
	ok := map[string]struct {
		field string
		value any
	}{
		"version object":   {fieldVersion, map[string]any{"value": 2, "comparator": "integer"}},
		"version integer":  {fieldVersion, 2},
		"version string":   {fieldVersion, "1.2.0"},
		"weight integer":   {fieldWeight, 3},
		"weight negative":  {fieldWeight, -0.5},
		"protocol version": {fieldProtocol, "0.6"},
		"valid_until":      {fieldValidUntil, "2027-01-01T00:00:00Z"},
		"stub":             {fieldStub, true},
	}
	for name, c := range ok {
		if err := ValidateMemory(memoryWith(t, c.field, c.value)); err != nil {
			t.Errorf("%s: rejected: %v", name, err)
		}
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
		"scope string":          {fieldScope, "crew:a"},
		"scope entry number":    {"scope", []any{1}},
		"tags string":           {"tags", "x"},
		"version list":          {fieldVersion, []any{2}},
		"version bool":          {fieldVersion, true},
		"version no comparator": {fieldVersion, map[string]any{"value": 2}},
		"protocol version num":  {fieldProtocol, 0.6},
		"empty protocol vers":   {fieldProtocol, ""},
		"weight string":         {fieldWeight, "high"},
		"link version list":     {"links", []any{map[string]any{"target": "a", "rel": "supersedes", "version": []any{1}}}},
		"valid_until not date":  {fieldValidUntil, "someday"},
		"body object":           {fieldBody, map[string]any{"k": "v"}},
		"provenance object":     {fieldProvenance, map[string]any{}},
		"provenance no kind":    {fieldProvenance, []any{map[string]any{"id": "a"}}},
		"provenance bad kind":   {fieldProvenance, []any{map[string]any{"id": "a", "kind": "robot"}}},
		"provenance bad at":     {fieldProvenance, []any{map[string]any{"id": "a", "kind": "agent", "at": "soon"}}},
		"evidence number":       {"evidence", []any{3}},
		"body number":           {fieldBody, 3},
		"stub string":           {fieldStub, "true"},
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
	for _, field := range []string{fieldProtocol, "profiles", "origin", "exported_at", "selection", "intent", "media_types"} {
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
