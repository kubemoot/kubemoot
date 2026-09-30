/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package controller

import (
	"context"
	"fmt"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// MootArchetypeReconciler validates MootArchetype CRs (state machine
// transitions reference declared phases, etc.) and sets .status.valid.
type MootArchetypeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=mootarchetypes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mootarchetypes/status,verbs=get;update;patch

func (r *MootArchetypeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	arch := &kubemootv1alpha1.MootArchetype{}
	if err := r.Get(ctx, req.NamespacedName, arch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	declared := map[string]bool{}
	for _, p := range arch.Spec.Phases {
		declared[p.Name] = true
	}

	if msg, ok := validateStateMachine(arch.Spec.StateMachine, declared); !ok {
		arch.Status.Valid = false
		arch.Status.Message = msg
		return ctrl.Result{}, r.Status().Update(ctx, arch)
	}

	arch.Status.Valid = true
	arch.Status.Message = fmt.Sprintf("archetype %q valid with %d phases", arch.Name, len(arch.Spec.Phases))
	if err := r.Status().Update(ctx, arch); err != nil {
		log.Error(err, "Failed to update MootArchetype status")
	}
	return ctrl.Result{}, nil
}

// validateStateMachine checks that the state machine's initial phase and every
// transition endpoint are declared phases. It returns a validation message and
// false on the first violation, or ("", true) when the state machine is absent
// or fully valid.
func validateStateMachine(sm *kubemootv1alpha1.ArchetypeStateMachine, declared map[string]bool) (string, bool) {
	if sm == nil {
		return "", true
	}
	if !declared[sm.Initial] {
		return fmt.Sprintf("stateMachine.initial %q is not declared as a phase", sm.Initial), false
	}
	for _, t := range sm.Transitions {
		if !declared[t.From] {
			return fmt.Sprintf("transition.from %q not declared", t.From), false
		}
		if !declared[t.To] {
			return fmt.Sprintf("transition.to %q not declared", t.To), false
		}
	}
	return "", true
}

func (r *MootArchetypeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Scheme = mgr.GetScheme()
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.MootArchetype{}).
		Complete(r)
}
