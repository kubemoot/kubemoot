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
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;create;update;patch;delete

// NetPolicySync applies the network policy objects a reconciler builds for one owner and
// removes the ones the owner no longer wants. It only ever touches objects that carry the
// generated-by label and are controlled by the owner, so hand-written policies stay as
// they are. A nil NetPolicySync does nothing.
type NetPolicySync struct {
	Client client.Client
	Scheme *runtime.Scheme
	Config netpol.Config
	// CiliumInstalled reports whether the CiliumNetworkPolicy kind exists in the cluster.
	CiliumInstalled func() bool
}

// Resolved is the config with the Cilium decision made: on and off are taken as set, and
// auto follows whether the kind is installed.
func (s *NetPolicySync) Resolved() netpol.Config {
	c := s.Config
	switch c.Cilium {
	case netpol.CiliumOn:
		return c.WithCilium(true)
	case netpol.CiliumOff:
		return c.WithCilium(false)
	}
	return c.WithCilium(s.ciliumKindPresent())
}

func (s *NetPolicySync) ciliumKindPresent() bool {
	return s.CiliumInstalled != nil && s.CiliumInstalled()
}

// Apply makes the generated objects of owner in namespace equal to desired: it creates or
// updates each desired object and deletes the owner's other generated objects.
func (s *NetPolicySync) Apply(ctx context.Context, owner client.Object, namespace string, desired []client.Object) error {
	if s == nil {
		return nil
	}
	keep := map[string]bool{}
	for _, obj := range desired {
		if err := s.upsert(ctx, owner, obj); err != nil {
			return err
		}
		keep[objectKey(obj)] = true
	}
	return s.prune(ctx, owner, namespace, keep)
}

func objectKey(obj client.Object) string {
	return fmt.Sprintf("%T/%s", obj, obj.GetName())
}

func (s *NetPolicySync) upsert(ctx context.Context, owner, desired client.Object) error {
	if err := controllerutil.SetControllerReference(owner, desired, s.Scheme); err != nil {
		return err
	}
	existing, ok := desired.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("network policy object %T is not a client.Object", desired)
	}
	err := s.Client.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if errors.IsNotFound(err) {
		return s.Client.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if existing.GetLabels()[netpol.LabelGeneratedBy] != netpol.GeneratedByValue || !metav1.IsControlledBy(existing, owner) {
		return fmt.Errorf("%s/%s exists and was not generated for %s; not overwriting it",
			desired.GetNamespace(), desired.GetName(), owner.GetName())
	}
	if sameGenerated(existing, desired) {
		return nil
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	return s.Client.Update(ctx, desired)
}

// sameGenerated reports whether two generated objects carry the same spec, labels, and
// owners.
func sameGenerated(a, b client.Object) bool {
	return equality.Semantic.DeepEqual(specOf(a), specOf(b)) &&
		equality.Semantic.DeepEqual(a.GetLabels(), b.GetLabels()) &&
		equality.Semantic.DeepEqual(a.GetOwnerReferences(), b.GetOwnerReferences())
}

func specOf(obj client.Object) any {
	switch o := obj.(type) {
	case *networkingv1.NetworkPolicy:
		return o.Spec
	case *unstructured.Unstructured:
		return o.Object["spec"]
	}
	return nil
}

// prune deletes the owner's generated objects in the namespace that are not in keep.
func (s *NetPolicySync) prune(ctx context.Context, owner client.Object, namespace string, keep map[string]bool) error {
	selector := client.MatchingLabels{netpol.LabelGeneratedBy: netpol.GeneratedByValue}
	policies := &networkingv1.NetworkPolicyList{}
	if err := s.Client.List(ctx, policies, client.InNamespace(namespace), selector); err != nil {
		return err
	}
	for i := range policies.Items {
		if err := s.deleteIfStale(ctx, owner, &policies.Items[i], keep); err != nil {
			return err
		}
	}
	if !s.ciliumKindPresent() {
		return nil
	}
	cilium := &unstructured.UnstructuredList{}
	cilium.SetGroupVersionKind(netpol.CiliumPolicyGVK.GroupVersion().WithKind(netpol.CiliumPolicyGVK.Kind + "List"))
	if err := s.Client.List(ctx, cilium, client.InNamespace(namespace), selector); err != nil {
		return client.IgnoreNotFound(err)
	}
	for i := range cilium.Items {
		if err := s.deleteIfStale(ctx, owner, &cilium.Items[i], keep); err != nil {
			return err
		}
	}
	return nil
}

func (s *NetPolicySync) deleteIfStale(ctx context.Context, owner, obj client.Object, keep map[string]bool) error {
	if !metav1.IsControlledBy(obj, owner) || keep[objectKey(obj)] {
		return nil
	}
	return client.IgnoreNotFound(s.Client.Delete(ctx, obj))
}

// CiliumKindInstalled returns a function that reports whether the mapper knows the
// CiliumNetworkPolicy kind.
func CiliumKindInstalled(mapper meta.RESTMapper) func() bool {
	return func() bool {
		gvk := netpol.CiliumPolicyGVK
		_, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		return err == nil
	}
}

// setEgressCondition records the EgressEnforced condition of a workload, and removes it
// when the workload has no egress rules to enforce.
func setEgressCondition(conditions *[]metav1.Condition, generation int64, st netpol.EgressStatus) {
	if !st.Applicable {
		meta.RemoveStatusCondition(conditions, netpol.ConditionEgressEnforced)
		return
	}
	status := metav1.ConditionTrue
	if !st.Enforced {
		status = metav1.ConditionFalse
	}
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               netpol.ConditionEgressEnforced,
		Status:             status,
		Reason:             st.Reason,
		Message:            st.Message,
		ObservedGeneration: generation,
	})
}
