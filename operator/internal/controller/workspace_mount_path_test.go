package controller

import (
	"encoding/json"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
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

// The emptyDir volumes the MCPServer builder actually produces for a workspace server are mounted
// at the paths in the shared fixture: the workspace path and the old /artifacts beside it. The
// constants are compared with the fixture above; this checks the builder uses them.
func TestBuiltWorkspaceVolumeMountsAreTheFixturePaths(t *testing.T) {
	f := loadMountFixture(t)
	ms := withQueryWorkspace(readOpsServer(), kubemootv1alpha1.WorkspaceQueryEngineSQLite)
	vols, mounts := buildMCPServerVolumes(ms)
	if len(vols) != 1 {
		t.Fatalf("want one workspace volume, got %d", len(vols))
	}
	var got []string
	for _, m := range mounts {
		if m.Name == vols[0].Name {
			got = append(got, m.MountPath)
		}
	}
	if !reflect.DeepEqual(got, []string{f.MountPath, f.LegacyMountPath}) {
		t.Errorf("the workspace volume is mounted at %v, the fixture says %q and %q", got, f.MountPath, f.LegacyMountPath)
	}
}

// A sidecar of a workspace server shares the same volume at the same two paths.
func TestBuiltSidecarMountsAreTheFixturePaths(t *testing.T) {
	f := loadMountFixture(t)
	ms := withQueryWorkspace(readOpsServer(), kubemootv1alpha1.WorkspaceQueryEngineSQLite)
	ms.Spec.Sidecars = []corev1.Container{{Name: sidecarMaterializer, Image: specImage}}
	sidecars := buildUserSidecars(ms)
	if len(sidecars) != 1 {
		t.Fatalf("want one sidecar, got %d", len(sidecars))
	}
	got := make([]string, 0, len(sidecars[0].VolumeMounts))
	for _, m := range sidecars[0].VolumeMounts {
		got = append(got, m.MountPath)
	}
	if !reflect.DeepEqual(got, []string{f.MountPath, f.LegacyMountPath}) {
		t.Errorf("the sidecar mounts the workspace at %v, the fixture says %q and %q", got, f.MountPath, f.LegacyMountPath)
	}
}
