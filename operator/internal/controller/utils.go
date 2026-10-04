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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// immediateRequeueDelay is the base delay of the default controller rate limiter.
const immediateRequeueDelay = 5 * time.Millisecond

// requeueNow is the result for a reconcile that added a finalizer and must run again
// at once to act on the updated object. Its delay is the default rate limiter's 5ms
// base delay, but RequeueAfter carries no per-item backoff: use it only where the
// next reconcile cannot repeat the same outcome (a second finalizer add is a no-op,
// and an update conflict returns an error, which does back off).
func requeueNow() ctrl.Result {
	return ctrl.Result{RequeueAfter: immediateRequeueDelay}
}

const deploymentHashAnnotation = "kubemoot.ai/deployment-spec-hash"

// promptHashAnnotation carries the assembled system-prompt hash on the
// Deployment pod template. It is part of the pod template, so changing it rolls
// the pod; it is also folded into computeDeploymentHash so a prompt-only change
// triggers a Deployment update. This is how a PromptModule edit reaches the
// running agent (which caches the prompt at startup).
const promptHashAnnotation = "kubemoot.ai/prompt-hash"

// hashString returns a short hex digest of s (first 8 bytes of sha256), matching
// computeDeploymentHash's brevity. Used for the pod-template prompt hash.
func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8])
}

// computeDeploymentHash hashes the ENTIRE desired pod template plus replicas, so
// any change the operator makes to a container or init container (image, command,
// args, env, resources, securityContext, volume mounts, probes, ...) bumps the
// hash and triggers a full spec replace. A previous hand-picked subset omitted
// init-container securityContext, so a sidecar security change silently no-oped
// and the Deployment went stale (it had to be deleted by hand). Hashing the whole
// template is comprehensive and future-proof: no field can be forgotten.
//
// The hash is always computed on the operator's DESIRED deployment, on both
// create and update, and compared to the previously stored desired hash, so it
// never sees API-server-defaulted fields and cannot churn between reconciles.
func computeDeploymentHash(deployment *appsv1.Deployment) string {
	var replicas int32
	if deployment.Spec.Replicas != nil {
		replicas = *deployment.Spec.Replicas
	}
	templateJSON, err := json.Marshal(deployment.Spec.Template)
	if err != nil {
		// Marshalling a pod template does not realistically fail; fall back to a
		// stable representation so the hash never panics.
		templateJSON = []byte(fmt.Sprintf("%+v", deployment.Spec.Template))
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("replicas=%d|%s", replicas, templateJSON)))
	return fmt.Sprintf("%x", hash[:8])
}

// matchVersionConstraintShared checks if version satisfies the constraint.
// Supports: >=1.2.8, ^2.0.0, ~1.5.0, 1.x, <2.0.0, exact match.
// Used by both MCPCatalog and MCPGateway controllers.
func matchVersionConstraintShared(constraint, version string) bool {
	version = strings.TrimPrefix(version, "v")
	serverParts := parseVersion(version)
	if serverParts == nil {
		return false
	}

	// Dispatch by prefix using a table of handlers
	type prefixHandler struct {
		prefix string
		fn     func(string, []int) bool
	}
	handlers := []prefixHandler{
		{"^", func(c string, sv []int) bool { return matchCaret(c, sv) }},
		{"~", func(c string, sv []int) bool { return matchTilde(c, sv) }},
		{">=", func(c string, sv []int) bool { return matchCompare(c, sv, func(cmp int) bool { return cmp >= 0 }) }},
		{">", func(c string, sv []int) bool { return matchCompare(c, sv, func(cmp int) bool { return cmp > 0 }) }},
		{"<=", func(c string, sv []int) bool { return matchCompare(c, sv, func(cmp int) bool { return cmp <= 0 }) }},
		{"<", func(c string, sv []int) bool { return matchCompare(c, sv, func(cmp int) bool { return cmp < 0 }) }},
	}

	for _, h := range handlers {
		if strings.HasPrefix(constraint, h.prefix) {
			return h.fn(strings.TrimPrefix(constraint, h.prefix), serverParts)
		}
	}

	// Wildcard (X.x or X.*)
	if strings.HasSuffix(constraint, ".x") || strings.HasSuffix(constraint, ".*") {
		majorStr := strings.TrimSuffix(strings.TrimSuffix(constraint, ".x"), ".*")
		major, err := parseInt(majorStr)
		if err != nil {
			return false
		}
		return serverParts[0] == major
	}

	// Exact version match
	constraintParts := parseVersion(constraint)
	if constraintParts == nil {
		return false
	}
	return compareVersions(serverParts, constraintParts) == 0
}

// matchCaret checks ^X.Y.Z — compatible with major version.
func matchCaret(constraintVersion string, serverParts []int) bool {
	constraintParts := parseVersion(constraintVersion)
	if constraintParts == nil {
		return false
	}
	if serverParts[0] != constraintParts[0] {
		return false
	}
	return compareVersions(serverParts, constraintParts) >= 0
}

// matchTilde checks ~X.Y.Z — compatible with minor version.
func matchTilde(constraintVersion string, serverParts []int) bool {
	constraintParts := parseVersion(constraintVersion)
	if constraintParts == nil {
		return false
	}
	if serverParts[0] != constraintParts[0] || serverParts[1] != constraintParts[1] {
		return false
	}
	return compareVersions(serverParts, constraintParts) >= 0
}

// matchCompare checks a comparison constraint (>=, >, <=, <).
func matchCompare(constraintVersion string, serverParts []int, cmpFn func(int) bool) bool {
	constraintParts := parseVersion(constraintVersion)
	if constraintParts == nil {
		return false
	}
	return cmpFn(compareVersions(serverParts, constraintParts))
}

// httpGet issues a GET bound to ctx, so a cancelled reconcile stops the request.
func httpGet(ctx context.Context, httpClient *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return httpClient.Do(req)
}
