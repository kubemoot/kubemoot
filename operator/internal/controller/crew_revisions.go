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
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// namespaceCrewEntry is one crew's value in the kubemoot.ai/crews namespace annotation.
type namespaceCrewEntry struct {
	Source      string `json:"source,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Revision    string `json:"revision,omitempty"`
	Channel     string `json:"channel,omitempty"`
	CrewVersion string `json:"crewVersion,omitempty"`
	DeployedAt  string `json:"deployedAt,omitempty"`
}

// isEmpty reports whether the entry carries no provenance at all.
func (e namespaceCrewEntry) isEmpty() bool {
	return e == namespaceCrewEntry{}
}

// crewProvenance reads the deployment provenance from the Crew's annotations and labels.
func crewProvenance(crew *kubemootv1alpha1.Crew) namespaceCrewEntry {
	a := crew.Annotations
	return namespaceCrewEntry{
		Source:      a[annoCrewForgeSource],
		Owner:       a[annoCrewForgeOwner],
		Revision:    a[annoCrewForgeRevision],
		Channel:     a[annoCrewForgeChannel],
		CrewVersion: crew.Labels[crewVersionLabel],
		DeployedAt:  a[annoCrewForgeDeployedAt],
	}
}

// revisionFromProvenance builds a status revision entry, or false when the Crew
// carries none of the revision, deployed-at, or crew-version markers.
func revisionFromProvenance(p namespaceCrewEntry, now metav1.Time) (kubemootv1alpha1.CrewRevision, bool) {
	if p.Revision == "" && p.DeployedAt == "" && p.CrewVersion == "" {
		return kubemootv1alpha1.CrewRevision{}, false
	}
	return kubemootv1alpha1.CrewRevision{
		Revision:    p.Revision,
		Source:      p.Source,
		Owner:       p.Owner,
		Channel:     p.Channel,
		CrewVersion: p.CrewVersion,
		DeployedAt:  p.DeployedAt,
		ObservedAt:  now,
	}, true
}

// sameRevision compares the (revision, deployed-at, crew-version) identity of two
// entries. Source, owner, and channel are deliberately not part of the identity:
// a change to only those does not mark a new deployment.
func sameRevision(a, b kubemootv1alpha1.CrewRevision) bool {
	return a.Revision == b.Revision && a.DeployedAt == b.DeployedAt && a.CrewVersion == b.CrewVersion
}

// appendRevision prepends next unless it matches the newest entry, keeping at
// most maxCrewRevisions. It reports whether the history changed.
func appendRevision(history []kubemootv1alpha1.CrewRevision, next kubemootv1alpha1.CrewRevision) ([]kubemootv1alpha1.CrewRevision, bool) {
	if len(history) > 0 && sameRevision(history[0], next) {
		return history, false
	}
	out := make([]kubemootv1alpha1.CrewRevision, 0, min(len(history)+1, maxCrewRevisions))
	out = append(out, next)
	for _, h := range history {
		if len(out) == maxCrewRevisions {
			break
		}
		out = append(out, h)
	}
	return out, true
}

// recordCrewRevision updates crew.Status.Revisions in memory; the caller's
// status update persists it.
func recordCrewRevision(crew *kubemootv1alpha1.Crew, p namespaceCrewEntry, now metav1.Time) {
	next, ok := revisionFromProvenance(p, now)
	if !ok {
		return
	}
	crew.Status.Revisions, _ = appendRevision(crew.Status.Revisions, next)
}

// parseNamespaceCrews decodes the annotation value. Malformed JSON decodes to an
// empty map (reported by the bool) so the operator rewrites it rather than failing.
func parseNamespaceCrews(raw string) (map[string]namespaceCrewEntry, bool) {
	out := map[string]namespaceCrewEntry{}
	if raw == "" {
		return out, true
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return map[string]namespaceCrewEntry{}, false
	}
	return out, true
}

// mergeNamespaceCrews returns the annotation value with crew's entry set (or
// removed when entry is empty), and whether the value changed. An empty result
// string means the annotation should be removed.
func mergeNamespaceCrews(raw, crew string, entry namespaceCrewEntry) (string, bool) {
	crews, wellFormed := parseNamespaceCrews(raw)
	if wellFormed && entryUnchanged(crews, crew, entry) {
		return raw, false
	}
	if entry.isEmpty() {
		delete(crews, crew)
	} else {
		crews[crew] = entry
	}
	return encodeNamespaceCrews(crews), true
}

// entryUnchanged reports whether setting entry for crew would leave crews as is.
func entryUnchanged(crews map[string]namespaceCrewEntry, crew string, entry namespaceCrewEntry) bool {
	current, present := crews[crew]
	if !present {
		return entry.isEmpty()
	}
	return current == entry
}

// encodeNamespaceCrews renders the map as JSON; an empty map renders as "".
func encodeNamespaceCrews(crews map[string]namespaceCrewEntry) string {
	if len(crews) == 0 {
		return ""
	}
	b, err := json.Marshal(crews)
	if err != nil {
		return ""
	}
	return string(b)
}

// reconcileNamespaceCrews mirrors the Crew's provenance p into the namespace's
// kubemoot.ai/crews annotation.
func (r *CrewReconciler) reconcileNamespaceCrews(ctx context.Context, crew *kubemootv1alpha1.Crew, p namespaceCrewEntry) {
	r.patchNamespaceCrews(ctx, crew.Namespace, crew.Name, p)
}

// removeNamespaceCrew drops the Crew's entry from the namespace annotation.
func (r *CrewReconciler) removeNamespaceCrew(ctx context.Context, crew *kubemootv1alpha1.Crew) {
	r.patchNamespaceCrews(ctx, crew.Namespace, crew.Name, namespaceCrewEntry{})
}

// patchNamespaceCrews sets or removes one crew's entry with a merge patch that
// touches only the kubemoot.ai/crews annotation. Failures are logged, never returned.
func (r *CrewReconciler) patchNamespaceCrews(ctx context.Context, namespace, crew string, entry namespaceCrewEntry) {
	log := logf.FromContext(ctx)
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to read namespace for crew provenance", "namespace", namespace)
		}
		return
	}
	raw := ns.Annotations[annoNamespaceCrews]
	if _, ok := parseNamespaceCrews(raw); !ok {
		log.Info("Replacing malformed namespace crews annotation", "namespace", namespace)
	}
	value, changed := mergeNamespaceCrews(raw, crew, entry)
	if !changed {
		return
	}
	if err := r.Patch(ctx, ns, client.RawPatch(types.MergePatchType, namespaceCrewsPatch(value))); err != nil {
		log.Error(err, "Failed to patch namespace crews annotation", "namespace", namespace)
	}
}

// namespaceCrewsPatch builds a JSON merge patch that sets the annotation, or
// removes it when value is empty.
func namespaceCrewsPatch(value string) []byte {
	var v any
	if value != "" {
		v = value
	}
	patch := map[string]any{"metadata": map[string]any{"annotations": map[string]any{annoNamespaceCrews: v}}}
	b, _ := json.Marshal(patch)
	return b
}
