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
	"strings"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func skillScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add kubemoot scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	return s
}

func mkSkill(name, crew string, order int32, desc, content string) *kubemootv1alpha1.Skill {
	return &kubemootv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testCrewNamespace,
			Labels:    map[string]string{crewLabelKey: crew},
		},
		Spec: kubemootv1alpha1.SkillSpec{
			Description: desc,
			Content:     content,
			Order:       order,
		},
	}
}

// TestSkillConfigMap_Create verifies that a Skill CR causes the per-crew
// ConfigMap to be created with the correct data keys.
func TestSkillConfigMap_Create(t *testing.T) {
	ctx := context.Background()
	scheme := skillScheme(t)

	skill := mkSkill(testSkillGPUBasics, testCrewName, 100, "GPU diagnostics", testGPUSkillContent)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skill).Build()
	r := &SkillReconciler{Client: cli, Scheme: scheme}

	r.syncSkillConfigMap(ctx, testCrewName, testCrewNamespace)

	cm := &corev1.ConfigMap{}
	if err := cli.Get(ctx, types.NamespacedName{Name: testCrewSkillsConfigMap, Namespace: testCrewNamespace}, cm); err != nil {
		t.Fatalf("expected ConfigMap crew-homelab-pilot-skills to exist: %v", err)
	}
	if _, ok := cm.Data["gpu-basics.txt"]; !ok {
		t.Error("expected gpu-basics.txt key in ConfigMap data")
	}
	if cm.Data["gpu-basics.txt"] != testGPUSkillContent {
		t.Errorf("unexpected content: %q", cm.Data["gpu-basics.txt"])
	}
	if !strings.Contains(cm.Data["skills-index.txt"], "gpu-basics: GPU diagnostics") {
		t.Errorf("skills-index.txt missing entry: %q", cm.Data["skills-index.txt"])
	}
	if cm.Labels[crewLabelKey] != testCrewName {
		t.Errorf("ConfigMap missing crew label, got: %v", cm.Labels)
	}
}

// TestSkillConfigMap_Update verifies that adding a second Skill updates the
// ConfigMap to include both skill bodies and updates the index.
func TestSkillConfigMap_Update(t *testing.T) {
	ctx := context.Background()
	scheme := skillScheme(t)

	skill1 := mkSkill(testSkillGPUBasics, testCrewName, 100, "GPU diagnostics", testGPUSkillContent)
	skill2 := mkSkill("network-diag", testCrewName, 200, "Network diagnostics", "WHEN asked about network THEN check ping")
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skill1, skill2).Build()
	r := &SkillReconciler{Client: cli, Scheme: scheme}

	r.syncSkillConfigMap(ctx, testCrewName, testCrewNamespace)

	cm := &corev1.ConfigMap{}
	if err := cli.Get(ctx, types.NamespacedName{Name: testCrewSkillsConfigMap, Namespace: testCrewNamespace}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if _, ok := cm.Data["network-diag.txt"]; !ok {
		t.Error("expected network-diag.txt key in updated ConfigMap")
	}
	if !strings.Contains(cm.Data["skills-index.txt"], "network-diag: Network diagnostics") {
		t.Errorf("index missing network-diag entry: %q", cm.Data["skills-index.txt"])
	}
	// gpu-basics has lower order so it appears first in the index
	gpuIdx := strings.Index(cm.Data["skills-index.txt"], testSkillGPUBasics)
	netIdx := strings.Index(cm.Data["skills-index.txt"], "network-diag")
	if gpuIdx < 0 || netIdx < 0 || gpuIdx > netIdx {
		t.Errorf("index order wrong: gpu-basics (%d) should precede network-diag (%d)", gpuIdx, netIdx)
	}
}

