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
	return imageResolution{Image: image, Reason: reasonImageFromKubemootConfig, Message: fmt.Sprintf("spec.images.%s of KubemootConfig %q", name, DefaultKubemootConfigName)}, nil
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
