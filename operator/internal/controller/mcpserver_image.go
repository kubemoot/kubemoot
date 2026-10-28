/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// conditionTypeImageResolved reports whether the image the Deployment runs
	// could be determined: spec.image as written, or the KubemootConfig image
	// spec.kubemootImage selects.
	conditionTypeImageResolved = "ImageResolved"

	reasonImageFromSpec           = "FromSpec"
	reasonImageFromKubemootConfig = "FromKubemootConfig"
	reasonImageNotSpecified       = "NotSpecified"
	reasonKubemootConfigMissing   = "KubemootConfigMissing"
	reasonKubemootImageUnset      = "KubemootImageUnset"
)

// imageResolution is the outcome of resolving an MCPServer's image.
type imageResolution struct {
	Image   string
	Reason  string
	Message string
	// QueryEngine is the artifact query engine KubemootConfig asks the server to run;
	// empty when the server does not select the artifact-access image.
	QueryEngine kubemootv1alpha1.ArtifactQueryEngine
}

// ok reports whether an image was resolved.
func (r imageResolution) ok() bool { return r.Image != "" }

// kubemootImageFor returns the KubemootConfig image an MCPServer selects with
// spec.kubemootImage, or "" when cfg does not carry it. There is no fallback: a
// Kubemoot image without a stamped version would float.
func kubemootImageFor(cfg *kubemootv1alpha1.KubemootConfig, name kubemootv1alpha1.KubemootImage) string {
	switch name {
	case kubemootv1alpha1.KubemootImageArtifactAccess:
		return cfg.Spec.Images.ArtifactAccess
	default:
		return ""
	}
}

// resolveMCPServerImage determines the image a managed MCPServer runs: spec.image
// when set, otherwise the KubemootConfig image spec.kubemootImage selects. The
// KubemootConfig is read through the client, the same informer that raises the
// watch event, so a reconcile triggered by a config change never sees an older
// image than the change carried. It never guesses a tag: a missing KubemootConfig
// or an unstamped image returns an unresolved result whose Reason and Message say
// what is missing.
func (r *MCPServerReconciler) resolveMCPServerImage(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer) (imageResolution, error) {
	if mcpServer.Spec.Image != "" {
		return imageResolution{Image: mcpServer.Spec.Image, Reason: reasonImageFromSpec, Message: "image set in spec.image"}, nil
	}
	name := mcpServer.Spec.KubemootImage
	if name == "" {
		return imageResolution{Reason: reasonImageNotSpecified, Message: "Either Image, KubemootImage, or ExternalEndpoint must be specified"}, nil
	}
	cfg := &kubemootv1alpha1.KubemootConfig{}
	if err := r.Get(ctx, client.ObjectKey{Name: DefaultKubemootConfigName}, cfg); err != nil {
		if apierrors.IsNotFound(err) {
			return imageResolution{
				Reason:  reasonKubemootConfigMissing,
				Message: fmt.Sprintf("spec.kubemootImage %q needs the KubemootConfig %q, which does not exist yet", name, DefaultKubemootConfigName),
			}, nil
		}
		return imageResolution{}, err
	}
	image := kubemootImageFor(cfg, name)
	if image == "" {
		return imageResolution{
			Reason:  reasonKubemootImageUnset,
			Message: fmt.Sprintf("KubemootConfig %q has no spec.images.%s; install the operator chart release that stamps it", DefaultKubemootConfigName, name),
		}, nil
	}
	res := imageResolution{Image: image, Reason: reasonImageFromKubemootConfig, Message: fmt.Sprintf("spec.images.%s of KubemootConfig %q", name, DefaultKubemootConfigName)}
	if name == kubemootv1alpha1.KubemootImageArtifactAccess {
		res.QueryEngine = cfg.Spec.Artifacts.QueryEngine
	}
	return res, nil
}

// artifactQueryEngineEnv is the variable artifact-access reads to choose its query engine.
const artifactQueryEngineEnv = "ARTIFACT_QUERY_ENGINE"

// hasEnv reports whether env sets the variable name.
func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}

// queryEngineOn reports whether engine asks for a query engine, as opposed to none or unset.
func queryEngineOn(engine kubemootv1alpha1.ArtifactQueryEngine) bool {
	return engine != "" && engine != kubemootv1alpha1.ArtifactQueryEngineNone
}

// withQueryEngineEnv returns mcpServer with ARTIFACT_QUERY_ENGINE set to the engine
// KubemootConfig asks for. Nothing is added for none or an unset engine, so a server
// behaves as it did before the setting existed, and a server that sets the variable
// itself keeps its value. The original is left alone.
func withQueryEngineEnv(mcpServer *kubemootv1alpha1.MCPServer, engine kubemootv1alpha1.ArtifactQueryEngine) *kubemootv1alpha1.MCPServer {
	if !queryEngineOn(engine) || hasEnv(mcpServer.Spec.Env, artifactQueryEngineEnv) {
		return mcpServer
	}
	out := mcpServer.DeepCopy()
	out.Spec.Env = append(out.Spec.Env, corev1.EnvVar{Name: artifactQueryEngineEnv, Value: string(engine)})
	return out
}

