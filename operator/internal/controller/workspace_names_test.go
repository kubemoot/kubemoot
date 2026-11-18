package controller

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	sidecarMaterializer = "materializer"
	workspaceTwoGi      = "2Gi"
	oldImage            = "ghcr.io/kubemoot/workspace-access:0.9.0"
	newImage            = "ghcr.io/kubemoot/workspace-access:1.0.0"
)

func workspaceServer(vols ...kubemootv1alpha1.EmptyDirVolume) *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{
		KubemootImage:   kubemootv1alpha1.KubemootImageWorkspaceAccess,
		EmptyDirVolumes: vols,
	}}
}

func TestEmptyDirMountPathsAliasTheWorkspaceDirectory(t *testing.T) {
	secretAtOld := workspaceServer(kubemootv1alpha1.EmptyDirVolume{MountPath: workspaceDir})
	secretAtOld.Spec.SecretVolumes = []kubemootv1alpha1.SecretVolume{{Name: "s", MountPath: legacyWorkspaceDir}}
	sandbox := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{
		Image:           specImage,
		Sidecars:        []corev1.Container{{Name: sidecarMaterializer}},
		EmptyDirVolumes: []kubemootv1alpha1.EmptyDirVolume{{MountPath: legacyWorkspaceDir}},
	}}
	unrelated := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{
		Image:           specImage,
		EmptyDirVolumes: []kubemootv1alpha1.EmptyDirVolume{{MountPath: workspaceDir}},
	}}
	two := workspaceServer(kubemootv1alpha1.EmptyDirVolume{MountPath: workspaceDir}, kubemootv1alpha1.EmptyDirVolume{MountPath: legacyWorkspaceDir})
	cases := []struct {
		name string
		ms   *kubemootv1alpha1.MCPServer
		at   int
		want []string
	}{
		{"workspace gains the old path", workspaceServer(kubemootv1alpha1.EmptyDirVolume{MountPath: workspaceDir}), 0, []string{workspaceDir, legacyWorkspaceDir}},
		{"the old path gains the workspace path", workspaceServer(kubemootv1alpha1.EmptyDirVolume{MountPath: legacyWorkspaceDir}), 0, []string{legacyWorkspaceDir, workspaceDir}},
		{"another path has no alias", workspaceServer(kubemootv1alpha1.EmptyDirVolume{MountPath: harnessTmpDir}), 0, []string{harnessTmpDir}},
		{"no alias over a volume that holds it", two, 0, []string{workspaceDir}},
		{"the second volume keeps its own path too", two, 1, []string{legacyWorkspaceDir}},
		{"no alias over a secret volume", secretAtOld, 0, []string{workspaceDir}},
		{"a sandbox pod with a sidecar gets the alias", sandbox, 0, []string{legacyWorkspaceDir, workspaceDir}},
		{"an unrelated server keeps its one mount", unrelated, 0, []string{workspaceDir}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := emptyDirMountPaths(tc.ms, tc.ms.Spec.EmptyDirVolumes[tc.at]); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func mountPathsOf(mounts []corev1.VolumeMount, volume string) []string {
	var paths []string
	for _, m := range mounts {
		if m.Name == volume {
			paths = append(paths, m.MountPath)
		}
	}
	return paths
}

func TestTheMainContainerAndTheSidecarsMountTheWorkspaceVolumeAtBothPaths(t *testing.T) {
	ms := workspaceServer(kubemootv1alpha1.EmptyDirVolume{MountPath: workspaceDir, SizeLimit: "1Gi"})
	ms.Spec.Sidecars = []corev1.Container{
		{Name: sidecarMaterializer},
		{Name: "own-mount", VolumeMounts: []corev1.VolumeMount{{Name: syncForeignData, MountPath: legacyWorkspaceDir}}},
	}
	want := []string{workspaceDir, legacyWorkspaceDir}

	vols, mounts := buildMCPServerVolumes(ms)
	if len(vols) != 1 {
		t.Fatalf("one volume expected, got %d", len(vols))
	}
	if got := mountPathsOf(mounts, vols[0].Name); !reflect.DeepEqual(got, want) {
		t.Errorf("main container: the volume is mounted at %v, want %v", got, want)
	}

	sidecars := buildUserSidecars(ms)
	if got := mountPathsOf(sidecars[0].VolumeMounts, vols[0].Name); !reflect.DeepEqual(got, want) {
		t.Errorf("sidecar: the volume is mounted at %v, want %v", got, want)
	}
	if got := mountPathsOf(sidecars[1].VolumeMounts, vols[0].Name); !reflect.DeepEqual(got, []string{workspaceDir}) {
		t.Errorf("a sidecar's own mount at the alias path is kept and not doubled: %v", got)
	}
}

func TestEitherNameOfTheImageResolvesAndTheCurrentValueWins(t *testing.T) {
	both := &kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Images: kubemootv1alpha1.ImageConfig{WorkspaceAccess: newImage, ArtifactAccess: oldImage},
	}}
	onlyOld := &kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Images: kubemootv1alpha1.ImageConfig{ArtifactAccess: oldImage},
	}}
	cases := []struct {
		name string
		cfg  *kubemootv1alpha1.KubemootConfig
		sel  kubemootv1alpha1.KubemootImage
		want string
	}{
		{"workspaceAccess with both set", both, kubemootv1alpha1.KubemootImageWorkspaceAccess, newImage},
		{"artifactAccess with both set", both, kubemootv1alpha1.KubemootImageArtifactAccess, newImage},
		{"workspaceAccess with only the old value", onlyOld, kubemootv1alpha1.KubemootImageWorkspaceAccess, oldImage},
		{"an unknown selection", both, testMismatchName, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kubemootImageFor(tc.cfg, tc.sel); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheQueryEngineIsTheWorkspaceSettingThenTheOldOneThenSQLite(t *testing.T) {
	old := kubemootv1alpha1.ArtifactsConfig{QueryEngine: kubemootv1alpha1.WorkspaceQueryEngineDuckDB}
	ws := &kubemootv1alpha1.WorkspaceConfig{QueryEngine: kubemootv1alpha1.WorkspaceQueryEngineNone}
	cases := []struct {
		name string
		spec kubemootv1alpha1.KubemootConfigSpec
		want kubemootv1alpha1.WorkspaceQueryEngine
	}{
		{"nothing set", kubemootv1alpha1.KubemootConfigSpec{}, kubemootv1alpha1.WorkspaceQueryEngineSQLite},
		{"only the old value", kubemootv1alpha1.KubemootConfigSpec{Artifacts: old}, kubemootv1alpha1.WorkspaceQueryEngineDuckDB},
		{"the workspace value wins", kubemootv1alpha1.KubemootConfigSpec{Artifacts: old, Workspace: ws}, kubemootv1alpha1.WorkspaceQueryEngineNone},
		{"a workspace block without an engine", kubemootv1alpha1.KubemootConfigSpec{Artifacts: old, Workspace: &kubemootv1alpha1.WorkspaceConfig{}}, kubemootv1alpha1.WorkspaceQueryEngineDuckDB},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := configuredQueryEngine(&kubemootv1alpha1.KubemootConfig{Spec: tc.spec}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAPinnedOldImageIsFlaggedWithWhatItIgnores(t *testing.T) {
	for image, want := range map[string]bool{
		"ghcr.io/kubemoot/artifact-access:0.347.0": true,
		"artifact-access:1":                        true,
		"ghcr.io/kubemoot/workspace-access:1.0.0":  false,
		"reg.test/x/my-artifact-access:1":          false,
		"":                                         false,
	} {
		ms := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{Image: image}}
		got := strings.Join(deprecatedWorkspaceNames(ms), ";")
		if flagged := strings.Contains(got, "ignores the WORKSPACE_QUERY_ENGINE"); flagged != want {
			t.Errorf("image %q: flagged=%v, want %v (%q)", image, flagged, want, got)
		}
	}
}

func TestDeprecatedNamesAreListedAndTheConditionFollowsThem(t *testing.T) {
	current := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{
		KubemootImage:   kubemootv1alpha1.KubemootImageWorkspaceAccess,
		Env:             []corev1.EnvVar{{Name: "WORKSPACE_BUCKET", Value: "x"}},
		EmptyDirVolumes: []kubemootv1alpha1.EmptyDirVolume{{MountPath: workspaceDir}},
	}}
	setDeprecatedNamesCondition(current)
	if meta.FindStatusCondition(current.Status.Conditions, conditionTypeDeprecatedNames) != nil {
		t.Fatalf("current names must not set the condition: %v", current.Status.Conditions)
	}

	old := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{
		KubemootImage:   kubemootv1alpha1.KubemootImageArtifactAccess,
		Env:             []corev1.EnvVar{{Name: "ARTIFACT_BUCKET", Value: "x"}, {Name: "OTHER", Value: "y"}},
		EmptyDirVolumes: []kubemootv1alpha1.EmptyDirVolume{{MountPath: legacyWorkspaceDir}},
	}}
	setDeprecatedNamesCondition(old)
	cond := meta.FindStatusCondition(old.Status.Conditions, conditionTypeDeprecatedNames)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("old names must set the condition: %v", old.Status.Conditions)
	}
	for _, want := range []string{"artifactAccess is workspaceAccess", "ARTIFACT_BUCKET is WORKSPACE_BUCKET", "/artifacts is /kubemoot/workspace"} {
		if !strings.Contains(cond.Message, want) {
			t.Errorf("message %q does not say %q", cond.Message, want)
		}
	}
	if strings.Contains(cond.Message, "OTHER") {
		t.Errorf("an unrelated variable is not listed: %q", cond.Message)
	}

	unrelated := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{
		Image:           specImage,
		Env:             []corev1.EnvVar{{Name: "ARTIFACT_REPO_URL", Value: "x"}},
		EmptyDirVolumes: []kubemootv1alpha1.EmptyDirVolume{{MountPath: legacyWorkspaceDir}},
	}}
	setDeprecatedNamesCondition(unrelated)
	if meta.FindStatusCondition(unrelated.Status.Conditions, conditionTypeDeprecatedNames) != nil {
		t.Errorf("a server that is not a workspace server must not get the condition: %v", unrelated.Status.Conditions)
	}

	old.Spec = current.Spec
	setDeprecatedNamesCondition(old)
	if meta.FindStatusCondition(old.Status.Conditions, conditionTypeDeprecatedNames) != nil {
		t.Errorf("the condition must clear once the names are current")
	}
}

