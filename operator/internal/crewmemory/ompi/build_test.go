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
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/internal/crewmemory"
	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
)

const (
	crewA        = "crew-a"
	pilotCrew    = "pilot"
	recordedText = "2026-10-31T08:15:00Z"
)

var (
	scope      = crewscope.Scope{Namespace: crewA, Crew: pilotCrew}
	learned    = time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	recorded   = time.Date(2026, 10, 31, 8, 15, 0, 0, time.UTC)
	exportedAt = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
)

func fact(topic, key, value string) crewmemory.Fact {
	return crewmemory.Fact{
		Scope: scope, Topic: topic, Key: key, Value: value, LearnedBy: "gpu-analyst",
		LearnedAt: learned, UsedAt: recorded, Revision: 41, Recorded: recorded,
	}
}

func buildExport(t *testing.T, facts ...crewmemory.Fact) Export {
	t.Helper()
	exp, err := Build(scope, crewmemory.ReadResult{Facts: facts}, Options{ExportedAt: exportedAt})
	if err != nil {
		t.Fatal(err)
	}
	return exp
}

func TestMemoryMapsEveryField(t *testing.T) {
	m := buildExport(t, fact("hardware", "gpu0", "RTX 5090, 32 GB")).Memories[0]
	checks := map[string][2]string{
		"title":        {m.Title, "hardware / gpu0"},
		"path":         {m.Path, "hardware/gpu0"},
		fieldMediaType: {m.MediaType, "text/plain"},
		"body":         {m.Body, "RTX 5090, 32 GB"},
		fieldCreatedAt: {m.CreatedAt, "2026-10-30T10:00:00Z"},
		"updated_at":   {m.UpdatedAt, recordedText},
		"usedAt":       {m.Extension.UsedAt, recordedText},
		"namespace":    {m.Extension.Namespace, "crew-a"},
		"crew":         {m.Extension.Crew, pilotCrew},
		"topic":        {m.Extension.Topic, "hardware"},
		"key":          {m.Extension.Key, "gpu0"},
	}
	for field, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", field, c[0], c[1])
		}
	}
	if m.Version == nil || m.Version.Value != 41 || m.Version.Comparator != "integer" {
		t.Errorf("version = %+v, want 41 with an integer comparator", m.Version)
	}
}

func TestScopeIsTheProposedTeamTag(t *testing.T) {
	m := buildExport(t, fact("t", "k", "v")).Memories[0]
	if len(m.Scope) != 1 || m.Scope[0] != "team:crew-a/pilot" {
		t.Errorf("scope = %v, want [team:crew-a/pilot]", m.Scope)
	}
}

func TestProvenanceNamesTheWritingAgent(t *testing.T) {
	m := buildExport(t, fact("t", "k", "v")).Memories[0]
	want := Contributor{ID: "gpu-analyst", Kind: "agent", Contribution: "authored", At: "2026-10-30T10:00:00Z"}
	if len(m.Provenance) != 1 || m.Provenance[0] != want {
		t.Errorf("provenance = %+v, want [%+v]", m.Provenance, want)
	}
}

func TestUnknownAuthorLeavesProvenanceOut(t *testing.T) {
	f := fact("t", "k", "v")
	f.LearnedBy = ""
	m := buildExport(t, f).Memories[0]
	if m.Provenance != nil {
		t.Errorf("provenance = %+v, want none (author unknown, section 4.5)", m.Provenance)
	}
	if data := mustJSON(t, m); strings.Contains(data, `"provenance"`) {
		t.Errorf("provenance key present in %s", data)
	}
}

func TestNothingIsInventedForFieldsTheStoreLacks(t *testing.T) {
	data := mustJSON(t, buildExport(t, fact("t", "k", "v")).Memories[0])
	for _, absent := range []string{`"evidence"`, `"links"`, `"checksum"`, `"valid_from"`, `"valid_until"`, `"tags"`, `"stub"`, `"status"`} {
		if strings.Contains(data, absent) {
			t.Errorf("%s present in %s", absent, data)
		}
	}
}