// TestSkillConfigMap_EmptyCrewLabel verifies that the Reconcile loop skips
// ConfigMap creation when the Skill has no crew label.
func TestSkillConfigMap_EmptyCrewLabel(t *testing.T) {
	ctx := context.Background()
	scheme := skillScheme(t)

	unlabeled := &kubemootv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: testOrphan, Namespace: testCrewNamespace},
		Spec:       kubemootv1alpha1.SkillSpec{Description: testOrphan, Content: "body"},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unlabeled).Build()
	r := &SkillReconciler{Client: cli, Scheme: scheme}

	// The Reconcile function bails out before syncSkillConfigMap when the
	// crew label is missing. Simulate by inspecting what Reconcile does:
	// it reads skill.Labels[crewLabelKey] and returns early if empty.
	crewName := unlabeled.Labels[crewLabelKey]
	if crewName != "" {
		// If this branch is hit the test setup is wrong.
		t.Fatal("expected unlabeled skill to have no crew label")
	}

	// Direct call to Reconcile path: early return means no ConfigMap.
	// Verify by confirming that no ConfigMap is found in the namespace.
	cmList := &corev1.ConfigMapList{}
	if err := cli.List(ctx, cmList); err != nil {
		t.Fatalf("list configmaps: %v", err)
	}
	if len(cmList.Items) != 0 {
		t.Errorf("expected no ConfigMaps before reconcile, got %d", len(cmList.Items))
	}

	// Now call reconcile; the controller should return without creating a ConfigMap.
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testOrphan, Namespace: testCrewNamespace},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	cmList2 := &corev1.ConfigMapList{}
	if err := cli.List(ctx, cmList2); err != nil {
		t.Fatalf("list configmaps after reconcile: %v", err)
	}
	if len(cmList2.Items) != 0 {
		t.Errorf("expected no ConfigMaps after reconcile (no crew label), got %d", len(cmList2.Items))
	}
}

// TestSkillReconcile_FinalizerAdded verifies that Reconcile adds the skill
// finalizer on first reconcile of a new Skill.
func TestSkillReconcile_FinalizerAdded(t *testing.T) {
	ctx := context.Background()
	scheme := skillScheme(t)

	skill := mkSkill(testSkillGPUBasics, testCrewName, 100, "GPU diagnostics", testGPUSkillContent)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skill).Build()
	r := &SkillReconciler{Client: cli, Scheme: scheme}

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testSkillGPUBasics, Namespace: testCrewNamespace},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	updated := &kubemootv1alpha1.Skill{}
	if err := cli.Get(ctx, types.NamespacedName{Name: testSkillGPUBasics, Namespace: testCrewNamespace}, updated); err != nil {
		t.Fatalf("get skill after reconcile: %v", err)
	}
	found := false
	for _, f := range updated.Finalizers {
		if f == skillFinalizer {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected finalizer %q on skill, got: %v", skillFinalizer, updated.Finalizers)
	}
}

// TestSkillReconcile_LastSkillDeletion verifies that reconciling a terminating
// skill (DeletionTimestamp set, finalizer present) removes the ConfigMap and
// removes the finalizer.
func TestSkillReconcile_LastSkillDeletion(t *testing.T) {
	ctx := context.Background()
	scheme := skillScheme(t)

	now := metav1.Now()
	skill := &kubemootv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testSkillGPUBasics,
			Namespace:         testCrewNamespace,
			Labels:            map[string]string{crewLabelKey: testCrewName},
			Finalizers:        []string{skillFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: kubemootv1alpha1.SkillSpec{
			Description: "GPU diagnostics",
			Content:     testGPUSkillContent,
			Order:       100,
		},
	}
	// Pre-create a ConfigMap simulating what a prior reconcile would have left.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testCrewSkillsConfigMap,
			Namespace: testCrewNamespace,
			Labels: map[string]string{
				labelManagedBy: managedByValue,
				labelComponent: testSkills,
				crewLabelKey:   testCrewName,
			},
		},
		Data: map[string]string{
			"gpu-basics.txt":   testGPUSkillContent,
			"skills-index.txt": "gpu-basics: GPU diagnostics",
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skill, cm).Build()
	r := &SkillReconciler{Client: cli, Scheme: scheme}

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testSkillGPUBasics, Namespace: testCrewNamespace},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// ConfigMap should have been deleted (no active skills remain).
	cmList := &corev1.ConfigMapList{}
	if err := cli.List(ctx, cmList, client.InNamespace(testCrewNamespace)); err != nil {
		t.Fatalf("list configmaps: %v", err)
	}
	for _, c := range cmList.Items {
		if c.Name == testCrewSkillsConfigMap {
			t.Errorf("expected ConfigMap crew-homelab-pilot-skills to be deleted, but it still exists")
		}
	}

	// Finalizer should have been removed. When the fake client removes the last
	// finalizer from an object with DeletionTimestamp set, it deletes the object
	// entirely. Not-found means the finalizer was removed successfully.
	updated := &kubemootv1alpha1.Skill{}
	getErr := cli.Get(ctx, types.NamespacedName{Name: testSkillGPUBasics, Namespace: testCrewNamespace}, updated)
	if getErr == nil {
		for _, f := range updated.Finalizers {
			if f == skillFinalizer {
				t.Errorf("expected finalizer %q to be removed, but it is still present", skillFinalizer)
			}
		}
	}
	// not-found is also acceptable: fake client purged the object after last finalizer removed
}

