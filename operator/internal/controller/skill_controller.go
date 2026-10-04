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

// skill_controller.go reconciles Skill CRs. For each Skill change the
// controller re-syncs the per-crew skills ConfigMap (crew-<crew>-skills)
// and re-enqueues the coordinator agent so syncCrewResumes picks up any
// skill additions or removals.
//
// Phase 1 scope: instruction-only skills (ADL content only). RAGSources and
// MCPServers declared on SkillSpec are schema-reserved but not acted upon.
package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

const skillFinalizer = "kubemoot.ai/skill-cleanup"

// SkillReconciler reconciles Skill objects.
type SkillReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	NATSPublisher *kubemootnats.Publisher
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=skills,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=skills/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=skills/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles Skill reconciliation.
func (r *SkillReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	skill := &kubemootv1alpha1.Skill{}
	if err := r.Get(ctx, req.NamespacedName, skill); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	crewName := skill.Labels[crewLabelKey]
	if crewName == "" {
		log.V(1).Info("Skill has no crew label; skipping ConfigMap sync", "skill", skill.Name)
		return ctrl.Result{}, nil
	}

	if !skill.DeletionTimestamp.IsZero() {
		r.handleSkillDeletion(ctx, skill, crewName)
		if err := removeFinalizer(ctx, r.Client, skill, skillFinalizer); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(skill, skillFinalizer) {
		if err := addFinalizer(ctx, r.Client, skill, skillFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		return requeueNow(), nil
	}

	r.syncSkillConfigMap(ctx, crewName, skill.Namespace)
	r.enqueueCoordinators(ctx, crewName, skill.Namespace)
	return ctrl.Result{}, nil
}

// handleSkillDeletion rebuilds the crew ConfigMap excluding terminating skills
// and updates the coordinator pool-hash annotation. Deletes the ConfigMap when
// no non-terminating skills remain.
func (r *SkillReconciler) handleSkillDeletion(ctx context.Context, skill *kubemootv1alpha1.Skill, crewName string) {
	r.syncSkillConfigMap(ctx, crewName, skill.Namespace)
	r.enqueueCoordinators(ctx, crewName, skill.Namespace)
}

// syncSkillConfigMap builds (or updates) the per-crew ConfigMap that holds all
// skill bodies for the crew. Each skill gets its own key (<name>.txt) plus a
// skills-index.txt summary. Deletes the ConfigMap when no active skills remain.
func (r *SkillReconciler) syncSkillConfigMap(ctx context.Context, crewName, namespace string) {
	log := logf.FromContext(ctx)

	items, err := r.listSortedSkills(ctx, namespace, crewName)
	if err != nil {
		log.Error(err, "Failed to list Skills for ConfigMap sync", "crew", crewName)
		return
	}

	cmName := fmt.Sprintf("crew-%s-skills", crewName)

	if len(items) == 0 {
		r.deleteSkillConfigMap(ctx, cmName, namespace, crewName)
		return
	}

	data := buildSkillConfigMapData(items)
	cmLabels := map[string]string{
		labelManagedBy: managedByValue,
		labelComponent: "skills",
		crewLabelKey:   crewName,
	}

	existing := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, existing)
	if apierrors.IsNotFound(err) {
		r.createSkillConfigMap(ctx, cmName, namespace, cmLabels, data)
		return
	}
	if err != nil {
		log.Error(err, "Failed to get skills ConfigMap", "name", cmName)
		return
	}
	if configMapDataEqual(existing.Data, data) {
		return
	}
	existing.Data = data
	if err := r.Update(ctx, existing); err != nil {
		log.Error(err, "Failed to update skills ConfigMap", "name", cmName)
		return
	}
	log.Info("Updated skills ConfigMap", "name", cmName, "crew", crewName, "skills", len(items))
}

// deleteSkillConfigMap removes the skills ConfigMap if it exists.
func (r *SkillReconciler) deleteSkillConfigMap(ctx context.Context, cmName, namespace, crewName string) {
	log := logf.FromContext(ctx)
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, existing)
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		log.Error(err, "Failed to get skills ConfigMap for deletion", "name", cmName)
		return
	}
	if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to delete skills ConfigMap", "name", cmName)
		return
	}
	log.Info("Deleted skills ConfigMap (no active skills remain)", "name", cmName, "crew", crewName)
}