func TestMissingEvidenceIsSaidOutright(t *testing.T) {
	m := buildExport(t, fact("t", "k", "v")).Memories[0]
	if !m.Extension.EvidenceUnavailable {
		t.Error("a memory without evidence must say evidence is unavailable (section 4.4)")
	}
	if !strings.Contains(mustJSON(t, m), `"evidenceUnavailable": true`) {
		t.Error("evidenceUnavailable not in the JSON")
	}
}

func TestCreatedAtFallsBackToTheStoreTime(t *testing.T) {
	f := fact("t", "k", "v")
	f.LearnedAt = time.Time{}
	m := buildExport(t, f).Memories[0]
	if m.CreatedAt != recordedText {
		t.Errorf("created_at = %q, want the store's write time", m.CreatedAt)
	}
	if len(m.Provenance) != 1 || m.Provenance[0].At != "" {
		t.Errorf("provenance at = %+v, want none when learnedAt is unknown", m.Provenance)
	}
}

func TestFactWithNoTimeIsReportedNotInvented(t *testing.T) {
	f := fact("t", "k", "v")
	f.LearnedAt, f.Recorded = time.Time{}, time.Time{}
	exp := buildExport(t, f, fact("t", "ok", "v"))
	if len(exp.Memories) != 1 || len(exp.Skipped) != 1 || !strings.Contains(exp.Skipped[0].Key, "t.k") {
		t.Fatalf("memories = %d, skipped = %+v", len(exp.Memories), exp.Skipped)
	}
	if exp.Manifest.Selection != SelectionSelection || exp.Manifest.Extension.Skipped != 1 {
		t.Errorf("manifest = %+v, want a selection with one skipped", exp.Manifest)
	}
}

func TestUnreadableEntriesAreCarriedIntoSkipped(t *testing.T) {
	res := crewmemory.ReadResult{
		Facts:   []crewmemory.Fact{fact("t", "k", "v")},
		Skipped: []crewmemory.Skipped{{Key: "crew-a.pilot.t.bad", Reason: "value is not the expected JSON"}},
	}
	exp, err := Build(scope, res, Options{ExportedAt: exportedAt})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Skipped) != 1 || exp.Manifest.Selection != SelectionSelection {
		t.Errorf("skipped = %+v, selection = %s", exp.Skipped, exp.Manifest.Selection)
	}
}

func TestVersionOmittedWhenTheStoreGaveNoRevision(t *testing.T) {
	f := fact("t", "k", "v")
	f.Revision = 0
	if m := buildExport(t, f).Memories[0]; m.Version != nil {
		t.Errorf("version = %+v, want none", m.Version)
	}
}

func TestIdentifiersAreStableAcrossRunsAndOrder(t *testing.T) {
	a := buildExport(t, fact("t", "one", "1"), fact("t", "two", "2"))
	b := buildExport(t, fact("t", "two", "2 changed"), fact("t", "one", "1 changed"))
	ids := func(e Export) map[string]string {
		out := map[string]string{}
		for _, m := range e.Memories {
			out[m.Extension.Key] = m.Identifier
		}
		return out
	}
	ia, ib := ids(a), ids(b)
	if ia["one"] != ib["one"] || ia["two"] != ib["two"] {
		t.Errorf("identifiers changed between runs: %v vs %v", ia, ib)
	}
	if ia["one"] == ia["two"] {
		t.Error("two facts share an identifier")
	}
}

func TestIdentifierIsPinned(t *testing.T) {
	// A change here breaks every previous export's identifiers.
	got := Identifier(fact("hardware", "gpu0", "x"))
	if got != "1a5fba7d-787c-5e63-997a-5c05542531d1" {
		t.Errorf("Identifier() = %s", got)
	}
}

