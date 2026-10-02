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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// addFinalizer adds a finalizer with a merge patch that carries only
// metadata.finalizers. Controllers never write a user's spec: a full Update
// re-sends every spec field and takes field ownership from the chart or kmctl
// that applied the object, so their next server-side apply conflicts. The
// optimistic lock keeps a concurrent finalizer change from being overwritten.
func addFinalizer(ctx context.Context, c client.Client, obj client.Object, finalizer string) error {
	return patchFinalizers(ctx, c, obj, finalizer, controllerutil.AddFinalizer)
}

// removeFinalizer removes a finalizer the same way addFinalizer adds one.
func removeFinalizer(ctx context.Context, c client.Client, obj client.Object, finalizer string) error {
	return patchFinalizers(ctx, c, obj, finalizer, controllerutil.RemoveFinalizer)
}

func patchFinalizers(ctx context.Context, c client.Client, obj client.Object, finalizer string,
	edit func(client.Object, string) bool) error {
	base, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("finalizer patch: %T does not deep-copy to a client.Object", obj)
	}
	if !edit(obj, finalizer) {
		return nil
	}
	return c.Patch(ctx, obj, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}
