/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package workspacetools

import (
	"maps"
	"slices"
	"strings"
)

// Deprecated is every name Kubemoot renamed when the discussion's shared store became the
// workspace. The old names keep working for one release and are removed in the next; the operator
// reports a use of one in the MCPServer DeprecatedNames condition, and a crew author or a tool such
// as kmctl checks what it writes against the same list.
type Deprecated struct {
	// Tools maps each old tool name to the tool that replaces it.
	Tools map[string]string
	// EnvNames lists the old workspace-access environment variables; each is WORKSPACE_ plus the
	// same word now.
	EnvNames []string
	// EnvPrefix is the prefix every old environment variable starts with.
	EnvPrefix string
	// Mount is the old emptyDir mount path; the current one is WorkspaceDir.
	Mount string
	// ImageRepo is the old image repository name; the current one is workspace-access.
	ImageRepo string
	// Fields lists the old API fields and values (spec.artifacts, artifactAccess, artifactReadOps,
	// Model spec.vramMib, now acceleratorMemoryMiB, and the stand_aside reason gpu-busy, now
	// accelerator-busy).
	Fields []string
	// Markers lists the old finding markers; the current ones start [WORKSPACE.
	Markers []string
}

// WorkspaceDir is the current workspace mount path.
const WorkspaceDir = "/kubemoot/workspace"

var deprecatedEnvNames = []string{
	"ARTIFACT_BUCKET", "ARTIFACT_DIR", "ARTIFACT_DUCKDB_BIN", "ARTIFACT_MODE",
	"ARTIFACT_QUERY_ENGINE", "ARTIFACT_QUERY_MAX_ARTIFACT_BYTES", "ARTIFACT_QUERY_MAX_ROWS",
	"ARTIFACT_QUERY_MEMORY", "ARTIFACT_QUERY_TIMEOUT", "ARTIFACT_SUBJECT",
}

// DeprecatedNames returns the one list of deprecated Kubemoot names. The caller owns the result.
// Entries go when the release that removes the old names lands.
func DeprecatedNames() Deprecated {
	return Deprecated{
		Tools:     Legacy(),
		EnvNames:  slices.Clone(deprecatedEnvNames),
		EnvPrefix: "ARTIFACT_",
		Mount:     "/artifacts",
		ImageRepo: "artifact-access",
		Fields:    []string{"artifactAccess", "artifactReadOps", "spec.artifacts", "vramMib", "gpu-busy"},
		Markers:   []string{"[ARTIFACT"},
	}
}

// Names returns every deprecated name as one flat, sorted list: the old tools, the environment
// prefix, the mount, the image repository, the fields and the markers. Look for them in text with
// Find, not with a plain substring search: the prefix ARTIFACT_ is inside the current variable
// WORKSPACE_QUERY_MAX_ARTIFACT_BYTES.
func (d Deprecated) Names() []string {
	out := slices.Sorted(maps.Keys(d.Tools))
	out = append(out, d.EnvPrefix, d.Mount, d.ImageRepo)
	out = append(out, d.Fields...)
	out = append(out, d.Markers...)
	return out
}

// Find returns the deprecated names the text uses, each once, in Names order. A name that starts
// with a letter, digit or underscore counts only where it starts a word: it is not preceded by a
// letter, digit, underscore or hyphen, so WORKSPACE_QUERY_MAX_ARTIFACT_BYTES and my-artifact-access
// are not flagged while ARTIFACT_MODE and artifact-access are.
func (d Deprecated) Find(text string) []string {
	var found []string
	for _, name := range d.Names() {
		if usesName(text, name) && !slices.Contains(found, name) {
			found = append(found, name)
		}
	}
	return found
}

// usesName reports whether text holds name at the start of a word (see Find).
func usesName(text, name string) bool {
	wordName := isWordByte(name[0])
	for from := 0; ; {
		i := strings.Index(text[from:], name)
		if i < 0 {
			return false
		}
		i += from
		if !wordName || i == 0 || !isWordByte(text[i-1]) && text[i-1] != '-' {
			return true
		}
		from = i + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
