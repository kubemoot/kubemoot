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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// reconcileConfig runs one reconcile of the named KubemootConfig with the given release
// and returns the stored object; seed nil means no object exists.
// testStampedRelease is the release a test chart stamps.
const testStampedRelease = "0.0.0-test"

func reconcileConfig(t *testing.T, seed *kubemootv1alpha1.KubemootConfig, name, release string) (*kubemootv1alpha1.KubemootConfig, error) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kubemootv1alpha1.KubemootConfig{})
	if seed != nil {
		b = b.WithObjects(seed)
	}
	c := b.Build()
	r := &KubemootConfigReconciler{Client: c, Scheme: scheme, ConfigCache: NewConfigCache(), Release: release}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	got := &kubemootv1alpha1.KubemootConfig{}
	if getErr := c.Get(context.Background(), types.NamespacedName{Name: name}, got); getErr != nil {
		return nil, err
	}
	return got, err
}

func defaultConfig(status kubemootv1alpha1.KubemootConfigStatus) *kubemootv1alpha1.KubemootConfig {
	return &kubemootv1alpha1.KubemootConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultKubemootConfigName},
		Status:     status,
	}
}

func TestReconcileWritesReleaseToStatus(t *testing.T) {
	got, err := reconcileConfig(t, defaultConfig(kubemootv1alpha1.KubemootConfigStatus{}), DefaultKubemootConfigName, testStampedRelease)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.Status.Release != testStampedRelease {
		t.Errorf("status.release = %q, want the release the chart stamped", got.Status.Release)
	}
	if !got.Status.Ready {
		t.Error("status.ready = false, want true")
	}
}

func TestReconcileReplacesAnOlderRelease(t *testing.T) {
	seed := defaultConfig(kubemootv1alpha1.KubemootConfigStatus{Release: "older-release"})
	got, err := reconcileConfig(t, seed, DefaultKubemootConfigName, "newer-release")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.Status.Release != "newer-release" {
		t.Errorf("status.release = %q, want newer-release", got.Status.Release)
	}
}

func TestReconcileWithoutReleaseLeavesStatusReleaseEmpty(t *testing.T) {
	got, err := reconcileConfig(t, defaultConfig(kubemootv1alpha1.KubemootConfigStatus{}), DefaultKubemootConfigName, "")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.Status.Release != "" {
		t.Errorf("status.release = %q, want empty when the operator knows no release", got.Status.Release)
	}
	if !got.Status.Ready {
		t.Error("status.ready = false, want true: the release is optional")
	}
}

func TestReconcileIgnoresOtherConfigs(t *testing.T) {
	seed := &kubemootv1alpha1.KubemootConfig{ObjectMeta: metav1.ObjectMeta{Name: "not-default"}}
	got, err := reconcileConfig(t, seed, "not-default", testStampedRelease)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got.Status.Release != "" || got.Status.Ready {
		t.Errorf("status = %+v, want untouched for a config not named default", got.Status)
	}
}

func TestReconcileMissingConfigIsNotAnError(t *testing.T) {
	if _, err := reconcileConfig(t, nil, DefaultKubemootConfigName, testStampedRelease); err != nil {
		t.Fatalf("reconcile of a deleted config: %v", err)
	}
}

// statusPatchCounter returns a fake client holding seed and a counter of the status
// patches written through it.
func statusPatchCounter(t *testing.T, seed *kubemootv1alpha1.KubemootConfig) (client.Client, *int) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	writes := 0
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kubemootv1alpha1.KubemootConfig{}).WithObjects(seed).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
				writes++
				return c.SubResource(sub).Patch(ctx, obj, p, opts...)
			},
		}).Build()
	return c, &writes
}

// A pass over an unchanged object writes nothing, so the status write of the first pass
// cannot start a second one, and LastUpdated stays at the time of the real change.
func TestReconcileUnchangedConfigMakesNoStatusWrite(t *testing.T) {
	c, writes := statusPatchCounter(t, defaultConfig(kubemootv1alpha1.KubemootConfigStatus{}))
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &KubemootConfigReconciler{Client: c, Scheme: scheme, ConfigCache: NewConfigCache(), Release: testStampedRelease}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: DefaultKubemootConfigName}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	first := &kubemootv1alpha1.KubemootConfig{}
	if err := c.Get(context.Background(), req.NamespacedName, first); err != nil {
		t.Fatal(err)
	}
	if *writes != 1 {
		t.Fatalf("first pass wrote the status %d times, want 1", *writes)
	}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if *writes != 1 {
		t.Errorf("a pass over an unchanged object wrote the status again (%d writes)", *writes)
	}
	second := &kubemootv1alpha1.KubemootConfig{}
	if err := c.Get(context.Background(), req.NamespacedName, second); err != nil {
		t.Fatal(err)
	}
	if !second.Status.LastUpdated.Equal(first.Status.LastUpdated) {
		t.Errorf("lastUpdated moved from %v to %v with nothing changed", first.Status.LastUpdated, second.Status.LastUpdated)
	}
}

// A new release is a change: the status is written again.
func TestReconcileNewReleaseWritesStatusAgain(t *testing.T) {
	c, writes := statusPatchCounter(t, defaultConfig(kubemootv1alpha1.KubemootConfigStatus{}))
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &KubemootConfigReconciler{Client: c, Scheme: scheme, ConfigCache: NewConfigCache(), Release: testStampedRelease}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: DefaultKubemootConfigName}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	r.Release = "0.0.0-test-next"
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if *writes != 2 {
		t.Errorf("status writes = %d, want 2", *writes)
	}
	got := &kubemootv1alpha1.KubemootConfig{}
	if err := c.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Release != "0.0.0-test-next" {
		t.Errorf("status.release = %q", got.Status.Release)
	}
}