// TestSkillReconcile_CoordinatorPoolHashChanges verifies that the coordinator
// pool-hash annotation is set when a skill exists and changes when a second
// skill is added.
func TestSkillReconcile_CoordinatorPoolHashChanges(t *testing.T) {
	ctx := context.Background()
	scheme := skillScheme(t)

	coord := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testHomelabCoordinator,
			Namespace: testCrewNamespace,
			Labels:    map[string]string{crewLabelKey: testCrewName},
		},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: roleCoordinator},
	}
	skill1 := mkSkill(testSkillGPUBasics, testCrewName, 100, "GPU diagnostics", testGPUSkillContent)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(coord, skill1).Build()
	r := &SkillReconciler{Client: cli, Scheme: scheme}

	// First reconcile: adds finalizer, requeues.
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testSkillGPUBasics, Namespace: testCrewNamespace},
	})
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	// Second reconcile: finalizer now present, runs sync + enqueue.
	_, err = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testSkillGPUBasics, Namespace: testCrewNamespace},
	})
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	// Coordinator should have a non-empty, non-"0" pool-hash annotation.
	updatedCoord := &kubemootv1alpha1.Agent{}
	if err := cli.Get(ctx, types.NamespacedName{Name: testHomelabCoordinator, Namespace: testCrewNamespace}, updatedCoord); err != nil {
		t.Fatalf("get coordinator: %v", err)
	}
	hash1 := updatedCoord.Annotations["kubemoot.ai/skill-pool-hash"]
	if hash1 == "" || hash1 == "0" {
		t.Errorf("expected non-empty pool-hash after first skill, got %q", hash1)
	}

	// Add a second skill and reconcile again.
	skill2 := mkSkill(testSkillNetDiag, testCrewName, 200, "Network diagnostics", "WHEN asked about network THEN check ping")
	if err := cli.Create(ctx, skill2); err != nil {
		t.Fatalf("create second skill: %v", err)
	}
	// First reconcile of skill2 adds its finalizer.
	_, err = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testSkillNetDiag, Namespace: testCrewNamespace},
	})
	if err != nil {
		t.Fatalf("Reconcile skill2 (finalizer add): %v", err)
	}
	// Second reconcile of skill2 runs sync + enqueue with updated pool.
	_, err = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testSkillNetDiag, Namespace: testCrewNamespace},
	})
	if err != nil {
		t.Fatalf("Reconcile skill2 (sync): %v", err)
	}

	updatedCoord2 := &kubemootv1alpha1.Agent{}
	if err := cli.Get(ctx, types.NamespacedName{Name: testHomelabCoordinator, Namespace: testCrewNamespace}, updatedCoord2); err != nil {
		t.Fatalf("get coordinator after second skill: %v", err)
	}
	hash2 := updatedCoord2.Annotations["kubemoot.ai/skill-pool-hash"]
	if hash2 == hash1 {
		t.Errorf("pool-hash should change when a second skill is added, but got same value %q", hash2)
	}
}
