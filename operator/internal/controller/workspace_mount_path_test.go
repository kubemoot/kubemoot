package controller

import (
	"encoding/json"
	"os"
	"path"
	"strings"
	"testing"
)

type mountFixture struct {
	MountPath       string `json:"mountPath"`
	LegacyMountPath string `json:"legacyMountPath"`
	CNBAppDir       string `json:"cnbAppDir"`
}

// loadMountFixture reads test/fixtures/workspace-mount.json, which workspace-access and the agent
// runtime also pin their paths to. cnbAppDir is where a Buildpacks image keeps its program; a
// volume mounted there hides the binary, so the container cannot start.
func loadMountFixture(t *testing.T) mountFixture {
	t.Helper()
	raw, err := os.ReadFile("../../../test/fixtures/workspace-mount.json")
	if err != nil {
		t.Fatal(err)
	}
	var f mountFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// The workspace mount must never be the CNB app dir or under it: every Kubemoot Go image and
// any user's Buildpacks MCP server runs /workspace/<binary>.
func TestWorkspaceMountIsNotTheBuildpacksAppDir(t *testing.T) {
	f := loadMountFixture(t)
	for _, dir := range []string{workspaceDir, legacyWorkspaceDir} {
		clean := path.Clean(dir)
		if clean == f.CNBAppDir || strings.HasPrefix(clean, f.CNBAppDir+"/") {
			t.Errorf("mount path %q is the Buildpacks app dir %q or under it", dir, f.CNBAppDir)
		}
	}
}

func TestWorkspaceMountsAreTheSharedFixture(t *testing.T) {
	f := loadMountFixture(t)
	if workspaceDir != f.MountPath || legacyWorkspaceDir != f.LegacyMountPath {
		t.Errorf("mounts = %q and %q, the fixture says %q and %q", workspaceDir, legacyWorkspaceDir, f.MountPath, f.LegacyMountPath)
	}
}
