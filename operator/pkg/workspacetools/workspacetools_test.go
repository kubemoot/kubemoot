/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package workspacetools

import "testing"

func TestIsRead(t *testing.T) {
	for name, want := range map[string]bool{
		"workspace_query": true, "workspace_grep": true, "artifact_select": true, "artifact_head": true,
		"mcp.workspace_head": true, "pods_list": false, "": false, "workspace_write": false,
	} {
		if got := IsRead(name); got != want {
			t.Errorf("IsRead(%q) = %v, want %v", name, got, want)
		}
	}
}
