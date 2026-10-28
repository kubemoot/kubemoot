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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	mrcServer = "srv-rc"
	mrcImage  = "registry/mcp:1"
	mrcOwn    = "own-server"
)

func mrcMCPServer(class *string) *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: mrcServer, Namespace: testNS1},
		Spec: kubemootv1alpha1.MCPServerSpec{
			Image:            mrcImage,
			Transport:        kubemootv1alpha1.TransportHTTP,
			RuntimeClassName: class,
		},
	}
}

// The pod spec carries the class only when the MCPServer asks for one: unset and empty both
// leave the field nil so rendered pods are unchanged.
func TestMCPServerPodRuntimeClass(t *testing.T) {
	cases := []struct {
		name  string
		class *string
		want  *string
	}{
		{"unset keeps today's pod", nil, nil},
		{"empty keeps today's pod", ptr.To(""), nil},
		{"set lands on the pod", ptr.To(rcGvisor), ptr.To(rcGvisor)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
			assertRuntimeClass(t, "mcp pod", r.buildDeployment(mrcMCPServer(tc.class)), tc.want)
		})
	}
}

// Changing the class changes the Deployment hash so the pods roll.
func TestMCPServerRuntimeClassChangesTheHash(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	plain := r.buildDeployment(mrcMCPServer(nil))
	sandboxed := r.buildDeployment(mrcMCPServer(ptr.To(rcGvisor)))
	if computeDeploymentHash(plain) == computeDeploymentHash(sandboxed) {
		t.Error("a runtime class change must change the Deployment hash")
	}
}

func mrcReconcileTwice(t *testing.T, r *MCPServerReconciler) {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: mrcServer, Namespace: testNS1}}
	for i := 0; i < 2; i++ { // the first pass adds the finalizer
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i+1, err)
		}
	}
}

func mrcFakeReconciler(t *testing.T, ms *kubemootv1alpha1.MCPServer, objs ...client.Object) (*MCPServerReconciler, client.Client) {
	t.Helper()
	s := rcScheme(t)
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(ms).WithObjects(objs...).WithStatusSubresource(ms).Build()
	return &MCPServerReconciler{Client: cli, Scheme: s, ConfigCache: NewConfigCache()}, cli
}

func mrcStored(t *testing.T, cli client.Client) *kubemootv1alpha1.MCPServer {
	t.Helper()
	got := &kubemootv1alpha1.MCPServer{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: mrcServer, Namespace: testNS1}, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func mrcDeployments(t *testing.T, cli client.Client) int {
	t.Helper()
	deps := &appsv1.DeploymentList{}
	if err := cli.List(context.Background(), deps, client.InNamespace(testNS1)); err != nil {
		t.Fatal(err)
	}
	return len(deps.Items)
}

// A missing class holds the Deployment, reports the condition False and the phase Pending.
func TestMCPServerHeldForAMissingRuntimeClass(t *testing.T) {
	r, cli := mrcFakeReconciler(t, mrcMCPServer(ptr.To(rcGvisor)))
	mrcReconcileTwice(t, r)
	if n := mrcDeployments(t, cli); n != 0 {
		t.Fatalf("a held MCPServer must get no Deployment, got %d", n)
	}
	got := mrcStored(t, cli)
	cond := meta.FindStatusCondition(got.Status.Conditions, ConditionRuntimeClassAvailable)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonRuntimeClassMissing {
		t.Fatalf("want RuntimeClassAvailable=False/RuntimeClassMissing, got %v", cond)
	}
	if got.Status.Phase != phasePending || got.Status.Ready {
		t.Errorf("a held MCPServer is Pending and not Ready, got phase %q ready %v", got.Status.Phase, got.Status.Ready)
	}
}

// With the class present the Deployment is built with it and the condition is True.
func TestMCPServerWithTheRuntimeClassPresent(t *testing.T) {
	class := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: rcGvisor}, Handler: rcHandler}
	r, cli := mrcFakeReconciler(t, mrcMCPServer(ptr.To(rcGvisor)), class)
	mrcReconcileTwice(t, r)
	deps := &appsv1.DeploymentList{}
	if err := cli.List(context.Background(), deps, client.InNamespace(testNS1)); err != nil || len(deps.Items) != 1 {
		t.Fatalf("want one Deployment, got %d (err %v)", len(deps.Items), err)
	}
	assertRuntimeClass(t, "mcp pod", &deps.Items[0], ptr.To(rcGvisor))
	cond := meta.FindStatusCondition(mrcStored(t, cli).Status.Conditions, ConditionRuntimeClassAvailable)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("want RuntimeClassAvailable=True, got %v", cond)
	}
}

