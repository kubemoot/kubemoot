package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/workspacetools"
)

const (
	// conditionTypeDeprecatedNames lists the old workspace names an MCPServer still uses.
	conditionTypeDeprecatedNames = "DeprecatedNames"
	reasonDeprecatedNamesInUse   = "OldWorkspaceNames"
)

// deprecated is the published list of deprecated names; the condition, the mount alias and the
// image check all read it.
var deprecated = workspacetools.DeprecatedNames()

// legacyWorkspaceDir is the mount path the workspace had before it was named: a volume mounted
// at it is also mounted at workspaceDir, and the other way round, for one release.
var legacyWorkspaceDir = deprecated.Mount

// emptyDirVolumeMounts returns the mounts of the MCPServer's emptyDir volumes, each at its own
// path and, for a workspace server, at the other workspace path too (see emptyDirMountPaths).
// The main container and the sidecars take the same mounts from here.
func emptyDirVolumeMounts(mcpServer *kubemootv1alpha1.MCPServer) []corev1.VolumeMount {
	var mounts []corev1.VolumeMount
	for i, ev := range mcpServer.Spec.EmptyDirVolumes {
		for _, path := range emptyDirMountPaths(mcpServer, ev) {
			mounts = append(mounts, corev1.VolumeMount{Name: emptyDirVolumeName(i), MountPath: path})
		}
	}
	return mounts
}

// appendFreeMounts appends the mounts of extra whose path the container does not already mount.
func appendFreeMounts(have, extra []corev1.VolumeMount) []corev1.VolumeMount {
	for _, m := range extra {
		if !mountedAt(have, m.MountPath) {
			have = append(have, m)
		}
	}
	return have
}

// mountedAt reports whether one of the mounts is at path.
func mountedAt(mounts []corev1.VolumeMount, path string) bool {
	for _, m := range mounts {
		if m.MountPath == path {
			return true
		}
	}
	return false
}

// emptyDirMountPaths returns the paths an emptyDir volume is mounted at: its own, and for a
// workspace server's volume at /kubemoot/workspace or /artifacts the other of the two, unless another
// volume of the server mounts it. A crew whose prompts and sandbox code still say /artifacts and
// one that says /kubemoot/workspace both find the files; the one-release alias goes away with the old
// name. A server that is neither the workspace server nor a pod with sidecars (the code sandbox
// with its materializer) keeps the one mount it asked for.
func emptyDirMountPaths(mcpServer *kubemootv1alpha1.MCPServer, ev kubemootv1alpha1.EmptyDirVolume) []string {
	paths := []string{ev.MountPath}
	alias := workspaceAlias(ev.MountPath)
	if alias != "" && usesWorkspace(mcpServer) && !pathTaken(mcpServer, alias) {
		paths = append(paths, alias)
	}
	return paths
}

// usesWorkspace reports whether the MCPServer works with the workspace: it runs the
// workspace-access image, or it has sidecars (the materializer beside the code sandbox).
func usesWorkspace(mcpServer *kubemootv1alpha1.MCPServer) bool {
	return isWorkspaceAccessImage(mcpServer.Spec.KubemootImage) || len(mcpServer.Spec.Sidecars) > 0
}

// pathTaken reports whether one of the server's own volumes (emptyDir or secret) is mounted at path.
func pathTaken(mcpServer *kubemootv1alpha1.MCPServer, path string) bool {
	if mountsPath(mcpServer.Spec.EmptyDirVolumes, path) {
		return true
	}
	for _, sv := range mcpServer.Spec.SecretVolumes {
		if sv.MountPath == path {
			return true
		}
	}
	return false
}

// workspaceAlias returns the other workspace mount path of path, or "" when path is neither.
func workspaceAlias(path string) string {
	switch path {
	case workspaceDir:
		return legacyWorkspaceDir
	case legacyWorkspaceDir:
		return workspaceDir
	default:
		return ""
	}
}

// legacyWorkspaceEnv names the variables workspace-access read before the workspace had its name,
// from the published list.
var legacyWorkspaceEnv = func() map[string]bool {
	m := make(map[string]bool, len(deprecated.EnvNames))
	for _, name := range deprecated.EnvNames {
		m[name] = true
	}
	return m
}()

// deprecatedWorkspaceNames lists the old workspace names an MCPServer spec still uses, each as
// the thing to change.
func deprecatedWorkspaceNames(mcpServer *kubemootv1alpha1.MCPServer) []string {
	var found []string
	if mcpServer.Spec.KubemootImage == kubemootv1alpha1.KubemootImageArtifactAccess {
		found = append(found, "spec.kubemootImage artifactAccess is workspaceAccess")
	}
	if isOldWorkspaceImage(mcpServer.Spec.Image) {
		found = append(found, "image "+mcpServer.Spec.Image+" is the old artifact-access image: it reads ARTIFACT_QUERY_ENGINE "+
			"and ignores the WORKSPACE_QUERY_ENGINE the operator sets, so use a workspace-access image")
	}
	for _, e := range mcpServer.Spec.Env {
		if legacyWorkspaceEnv[e.Name] {
			found = append(found, "env "+e.Name+" is WORKSPACE_"+strings.TrimPrefix(e.Name, deprecated.EnvPrefix))
		}
	}
	if usesWorkspace(mcpServer) && mountsPath(mcpServer.Spec.EmptyDirVolumes, legacyWorkspaceDir) {
		found = append(found, "the emptyDir mount /artifacts is /kubemoot/workspace")
	}
	return found
}

// isOldWorkspaceImage reports whether an image reference is the image the component had before it
// was renamed: its repository name is artifact-access.
func isOldWorkspaceImage(image string) bool {
	repo, _, _ := strings.Cut(image, ":")
	return repo == deprecated.ImageRepo || strings.HasSuffix(repo, "/"+deprecated.ImageRepo)
}

// setDeprecatedNamesCondition records the old workspace names the MCPServer uses on its status,
// and removes the condition when it uses none.
func setDeprecatedNamesCondition(mcpServer *kubemootv1alpha1.MCPServer) {
	found := deprecatedWorkspaceNames(mcpServer)
	if len(found) == 0 {
		meta.RemoveStatusCondition(&mcpServer.Status.Conditions, conditionTypeDeprecatedNames)
		return
	}
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               conditionTypeDeprecatedNames,
		Status:             metav1.ConditionTrue,
		Reason:             reasonDeprecatedNamesInUse,
		Message:            "old names still work and are removed in the next release: " + strings.Join(found, "; "),
		ObservedGeneration: mcpServer.Generation,
	})
}
