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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	artifactAccessV1 = "ghcr.io/kubemoot/artifact-access:1.0.0"
	artifactAccessV2 = "ghcr.io/kubemoot/artifact-access:1.1.0"
	readOpsName      = "readops"
	specImage        = "registry/mcp:1"
	unknownImage     = "nope"
)

func configWithArtifactAccess(image string) *kubemootv1alpha1.KubemootConfig {
	return &kubemootv1alpha1.KubemootConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultKubemootConfigName},
		Spec: kubemootv1alpha1.KubemootConfigSpec{
			Images: kubemootv1alpha1.ImageConfig{ArtifactAccess: image},
		},
	}
}

func readOpsServer() *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: readOpsName, Namespace: testNS1},
		Spec: kubemootv1alpha1.MCPServerSpec{
			KubemootImage: kubemootv1alpha1.KubemootImageArtifactAccess,
			Transport:     kubemootv1alpha1.TransportHTTP,
		},
	}
}

// newReadOpsReconciler builds a reconciler over a fake client holding the read-ops
// server and, when cfg is not nil, the KubemootConfig. The ConfigCache is left empty
// on purpose: the image must come from the client, not the cache.
func newReadOpsReconciler(t *testing.T, cfg *kubemootv1alpha1.KubemootConfig) (*MCPServerReconciler, client.Client, ctrl.Request) {
	t.Helper()
	scheme := gatewayScheme(t)
	ms := readOpsServer()
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ms).WithStatusSubresource(ms)
	if cfg != nil {
		b = b.WithObjects(cfg)
	}
	cli := b.Build()
	r := &MCPServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	return r, cli, ctrl.Request{NamespacedName: types.NamespacedName{Name: readOpsName, Namespace: testNS1}}
}

// reconcileTwice runs the finalizer pass and then the real pass.
func reconcileTwice(t *testing.T, r *MCPServerReconciler, req ctrl.Request) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i+1, err)
		}
	}
}

func deploymentImage(t *testing.T, cli client.Client) string {
	t.Helper()
	dep := &appsv1.Deployment{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: readOpsName, Namespace: testNS1}, dep); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	return dep.Spec.Template.Spec.Containers[0].Image
}

func TestResolveMCPServerImage(t *testing.T) {
	configured := configWithArtifactAccess(artifactAccessV1)
	unstamped := configWithArtifactAccess("")

	tests := []struct {
		name       string
		cfg        *kubemootv1alpha1.KubemootConfig
		spec       kubemootv1alpha1.MCPServerSpec
		wantImage  string
		wantReason string
	}{
		{"spec.image wins as written", nil, kubemootv1alpha1.MCPServerSpec{Image: specImage}, specImage, reasonImageFromSpec},
		{"kubemootImage resolves to the KubemootConfig image", configured, kubemootv1alpha1.MCPServerSpec{KubemootImage: kubemootv1alpha1.KubemootImageArtifactAccess}, artifactAccessV1, reasonImageFromKubemootConfig},
		{"no KubemootConfig", nil, kubemootv1alpha1.MCPServerSpec{KubemootImage: kubemootv1alpha1.KubemootImageArtifactAccess}, "", reasonKubemootConfigMissing},
		{"KubemootConfig without the image", unstamped, kubemootv1alpha1.MCPServerSpec{KubemootImage: kubemootv1alpha1.KubemootImageArtifactAccess}, "", reasonKubemootImageUnset},
		{"unknown kubemootImage name", configured, kubemootv1alpha1.MCPServerSpec{KubemootImage: unknownImage}, "", reasonKubemootImageUnset},
		{"nothing selected", configured, kubemootv1alpha1.MCPServerSpec{}, "", reasonImageNotSpecified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(gatewayScheme(t))
			if tt.cfg != nil {
				b = b.WithObjects(tt.cfg)
			}
			r := &MCPServerReconciler{Client: b.Build()}
			got, err := r.resolveMCPServerImage(context.Background(), &kubemootv1alpha1.MCPServer{Spec: tt.spec})
			if err != nil {
				t.Fatal(err)
			}
			if got.Image != tt.wantImage || got.Reason != tt.wantReason {
				t.Errorf("got image %q reason %q, want image %q reason %q", got.Image, got.Reason, tt.wantImage, tt.wantReason)
			}
			if got.ok() != (tt.wantImage != "") {
				t.Errorf("ok() = %v for image %q", got.ok(), got.Image)
			}
		})
	}
}

func TestKubemootImageFor_NeverFallsBack(t *testing.T) {
	if got := kubemootImageFor(configWithArtifactAccess(""), kubemootv1alpha1.KubemootImageArtifactAccess); got != "" {
		t.Errorf("an unstamped config must give no image, got %q", got)
	}
	if got := kubemootImageFor(configWithArtifactAccess(artifactAccessV1), kubemootv1alpha1.KubemootImageArtifactAccess); got != artifactAccessV1 {
		t.Errorf("got %q, want %q", got, artifactAccessV1)
	}
	if got := kubemootImageFor(configWithArtifactAccess(artifactAccessV1), unknownImage); got != "" {
		t.Errorf("an unknown name must give no image, got %q", got)
	}
}

func TestWithResolvedImage_LeavesTheOriginalAlone(t *testing.T) {
	orig := readOpsServer()
	resolved := withResolvedImage(orig, artifactAccessV1)
	if resolved.Spec.Image != artifactAccessV1 {
		t.Errorf("copy image = %q", resolved.Spec.Image)
	}
	if orig.Spec.Image != "" {
		t.Errorf("the user's object must keep an empty spec.image, got %q", orig.Spec.Image)
	}
}

