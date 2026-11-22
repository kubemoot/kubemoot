/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package workspacetools

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	tGrep      = "workspace_grep"
	tPods      = "pods_list"
	tChanged   = "changed"
	tOldHead   = "artifact_head"
	tOldPrefix = "ARTIFACT_"
)

func TestIsRead(t *testing.T) {
	for name, want := range map[string]bool{
		"workspace_query": true, tGrep: true, "artifact_select": true, tOldHead: true,
		"mcp.workspace_head": true, tPods: false, "": false, "workspace_write": false,
	} {
		if got := IsRead(name); got != want {
			t.Errorf("IsRead(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCanonicalIsExact(t *testing.T) {
	for name, want := range map[string]struct {
		current string
		ok      bool
	}{
		tGrep:                {tGrep, true},
		"artifact_select":    {Query, true},
		"mcp.workspace_grep": {"mcp.workspace_grep", false},
		tPods:                {tPods, false},
	} {
		if current, ok := Canonical(name); current != want.current || ok != want.ok {
			t.Errorf("Canonical(%q) = %q, %v; want %q, %v", name, current, ok, want.current, want.ok)
		}
	}
}

func TestListsAreCopies(t *testing.T) {
	ReadTools()[0] = tChanged
	Legacy()[tOldHead] = tChanged
	if ReadTools()[0] == tChanged || Legacy()[tOldHead] == tChanged {
		t.Error("a caller must not be able to change the lists")
	}
}

// The agent-runtime's WorkspaceTools.java runs the same fixture.
func TestListsMatchTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/workspace-tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		ReadTools []string          `json:"readTools"`
		Legacy    map[string]string `json:"legacy"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	got := ReadTools()
	slices.Sort(got)
	slices.Sort(f.ReadTools)
	if !slices.Equal(got, f.ReadTools) {
		t.Errorf("read tools = %v, fixture %v", got, f.ReadTools)
	}
	legacy := Legacy()
	if len(legacy) != len(f.Legacy) {
		t.Errorf("legacy = %v, fixture %v", legacy, f.Legacy)
	}
	for old, current := range f.Legacy {
		if legacy[old] != current {
			t.Errorf("legacy[%q] = %q, fixture %q", old, legacy[old], current)
		}
	}
}

func TestDeprecatedNamesCoverEveryOldName(t *testing.T) {
	d := DeprecatedNames()
	if !reflect.DeepEqual(d.Tools, Legacy()) {
		t.Errorf("tools %v, want the legacy tools %v", d.Tools, Legacy())
	}
	if len(d.EnvNames) == 0 || d.Mount == "" || d.ImageRepo == "" || len(d.Fields) == 0 || len(d.Markers) == 0 {
		t.Fatalf("an empty part: %+v", d)
	}
	for _, name := range d.EnvNames {
		if !strings.HasPrefix(name, d.EnvPrefix) {
			t.Errorf("%s does not start with %s", name, d.EnvPrefix)
		}
	}
	if d.Mount == WorkspaceDir {
		t.Errorf("the old mount is the current one")
	}
}

func TestDeprecatedNamesListTheOldAPIFields(t *testing.T) {
	d := DeprecatedNames()
	for _, f := range []string{"spec.artifacts", "artifactAccess", "artifactReadOps", "vramMib", "gpu-busy"} {
		if !slices.Contains(d.Fields, f) {
			t.Errorf("fields %v miss %s", d.Fields, f)
		}
	}
}

func TestDeprecatedNamesNamesListsEachPartAndIsACopy(t *testing.T) {
	d := DeprecatedNames()
	got := d.Names()
	for _, want := range []string{tOldHead, tOldPrefix, "/artifacts", "artifact-access", "artifactAccess", "[ARTIFACT"} {
		if !slices.Contains(got, want) {
			t.Errorf("Names() misses %q: %v", want, got)
		}
	}
	d.EnvNames[0] = tChanged
	d.Tools[tOldHead] = tChanged
	again := DeprecatedNames()
	if again.EnvNames[0] == tChanged || again.Tools[tOldHead] == tChanged {
		t.Errorf("the list can be changed by a caller")
	}
}

func TestFindFlagsOldNamesAtTheStartOfAWordOnly(t *testing.T) {
	d := DeprecatedNames()
	cases := []struct {
		text string
		want []string
	}{
		{"name: ARTIFACT_MODE", []string{tOldPrefix}},
		{"ARTIFACT_MODE", []string{tOldPrefix}},
		{"name: WORKSPACE_QUERY_MAX_ARTIFACT_BYTES", nil},
		{"WORKSPACE_MODE WORKSPACE_DIR WORKSPACE_QUERY_ENGINE", nil},
		{"WORKSPACE_QUERY_MAX_ARTIFACT_BYTES and ARTIFACT_MODE", []string{tOldPrefix}},
		{"tools: [artifact_head, workspace_head]", []string{tOldHead}},
		{"tools: [my_artifact_head]", nil},
		{"image: ghcr.io/kubemoot/artifact-access:1", []string{"artifact-access"}},
		{"image: reg/my-artifact-access:1", nil},
		{"mountPath: /artifacts", []string{"/artifacts"}},
		{"mountPath: /kubemoot/workspace", nil},
		{"[ARTIFACT x]", []string{"[ARTIFACT"}},
		{"vramMib: 11000", []string{"vramMib"}},
		{"acceleratorMemoryMiB: 11000", nil},
		{"myvramMib: 1", nil},
		{"reason: gpu-busy", []string{"gpu-busy"}},
		{"reason: accelerator-busy", nil},
		{"", nil},
	}
	for _, tc := range cases {
		if got := d.Find(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("Find(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}