func TestIdentifierDiffersByScopeTopicAndKey(t *testing.T) {
	base := fact("t", "k", "v")
	other := base
	other.Scope = crewscope.Scope{Namespace: "crew-b", Crew: pilotCrew}
	if Identifier(base) == Identifier(other) {
		t.Error("same identifier in two namespaces")
	}
	other = base
	other.Key = "k2"
	if Identifier(base) == Identifier(other) {
		t.Error("same identifier for two keys")
	}
}

func TestIdentifierIsAUUIDv5(t *testing.T) {
	id := Identifier(fact("t", "k", "v"))
	if len(id) != 36 || id[14] != '5' {
		t.Errorf("Identifier() = %q, want a version 5 UUID", id)
	}
}

func TestSameKeyTwiceIsAnError(t *testing.T) {
	if _, err := Build(scope, crewmemory.ReadResult{Facts: []crewmemory.Fact{fact("t", "k", "a"), fact("t", "k", "b")}}, Options{}); err == nil {
		t.Error("Build() = nil, want a duplicate identifier error")
	}
}

func TestManifestDeclaresOriginAndTime(t *testing.T) {
	m := buildExport(t, fact("t", "k", "v")).Manifest
	if m.ProtocolVersion != "0.2" || m.Intent != "copy" || m.Selection != SelectionSnapshot {
		t.Errorf("manifest = %+v", m)
	}
	if m.ExportedAt != "2026-10-31T12:00:00Z" {
		t.Errorf("exported_at = %q", m.ExportedAt)
	}
	if !strings.HasPrefix(m.Origin, "store:") || m.Origin != DefaultOrigin(scope) {
		t.Errorf("origin = %q", m.Origin)
	}
}

func TestManifestDeclaresProfileAndMediaTypes(t *testing.T) {
	m := buildExport(t, fact("t", "k", "v")).Manifest
	if len(m.Profiles) != 1 || m.Profiles[0] != "minimal" {
		t.Errorf("profiles = %v", m.Profiles)
	}
	if len(m.MediaTypes) != 1 || m.MediaTypes[0] != "text/plain" {
		t.Errorf("media types = %v", m.MediaTypes)
	}
}

func TestOriginCanBeSetAndDefaultIsPerCrew(t *testing.T) {
	exp, err := Build(scope, crewmemory.ReadResult{}, Options{ExportedAt: exportedAt, Origin: "store:mine"})
	if err != nil || exp.Manifest.Origin != "store:mine" {
		t.Errorf("origin = %q, err = %v", exp.Manifest.Origin, err)
	}
	if DefaultOrigin(scope) == DefaultOrigin(crewscope.Scope{Namespace: crewA, Crew: "other"}) {
		t.Error("two crews share a default origin")
	}
}

func TestEmptyBucketGivesAValidManifestAndNoMemories(t *testing.T) {
	exp := buildExport(t)
	if len(exp.Memories) != 0 || exp.Manifest.Selection != SelectionSnapshot {
		t.Errorf("export = %+v", exp)
	}
	files, err := Files(exp, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[ManifestFile] == nil {
		t.Errorf("files = %v, want only the manifest", keysOf(files))
	}
	if !strings.Contains(string(files[ManifestFile]), `"media_types"`) {
		t.Error("manifest lost its media_types array")
	}
}

func TestHostileValuesStayData(t *testing.T) {
	hostile := "line1\n---\nidentifier: evil\n\x00[x](http://e)<script>\"\\   \xff end"
	exp := buildExport(t, fact("../etc/passwd", "a b\nc", hostile), fact("t", strings.Repeat("k", 5000), strings.Repeat("v", 1<<20)))
	files, err := Files(exp, true)
	if err != nil {
		t.Fatal(err)
	}
	for rel := range files {
		if err := checkRelative(rel); err != nil {
			t.Errorf("%v", err)
		}
	}
	for _, m := range exp.Memories {
		if m.Path == "" || strings.ContainsAny(m.Path, " .\\\n") || strings.HasPrefix(m.Path, "/") {
			t.Errorf("path %q is not safe", m.Path)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func keysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