// Asking for no class adds no condition and holds nothing.
func TestMCPServerWithNoRuntimeClass(t *testing.T) {
	r, cli := mrcFakeReconciler(t, mrcMCPServer(nil))
	mrcReconcileTwice(t, r)
	if n := mrcDeployments(t, cli); n != 1 {
		t.Fatalf("want one Deployment, got %d", n)
	}
	if meta.FindStatusCondition(mrcStored(t, cli).Status.Conditions, ConditionRuntimeClassAvailable) != nil {
		t.Error("asking for no class must add no condition")
	}
}

// A server whose image comes from KubemootConfig keeps that image through the condition write.
func TestMCPServerRuntimeClassKeepsTheResolvedImage(t *testing.T) {
	ms := readOpsServer()
	ms.Spec.RuntimeClassName = ptr.To(rcGvisor)
	class := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: rcGvisor}, Handler: rcHandler}
	s := rcScheme(t)
	cli := fake.NewClientBuilder().WithScheme(s).WithObjects(ms, class, configWithArtifactAccess(artifactAccessV1)).WithStatusSubresource(ms).Build()
	r := &MCPServerReconciler{Client: cli, Scheme: s, ConfigCache: NewConfigCache()}
	reconcileTwice(t, r, ctrl.Request{NamespacedName: types.NamespacedName{Name: readOpsName, Namespace: testNS1}})
	if got := deploymentImage(t, cli); got != artifactAccessV1 {
		t.Errorf("Deployment image = %q, want %q", got, artifactAccessV1)
	}
}

// Clearing runtimeClassName on a held server removes the condition and releases the hold.
func TestMCPServerClearingTheClassReleasesTheHold(t *testing.T) {
	r, cli := mrcFakeReconciler(t, mrcMCPServer(ptr.To(rcGvisor)))
	mrcReconcileTwice(t, r)
	if n := mrcDeployments(t, cli); n != 0 {
		t.Fatalf("setup: want the Deployment held, got %d", n)
	}
	ms := mrcStored(t, cli)
	ms.Spec.RuntimeClassName = nil
	if err := cli.Update(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	mrcReconcileTwice(t, r)
	if n := mrcDeployments(t, cli); n != 1 {
		t.Errorf("clearing the class must release the hold, got %d Deployments", n)
	}
	got := mrcStored(t, cli)
	cond := meta.FindStatusCondition(got.Status.Conditions, ConditionRuntimeClassAvailable)
	if cond != nil || got.Status.Phase == phasePending {
		t.Errorf("want no condition and no Pending phase, got phase %q cond %v", got.Status.Phase, cond)
	}
}

// Switching a held managed server to an external endpoint drops the condition, since an
// external server has no pod.
func TestMCPServerSwitchToExternalRemovesTheCondition(t *testing.T) {
	r, cli := mrcFakeReconciler(t, mrcMCPServer(ptr.To(rcGvisor)))
	mrcReconcileTwice(t, r)
	ms := mrcStored(t, cli)
	ms.Spec.Image = ""
	ms.Spec.ExternalEndpoint = "http://remote-mcp:9100"
	if err := cli.Update(context.Background(), ms); err != nil {
		t.Fatal(err)
	}
	mrcReconcileTwice(t, r)
	got := mrcStored(t, cli)
	if meta.FindStatusCondition(got.Status.Conditions, ConditionRuntimeClassAvailable) != nil {
		t.Error("an external server must not carry RuntimeClassAvailable")
	}
	if got.Status.Phase != phaseReady {
		t.Errorf("want phase %q, got %q", phaseReady, got.Status.Phase)
	}
}

// A RuntimeClass event enqueues exactly the MCP servers whose pods ask for that class.
func TestMCPServerRuntimeClassWatchMapping(t *testing.T) {
	own := mrcMCPServer(ptr.To(rcGvisor))
	own.Name = mrcOwn
	other := mrcMCPServer(ptr.To(rcKata))
	other.Name = testMismatchName
	none := mrcMCPServer(nil)
	none.Name = coreTestNone
	r := &MCPServerReconciler{Client: fake.NewClientBuilder().WithScheme(rcScheme(t)).WithObjects(own, other, none).Build()}
	reqs := r.runtimeClassRequests(context.Background(), &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: rcGvisor}})
	if len(reqs) != 1 || reqs[0].Name != mrcOwn {
		t.Errorf("want only own enqueued, got %v", reqs)
	}
	if r.runtimeClassRequests(context.Background(), &corev1.Pod{}) != nil {
		t.Error("a non-RuntimeClass object must enqueue nothing")
	}
}