func TestAServerWithAVolumeAtEitherPathGetsNoSecondWorkspaceVolume(t *testing.T) {
	for _, path := range []string{workspaceDir, legacyWorkspaceDir} {
		ms := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{
			EmptyDirVolumes: []kubemootv1alpha1.EmptyDirVolume{{MountPath: path, SizeLimit: workspaceTwoGi}},
		}}
		got := withQueryWorkspace(ms, kubemootv1alpha1.WorkspaceQueryEngineSQLite)
		if len(got.Spec.EmptyDirVolumes) != 1 || got.Spec.EmptyDirVolumes[0].MountPath != path {
			t.Errorf("the server's own volume at %s must be kept alone: %v", path, got.Spec.EmptyDirVolumes)
		}
	}
}

// The old variables the operator flags are the ones workspace-access reads under their old names:
// both lists are compared with test/fixtures/workspace-env.json.
func TestTheLegacyEnvironmentVariablesAreTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../test/fixtures/workspace-env.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Legacy []string `json:"legacy"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	have := make([]string, 0, len(legacyWorkspaceEnv))
	for name := range legacyWorkspaceEnv {
		have = append(have, name)
	}
	slices.Sort(have)
	if !slices.Equal(have, f.Legacy) {
		t.Errorf("the operator flags %v, the fixture lists %v", have, f.Legacy)
	}
}