// A read-ops MCPServer that names no image runs the KubemootConfig image.
func TestMCPServerReconcile_KubemootImageUsesKubemootConfig(t *testing.T) {
	r, cli, req := newReadOpsReconciler(t, configWithArtifactAccess(artifactAccessV1))

	reconcileTwice(t, r, req)

	if got := deploymentImage(t, cli); got != artifactAccessV1 {
		t.Errorf("Deployment image = %q, want %q", got, artifactAccessV1)
	}
	got := &kubemootv1alpha1.MCPServer{}
	if err := cli.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Image != "" {
		t.Errorf("the controller must not write the resolved image into spec, got %q", got.Spec.Image)
	}
	if got.Status.Image != artifactAccessV1 {
		t.Errorf("status.image = %q, want %q", got.Status.Image, artifactAccessV1)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionTypeImageResolved)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonImageFromKubemootConfig {
		t.Errorf("expected ImageResolved=True/%s, got %+v", reasonImageFromKubemootConfig, cond)
	}
}

// With no usable KubemootConfig image the server reports a clear condition, creates
// no Deployment, and does not fail the reconcile.
func TestMCPServerReconcile_KubemootImageUnavailableReportsCondition(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *kubemootv1alpha1.KubemootConfig
		wantReason string
		wantText   string
	}{
		{"no KubemootConfig", nil, reasonKubemootConfigMissing, "KubemootConfig"},
		{"image not stamped", configWithArtifactAccess(""), reasonKubemootImageUnset, "spec.images.artifactAccess"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, cli, req := newReadOpsReconciler(t, tt.cfg)
			reconcileTwice(t, r, req)

			assertImageUnresolved(t, cli, req, tt.wantReason, tt.wantText)
		})
	}
}

func assertImageUnresolved(t *testing.T, cli client.Client, req ctrl.Request, wantReason, wantText string) {
	t.Helper()
	deps := &appsv1.DeploymentList{}
	if err := cli.List(context.Background(), deps, client.InNamespace(testNS1)); err != nil || len(deps.Items) != 0 {
		t.Fatalf("no Deployment may be created without an image, got %d (err %v)", len(deps.Items), err)
	}
	got := &kubemootv1alpha1.MCPServer{}
	if err := cli.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Ready || got.Status.Phase != phaseError {
		t.Errorf("expected phase Error and not Ready, got %q ready=%v", got.Status.Phase, got.Status.Ready)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionTypeImageResolved)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != wantReason {
		t.Fatalf("expected ImageResolved=False/%s, got %+v", wantReason, cond)
	}
	if !strings.Contains(cond.Message, wantText) {
		t.Errorf("message %q should mention %q", cond.Message, wantText)
	}
}

// When the KubemootConfig image changes, the next reconcile (which the KubemootConfig
// watch triggers) rolls the Deployment to the new image, and a server that was waiting
// for the image recovers. The ConfigCache stays empty throughout: the image is read
// from the client, so a reconcile raised by the change cannot see an older image.
func TestMCPServerReconcile_RerollsOnKubemootConfigChange(t *testing.T) {
	ctx := context.Background()
	cfg := configWithArtifactAccess("")
	r, cli, req := newReadOpsReconciler(t, cfg)
	reconcileTwice(t, r, req)

	setArtifactAccess := func(image string) {
		t.Helper()
		got := &kubemootv1alpha1.KubemootConfig{}
		if err := cli.Get(ctx, client.ObjectKey{Name: DefaultKubemootConfigName}, got); err != nil {
			t.Fatal(err)
		}
		got.Spec.Images.ArtifactAccess = image
		if err := cli.Update(ctx, got); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("reconcile after the image changed: %v", err)
		}
	}

	setArtifactAccess(artifactAccessV1)
	if got := deploymentImage(t, cli); got != artifactAccessV1 {
		t.Fatalf("Deployment image = %q, want %q after the image was stamped", got, artifactAccessV1)
	}
	setArtifactAccess(artifactAccessV2)
	if got := deploymentImage(t, cli); got != artifactAccessV2 {
		t.Errorf("Deployment image = %q, want %q after the KubemootConfig image changed", got, artifactAccessV2)
	}
}

// A server that cannot resolve its image reports no image in status.
func TestMCPServerReconcile_UnresolvedClearsStatusImage(t *testing.T) {
	ctx := context.Background()
	r, cli, req := newReadOpsReconciler(t, configWithArtifactAccess(artifactAccessV1))
	reconcileTwice(t, r, req)

	cfg := &kubemootv1alpha1.KubemootConfig{}
	if err := cli.Get(ctx, client.ObjectKey{Name: DefaultKubemootConfigName}, cfg); err != nil {
		t.Fatal(err)
	}
	if err := cli.Delete(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	got := &kubemootv1alpha1.MCPServer{}
	if err := cli.Get(ctx, req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Image != "" || got.Status.Phase != phaseError {
		t.Errorf("expected phase Error and empty status.image, got %q %q", got.Status.Phase, got.Status.Image)
	}
}

// A KubemootConfig change enqueues every MCPServer, so the re-roll above is triggered.
func TestMapKubemootConfigToRequests_EnqueuesMCPServers(t *testing.T) {
	scheme := gatewayScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(readOpsServer()).Build()
	reqs := mapKubemootConfigToRequests(context.Background(), cli,
		func() client.ObjectList { return &kubemootv1alpha1.MCPServerList{} },
		"mcpserver", configWithArtifactAccess(artifactAccessV2))
	if len(reqs) != 1 || reqs[0].Name != readOpsName || reqs[0].Namespace != testNS1 {
		t.Errorf("expected the read-ops server enqueued, got %v", reqs)
	}
}