// Under a real manager: a missing class holds the Deployment, creating the class makes it
// appear with the class on its pods, and deleting the class leaves the running Deployment.
var _ = Describe("MCPServer controller and a missing RuntimeClass", func() {
	It("holds the Deployment until the class exists", func() {
		ctx, stop := context.WithCancel(context.Background())
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "mrc-missing-"}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		ns := namespace.Name

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}},
			Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		r := &MCPServerReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ConfigCache: NewConfigCache()}
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			Expect(mgr.Start(ctx)).To(Succeed())
		}()
		DeferCleanup(func() {
			stop()
			<-done
		})

		class := "mrc-envtest-" + ns
		server := mrcMCPServer(ptr.To(class))
		server.Namespace = ns
		Expect(k8sClient.Create(ctx, server)).To(Succeed())
		key := types.NamespacedName{Name: mrcServer, Namespace: ns}

		condition := func() *metav1.Condition {
			got := &kubemootv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, key, got); err != nil {
				return nil
			}
			return meta.FindStatusCondition(got.Status.Conditions, ConditionRuntimeClassAvailable)
		}
		conditionStatus := func() string {
			if c := condition(); c != nil {
				return string(c.Status)
			}
			return ""
		}
		// The condition is saved before the closing status write that sets the phase, so each is
		// awaited as its own state.
		Eventually(conditionStatus, "10s").Should(Equal(string(metav1.ConditionFalse)))
		Expect(condition().Message).To(ContainSubstring(class))
		Eventually(func() string {
			got := &kubemootv1alpha1.MCPServer{}
			if err := k8sClient.Get(ctx, key, got); err != nil {
				return ""
			}
			return got.Status.Phase
		}, "10s").Should(Equal(phasePending))
		err = k8sClient.Get(ctx, types.NamespacedName{Name: boundedName(mrcServer), Namespace: ns}, &appsv1.Deployment{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the Deployment is held while the class is missing")

		rc := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: class}, Handler: rcHandler}
		Expect(k8sClient.Create(ctx, rc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), rc) })

		Eventually(conditionStatus, "10s").Should(Equal(string(metav1.ConditionTrue)), "the condition clears when the class appears")
		dep := &appsv1.Deployment{}
		depKey := types.NamespacedName{Name: boundedName(mrcServer), Namespace: ns}
		Eventually(func() error { return k8sClient.Get(ctx, depKey, dep) }, "10s").Should(Succeed())
		Expect(dep.Spec.Template.Spec.RuntimeClassName).To(Equal(ptr.To(class)))

		Expect(k8sClient.Delete(ctx, rc)).To(Succeed())
		Eventually(conditionStatus, "10s").Should(Equal(string(metav1.ConditionFalse)), "a deleted class is reported again")
		Expect(k8sClient.Get(ctx, depKey, &appsv1.Deployment{})).To(Succeed(), "a running Deployment is left as it is")
	})
})