const (
	// artifactWorkspaceDir is where artifact-access materializes the artifacts a query
	// names (its ARTIFACT_DIR default) and the engine keeps its scratch files.
	artifactWorkspaceDir = "/artifacts"

	// artifactWorkspaceSizeLimit bounds the workspace emptyDir. A query names at most 8
	// artifacts of at most 32 MiB each (256 MiB), and the DuckDB engine may spill up to
	// 512 MiB of temporary data, so 768 MiB is the most one query writes; 1Gi leaves
	// headroom for a query that overlaps a slow cleanup. The kubelet evicts a pod that
	// exceeds it, so a runaway query cannot fill the node.
	artifactWorkspaceSizeLimit = "1Gi"

	// tmpDirEnv is the variable Go and the engines read for scratch space; the root
	// filesystem of a hardened pod has no writable /tmp.
	tmpDirEnv = "TMPDIR"
)

// withQueryWorkspace returns mcpServer with a size-limited emptyDir at /artifacts and
// TMPDIR pointing at it, so a pod with a read-only root filesystem has a writable,
// bounded place for the materialized artifacts and the engine's scratch files. It
// reuses the EmptyDirVolumes path the code sandbox uses. Nothing is added for none or
// an unset engine, a volume the server already mounts at /artifacts is kept, and a
// TMPDIR the server sets itself wins. The original is left alone.
func withQueryWorkspace(mcpServer *kubemootv1alpha1.MCPServer, engine kubemootv1alpha1.ArtifactQueryEngine) *kubemootv1alpha1.MCPServer {
	if !queryEngineOn(engine) {
		return mcpServer
	}
	out := mcpServer.DeepCopy()
	if !mountsPath(out.Spec.EmptyDirVolumes, artifactWorkspaceDir) {
		out.Spec.EmptyDirVolumes = append(out.Spec.EmptyDirVolumes,
			kubemootv1alpha1.EmptyDirVolume{MountPath: artifactWorkspaceDir, SizeLimit: artifactWorkspaceSizeLimit})
	}
	if !hasEnv(out.Spec.Env, tmpDirEnv) {
		out.Spec.Env = append(out.Spec.Env, corev1.EnvVar{Name: tmpDirEnv, Value: artifactWorkspaceDir})
	}
	return out
}

// mountsPath reports whether one of the emptyDir volumes is mounted at path.
func mountsPath(vols []kubemootv1alpha1.EmptyDirVolume, path string) bool {
	for _, v := range vols {
		if v.MountPath == path {
			return true
		}
	}
	return false
}

// withResolvedImage returns a copy of mcpServer whose spec.image is the resolved
// image, so the Deployment build and trial records see one image field however it
// was chosen. The original is left alone: it is the object whose spec the user owns.
func withResolvedImage(mcpServer *kubemootv1alpha1.MCPServer, image string) *kubemootv1alpha1.MCPServer {
	resolved := mcpServer.DeepCopy()
	resolved.Spec.Image = image
	return resolved
}

// setImageResolvedCondition records the resolution on the status conditions.
func setImageResolvedCondition(mcpServer *kubemootv1alpha1.MCPServer, res imageResolution) {
	status := metav1.ConditionTrue
	if !res.ok() {
		status = metav1.ConditionFalse
	}
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               conditionTypeImageResolved,
		Status:             status,
		Reason:             res.Reason,
		Message:            res.Message,
		ObservedGeneration: mcpServer.Generation,
	})
}

// reconcileUnresolvedImage reports an MCPServer whose image could not be resolved:
// phase Error with the reason, requeued by updateStatus so it recovers once the
// KubemootConfig gains the image (the KubemootConfig watch also re-enqueues it).
func (r *MCPServerReconciler) reconcileUnresolvedImage(ctx context.Context, mcpServer *kubemootv1alpha1.MCPServer, res imageResolution) (ctrl.Result, error) {
	setImageResolvedCondition(mcpServer, res)
	return r.updateStatus(ctx, mcpServer, "Error", false, res.Message, withImage(""))
}

// clearImageResolution drops the image status of a server that no longer runs a
// managed image (switched to an externalEndpoint).
func clearImageResolution(mcpServer *kubemootv1alpha1.MCPServer) {
	meta.RemoveStatusCondition(&mcpServer.Status.Conditions, conditionTypeImageResolved)
	mcpServer.Status.Image = ""
}
