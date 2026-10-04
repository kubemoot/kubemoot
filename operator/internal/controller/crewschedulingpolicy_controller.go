/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// CrewSchedulingPolicyReconciler validates CrewSchedulingPolicy CRs against
// the referenced MootArchetype's phase vocabulary and records validation
// state in .status. It does not perform scheduling — that lives in the
// Agent reconciler.
type CrewSchedulingPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const defaultArchetypeName = "consent-3"

// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewschedulingpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewschedulingpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewschedulingpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mootarchetypes,verbs=get;list;watch

func (r *CrewSchedulingPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &kubemootv1alpha1.CrewSchedulingPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	archetypeName := policy.Spec.ArchetypeRef
	if archetypeName == "" {
		archetypeName = defaultArchetypeName
	}

	archetype := &kubemootv1alpha1.MootArchetype{}
	if err := r.Get(ctx, types.NamespacedName{Name: archetypeName}, archetype); err != nil {
		// If the archetype isn't installed yet, leave the policy permissive.
		// We can't validate phase strings without it.
		if apierrors.IsNotFound(err) {
			policy.Status.ValidationError = fmt.Sprintf("referenced MootArchetype %q not found; accepting policy permissively until it exists", archetypeName)
			policy.Status.LastValidated = nowPtr()
			return ctrl.Result{}, r.Status().Update(ctx, policy)
		}
		return ctrl.Result{}, err
	}

	policy.Status.ValidationError = policyPhaseValidationError(policy, archetype, archetypeName)
	policy.Status.LastValidated = nowPtr()
	if err := r.Status().Update(ctx, policy); err != nil {
		log.Error(err, "Failed to update CrewSchedulingPolicy status")
	}
	return ctrl.Result{}, nil
}

func nowPtr() *metav1.Time {
	t := metav1.Now()
	return &t
}

func (r *CrewSchedulingPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Scheme = mgr.GetScheme()
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.CrewSchedulingPolicy{}).
		Complete(r)
}

// policyPhaseValidationError names the policy's rule phases that are not in the
// archetype's phase vocabulary, sorted; it is empty when every phase is known.
func policyPhaseValidationError(policy *kubemootv1alpha1.CrewSchedulingPolicy, archetype *kubemootv1alpha1.MootArchetype, archetypeName string) string {
	allowed := map[string]bool{}
	for _, p := range archetype.Spec.Phases {
		allowed[p.Name] = true
	}

	var bad []string
	for _, rule := range policy.Spec.Rules {
		if !allowed[rule.Phase] {
			bad = append(bad, rule.Phase)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return fmt.Sprintf("phase(s) not in archetype %q vocabulary: %s", archetypeName, strings.Join(bad, ", "))
}
