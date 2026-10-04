/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestValidateStateMachine(t *testing.T) {
	declared := map[string]bool{"a": true, "b": true}
	if _, ok := validateStateMachine(nil, declared); !ok {
		t.Error("a nil state machine is valid (sequential progression)")
	}
	if _, ok := validateStateMachine(&kubemootv1alpha1.ArchetypeStateMachine{Initial: "x"}, declared); ok {
		t.Error("an undeclared initial phase must be invalid")
	}
	if _, ok := validateStateMachine(&kubemootv1alpha1.ArchetypeStateMachine{
		Initial: "a", Transitions: []kubemootv1alpha1.PhaseTransition{{From: "x", To: "b"}},
	}, declared); ok {
		t.Error("an undeclared transition.from must be invalid")
	}
	if _, ok := validateStateMachine(&kubemootv1alpha1.ArchetypeStateMachine{
		Initial: "a", Transitions: []kubemootv1alpha1.PhaseTransition{{From: "a", To: "x"}},
	}, declared); ok {
		t.Error("an undeclared transition.to must be invalid")
	}
	if _, ok := validateStateMachine(&kubemootv1alpha1.ArchetypeStateMachine{
		Initial: "a", Transitions: []kubemootv1alpha1.PhaseTransition{{From: "a", To: "b"}},
	}, declared); !ok {
		t.Error("a fully-declared state machine is valid")
	}
}

func reconcileArch(t *testing.T, arch *kubemootv1alpha1.MootArchetype) *kubemootv1alpha1.MootArchetype {
	t.Helper()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(arch).WithStatusSubresource(arch).Build()
	r := &MootArchetypeReconciler{Client: cli, Scheme: scheme}
	name := types.NamespacedName{Name: arch.Name, Namespace: arch.Namespace}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: name}); err != nil {
		t.Fatalf("reconcile %s: %v", arch.Name, err)
	}
	got := &kubemootv1alpha1.MootArchetype{}
	if err := cli.Get(context.Background(), name, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMootArchetypeReconcile_ValidAndInvalid(t *testing.T) {
	valid := reconcileArch(t, &kubemootv1alpha1.MootArchetype{
		ObjectMeta: metav1.ObjectMeta{Name: "ar-ok", Namespace: testNS1},
		Spec: kubemootv1alpha1.MootArchetypeSpec{
			Phases: []kubemootv1alpha1.ArchetypePhase{{Name: "a"}, {Name: "b"}},
			StateMachine: &kubemootv1alpha1.ArchetypeStateMachine{
				Initial:     "a",
				Transitions: []kubemootv1alpha1.PhaseTransition{{From: "a", To: "b"}},
			},
		},
	})
	if !valid.Status.Valid {
		t.Errorf("a well-formed archetype should be Valid, msg=%q", valid.Status.Message)
	}

	invalid := reconcileArch(t, &kubemootv1alpha1.MootArchetype{
		ObjectMeta: metav1.ObjectMeta{Name: "ar-bad", Namespace: testNS1},
		Spec: kubemootv1alpha1.MootArchetypeSpec{
			Phases:       []kubemootv1alpha1.ArchetypePhase{{Name: "a"}},
			StateMachine: &kubemootv1alpha1.ArchetypeStateMachine{Initial: "missing"},
		},
	})
	if invalid.Status.Valid {
		t.Error("an archetype whose initial phase is undeclared must be Invalid")
	}
}

func TestMootArchetypeReconcile_NotFoundIsNoOp(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &MootArchetypeReconciler{Client: cli, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testAbsent, Namespace: testNS1},
	}); err != nil {
		t.Errorf("absent archetype reconcile must be a no-op, got %v", err)
	}
}