// listSortedSkills returns active (non-terminating) Skills for the crew sorted
// by order then name. Terminating skills are excluded so a skill mid-deletion
// never appears in the ConfigMap or resume pool.
func (r *SkillReconciler) listSortedSkills(ctx context.Context, namespace, crewName string) ([]kubemootv1alpha1.Skill, error) {
	list := &kubemootv1alpha1.SkillList{}
	if err := r.List(ctx, list,
		client.InNamespace(namespace),
		client.MatchingLabels{crewLabelKey: crewName},
	); err != nil {
		return nil, err
	}
	active := list.Items[:0]
	for i := range list.Items {
		if list.Items[i].DeletionTimestamp.IsZero() {
			active = append(active, list.Items[i])
		}
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].Spec.Order != active[j].Spec.Order {
			return active[i].Spec.Order < active[j].Spec.Order
		}
		return active[i].Name < active[j].Name
	})
	return active, nil
}

// buildSkillConfigMapData assembles the ConfigMap data from a sorted Skill list.
func buildSkillConfigMapData(items []kubemootv1alpha1.Skill) map[string]string {
	data := make(map[string]string, len(items)+1)
	var idx strings.Builder
	for _, s := range items {
		key := s.Name + ".txt"
		data[key] = s.Spec.Content
		if idx.Len() > 0 {
			idx.WriteString("\n")
		}
		idx.WriteString(s.Name)
		idx.WriteString(": ")
		idx.WriteString(s.Spec.Description)
	}
	data["skills-index.txt"] = idx.String()
	return data
}

// createSkillConfigMap creates the skills ConfigMap.
func (r *SkillReconciler) createSkillConfigMap(ctx context.Context, name, namespace string, labels, data map[string]string) {
	log := logf.FromContext(ctx)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Data: data,
	}
	if err := r.Create(ctx, cm); err != nil {
		log.Error(err, "Failed to create skills ConfigMap", "name", name)
		return
	}
	log.Info("Created skills ConfigMap", "name", name)
}

// configMapDataEqual reports whether two ConfigMap data maps are equal.
func configMapDataEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// computePoolHash returns a 16-char hex hash over the non-terminating skill
// pool: name + order + resourceVersion per skill, sorted by name. Returns "0"
// when the pool is empty so it always differs from a populated pool hash.
func computePoolHash(items []kubemootv1alpha1.Skill) string {
	if len(items) == 0 {
		return "0"
	}
	sorted := make([]kubemootv1alpha1.Skill, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	for _, s := range sorted {
		// Writes to a hash never fail.
		_, _ = fmt.Fprintf(h, "%s|%d|%s\n", s.Name, s.Spec.Order, s.ResourceVersion)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// enqueueCoordinators lists coordinators for the crew and triggers their Agent
// reconcile so syncCrewResumes picks up any Skill pool changes. The annotation
// value is a deterministic pool hash so only real pool changes cause an update.
func (r *SkillReconciler) enqueueCoordinators(ctx context.Context, crewName, namespace string) {
	log := logf.FromContext(ctx)

	skills, err := r.listSortedSkills(ctx, namespace, crewName)
	if err != nil {
		log.V(1).Info("enqueueCoordinators: list skills failed", "error", err)
		return
	}
	poolHash := computePoolHash(skills)

	agents := &kubemootv1alpha1.AgentList{}
	if err := r.List(ctx, agents,
		client.InNamespace(namespace),
		client.MatchingLabels{crewLabelKey: crewName},
	); err != nil {
		log.V(1).Info("enqueueCoordinators: list agents failed", "error", err)
		return
	}
	for i := range agents.Items {
		a := &agents.Items[i]
		if a.Spec.DiscussRole != roleCoordinator {
			continue
		}
		if a.Annotations == nil {
			a.Annotations = make(map[string]string)
		}
		if a.Annotations["kubemoot.ai/skill-pool-hash"] == poolHash {
			continue
		}
		a.Annotations["kubemoot.ai/skill-pool-hash"] = poolHash
		if err := r.Update(ctx, a); err != nil && !apierrors.IsNotFound(err) {
			log.V(1).Info("enqueueCoordinators: update failed", "agent", a.Name, "error", err)
		}
	}
}

// SetupWithManager registers the SkillReconciler.
func (r *SkillReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Scheme = mgr.GetScheme()
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.Skill{}).
		Complete(r)
}
