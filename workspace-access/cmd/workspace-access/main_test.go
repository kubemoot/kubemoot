package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/workspace-access/internal/scope"
	"github.com/kubemoot/kubemoot/workspace-access/internal/workspace"
)

type mountFixture struct {
	MountPath string `json:"mountPath"`
	CNBAppDir string `json:"cnbAppDir"`
}

// The operator mounts the workspace volume at the path in test/fixtures/workspace-mount.json (and
// at /artifacts for one release); the default must be that path, and never the Buildpacks app dir,
// where this image's own binary lives.
func TestTheDefaultWorkspaceDirIsTheOperatorsMount(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/workspace-mount.json")
	if err != nil {
		t.Fatal(err)
	}
	var f mountFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if defaultWorkspaceDir != f.MountPath {
		t.Fatalf("defaultWorkspaceDir = %q, want %q", defaultWorkspaceDir, f.MountPath)
	}
	if defaultWorkspaceDir == f.CNBAppDir || strings.HasPrefix(defaultWorkspaceDir, f.CNBAppDir+"/") {
		t.Fatalf("defaultWorkspaceDir %q is the Buildpacks app dir %q or under it", defaultWorkspaceDir, f.CNBAppDir)
	}
}

type envFixture struct {
	Current []string `json:"current"`
	Legacy  []string `json:"legacy"`
}

// The variables this component reads are the ones in test/fixtures/workspace-env.json, which the
// operator also reads to flag an old name; each has its old ARTIFACT_ twin.
func TestTheEnvironmentVariablesAreTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/workspace-env.json")
	if err != nil {
		t.Fatal(err)
	}
	var f envFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	read := []string{
		"WORKSPACE_MODE", "WORKSPACE_DIR", "WORKSPACE_BUCKET", scope.SubjectEnv, workspace.EnvEngine,
		workspace.EnvDuckDBBin, workspace.EnvTimeout, workspace.EnvMaxRows, workspace.EnvMemory,
		workspace.EnvMaxArtifactBytes,
	}
	slices.Sort(read)
	if !slices.Equal(read, f.Current) {
		t.Errorf("the variables read are %v, the fixture lists %v", read, f.Current)
	}
	legacy := make([]string, 0, len(f.Current))
	for _, name := range f.Current {
		legacy = append(legacy, "ARTIFACT_"+strings.TrimPrefix(name, "WORKSPACE_"))
	}
	if !slices.Equal(legacy, f.Legacy) {
		t.Errorf("the old names are %v, the fixture lists %v", legacy, f.Legacy)
	}
}
