/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// model_propagation.go wires Model changes to the agents in the same namespace.
// An agent's per-phase model binding and its candidate list are derived from the
// Models it can see; without a watch, adding, removing, or relabeling Models (or a
// new Model finishing its pull) left every agent on its old binding until an
// unrelated change or the informer resync.
package controller

import (
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// modelBindingChanged passes the Model events that can change a binding: creation,
// deletion (including the start of a finalizer-held deletion), and updates to the
// labels the scheduler selects on, the spec, or whether the model is usable. A move
// between Loaded and Available is not one of them: it happens every time a model
// loads or unloads, and reacting to it would roll agent pods mid-discussion.
func modelBindingChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldM, okOld := e.ObjectOld.(*kubemootv1alpha1.Model)
			newM, okNew := e.ObjectNew.(*kubemootv1alpha1.Model)
			if !okOld || !okNew {
				return false
			}
			return !reflect.DeepEqual(oldM.Labels, newM.Labels) ||
				!reflect.DeepEqual(oldM.Spec, newM.Spec) ||
				oldM.Status.Ready != newM.Status.Ready ||
				oldM.DeletionTimestamp.IsZero() != newM.DeletionTimestamp.IsZero()
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}
