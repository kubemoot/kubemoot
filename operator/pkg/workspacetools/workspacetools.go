/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package workspacetools is the one Go list of the tools that read the discussion workspace, under
// their current names and the names they had before. The operator takes the tools a declared access
// allows from it, and the fitness runner asks it whether a tool read data that is already in the
// store. The agent-runtime keeps the same list in WorkspaceTools.java; both are checked against
// test/fixtures/workspace-tools.json, so the copies cannot drift.
//
// The package is a leaf of names and two predicates with no dependencies. It is concrete and
// stable (many importers, nothing it imports), so its distance from the main sequence (D') is high
// by construction; that is a deliberate exception to the D' <= 0.3 rule, and an interface here
// would add nothing.
//
// A tool is named in an access list exactly (Canonical), and as a tool call by any server
// prefix too (IsRead), because in direct MCP mode a call carries "server.tool".
package workspacetools

import (
	"slices"
	"strings"
)

// Query is the one tool that replaces the four retired query tools.
const Query = "workspace_query"

var readTools = []string{
	"workspace_list", "workspace_describe", Query,
	"workspace_head", "workspace_tail", "workspace_grep",
}

var legacy = map[string]string{
	"artifact_head": "workspace_head", "artifact_tail": "workspace_tail", "artifact_grep": "workspace_grep",
	"artifact_count": Query, "artifact_rows": Query,
	"artifact_select": Query, "artifact_jq": Query,
}

// ReadTools returns the tools that read the workspace, by their current names.
func ReadTools() []string { return slices.Clone(readTools) }

// Legacy returns each old tool name and the tool that replaces it. They keep working for one release.
func Legacy() map[string]string {
	out := make(map[string]string, len(legacy))
	for k, v := range legacy {
		out[k] = v
	}
	return out
}

// Canonical returns the current name of a workspace tool given its current or old name, exactly as
// written (a server prefix is not removed), and whether the name is a workspace tool at all.
func Canonical(name string) (string, bool) {
	if current, ok := legacy[name]; ok {
		return current, true
	}
	return name, slices.Contains(readTools, name)
}

// IsRead is true for a tool call that reads the workspace, under its current or an old name, with
// or without a server prefix ("server.tool").
func IsRead(name string) bool {
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	_, ok := Canonical(name)
	return ok
}
