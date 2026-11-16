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
// store. The agent-runtime keeps the same list in WorkspaceTools.java.
package workspacetools

import "strings"

// Query is the one tool that replaces the four retired query tools.
const Query = "workspace_query"

// ReadTools are the tools that read the workspace, by their current names.
var ReadTools = []string{
	"workspace_list", "workspace_describe", Query,
	"workspace_head", "workspace_tail", "workspace_grep",
}

// Legacy maps each old tool name to the tool that replaces it. They keep working for one release.
var Legacy = map[string]string{
	"artifact_head": "workspace_head", "artifact_tail": "workspace_tail", "artifact_grep": "workspace_grep",
	"artifact_count": Query, "artifact_rows": Query,
	"artifact_select": Query, "artifact_jq": Query,
}

// IsRead is true for a tool that reads the workspace, under its current or an old name. A server
// prefix ("server.tool") is ignored.
func IsRead(name string) bool {
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if current, ok := Legacy[name]; ok {
		name = current
	}
	for _, t := range ReadTools {
		if t == name {
			return true
		}
	}
	return false
}
