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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	rcGvisor   = "gvisor"
	rcKata     = "kata"
	rcRealm    = "realm-default"
	rcHandler  = "runsc"
	rcAgent    = "a1"
	rcServer   = "prov1"
	rcPolicyCM = "a1-policy"
)

func rcAgentWith(class *string) *kubemootv1alpha1.Agent {
	a := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: rcAgent, Namespace: testNS1}}
	if class != nil {
		a.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{RuntimeClassName: class}
	}
	return a
}

func rcReconciler(t *testing.T, realmDefault string) *AgentReconciler {
	t.Helper()
	r, _ := harnessReconciler(t)
	r.ConfigCache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{Defaults: kubemootv1alpha1.DefaultConfig{AgentRuntimeClassName: realmDefault}},
	})
	return r
}

// The pod spec carries the resolved class in both pod shapes: nothing set leaves the field
// nil, the realm default applies, a per-Agent value wins, and an explicit empty value turns the
// realm default off.
func TestAgentPodRuntimeClassResolution(t *testing.T) {
	cases := []struct {
		name  string
		realm string
		agent *string
		want  *string
	}{
		{"nothing set keeps today's pod", "", nil, nil},
		{"realm default applies", rcRealm, nil, ptr.To(rcRealm)},
		{"per-agent value with no realm default", "", ptr.To(rcGvisor), ptr.To(rcGvisor)},
		{"per-agent value wins over the realm default", rcRealm, ptr.To(rcKata), ptr.To(rcKata)},
		{"explicit empty value turns the realm default off", rcRealm, ptr.To(""), nil},
		{"explicit empty value with no realm default", "", ptr.To(""), nil},
	}
	mulling, triage := harnessPick(testModelID, "rig0"), harnessPick("qwen3:4b", "rig1")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rcReconciler(t, tc.realm)
			legacy := r.buildDeployment(context.Background(), rcAgentWith(tc.agent), mulling, triage, rcPolicyCM, "h1")
			assertRuntimeClass(t, "legacy pod", legacy, tc.want)

			sc := sidecarAgent()
			if tc.agent != nil {
				sc.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{RuntimeClassName: tc.agent}
			}
			sidecar, err := r.buildSidecarDeployment(context.Background(), sc, mulling, triage, rcPolicyCM, "h1")
			if err != nil {
				t.Fatal(err)
			}
			assertRuntimeClass(t, "sidecar pod", sidecar, tc.want)
		})
	}
}

func assertRuntimeClass(t *testing.T, what string, d *appsv1.Deployment, want *string) {
	t.Helper()
	got := d.Spec.Template.Spec.RuntimeClassName
	if (got == nil) != (want == nil) || (got != nil && *got != *want) {
		t.Errorf("%s runtimeClassName = %v, want %v", what, deref(got), deref(want))
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// With no config cache at all the pod is unchanged, and changing the class changes the
// Deployment hash so the pods roll.
func TestRuntimeClassWithoutConfigAndHashChange(t *testing.T) {
	if got := (&AgentReconciler{}).podRuntimeClass(rcAgentWith(nil)); got != nil {
		t.Errorf("no config and no per-agent value must set no class, got %q", *got)
	}
	r := rcReconciler(t, "")
	mulling, triage := harnessPick(testModelID, "rig0"), harnessPick("qwen3:4b", "rig1")
	plain := r.buildDeployment(context.Background(), rcAgentWith(nil), mulling, triage, rcPolicyCM, "h1")
	sandboxed := r.buildDeployment(context.Background(), rcAgentWith(ptr.To(rcGvisor)), mulling, triage, rcPolicyCM, "h1")
	if plain.Annotations[deploymentHashAnnotation] == sandboxed.Annotations[deploymentHashAnnotation] {
		t.Error("a runtime class change must change the Deployment hash")
	}
}

func rcScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := gatewayScheme(t)
	if err := nodev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// rcSync runs the condition sync for the Agent against a fake cluster that holds the given
// RuntimeClasses, and returns whether the Deployment is held and the Agent as the API now has it.
func rcSync(t *testing.T, agent *kubemootv1alpha1.Agent, realm string, objs ...*nodev1.RuntimeClass) (bool, *kubemootv1alpha1.Agent) {
	t.Helper()
	s := rcScheme(t)
	b := fake.NewClientBuilder().WithScheme(s).WithObjects(agent).WithStatusSubresource(agent)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	cli := b.Build()
	r := &AgentReconciler{Client: cli, Scheme: s, ConfigCache: NewConfigCache()}
	r.ConfigCache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{Defaults: kubemootv1alpha1.DefaultConfig{AgentRuntimeClassName: realm}},
	})
	if err := r.syncRuntimeClassCondition(context.Background(), agent); err != nil {
		t.Fatalf("sync: %v", err)
	}
	stored := &kubemootv1alpha1.Agent{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, stored); err != nil {
		t.Fatal(err)
	}
	_, held := runtimeClassHold(stored)
	return held, stored
}

func TestRuntimeClassConditionWithNoClassAsked(t *testing.T) {
	held, a := rcSync(t, rcAgentWith(nil), "")
	if held || meta.FindStatusCondition(a.Status.Conditions, ConditionRuntimeClassAvailable) != nil {
		t.Error("asking for no class must neither hold nor add a condition")
	}
	held, a = rcSync(t, rcAgentWith(ptr.To("")), rcGvisor)
	if held || meta.FindStatusCondition(a.Status.Conditions, ConditionRuntimeClassAvailable) != nil {
		t.Error("an explicit empty class must opt out of the realm default without a condition")
	}
}

func TestRuntimeClassConditionWithTheClassPresent(t *testing.T) {
	present := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: rcGvisor}, Handler: rcHandler}
	held, a := rcSync(t, rcAgentWith(ptr.To(rcGvisor)), "", present)
	cond := meta.FindStatusCondition(a.Status.Conditions, ConditionRuntimeClassAvailable)
	if held || cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("an existing class must not hold and must set the condition True, held=%v cond=%v", held, cond)
	}
}

func TestRuntimeClassConditionWithTheClassMissing(t *testing.T) {
	held, a := rcSync(t, rcAgentWith(nil), rcGvisor)
	cond := meta.FindStatusCondition(a.Status.Conditions, ConditionRuntimeClassAvailable)
	if !held || cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonRuntimeClassMissing {
		t.Fatalf("a missing realm-default class must hold with the condition False, held=%v cond=%v", held, cond)
	}
	if !strings.Contains(cond.Message, `RuntimeClass "gvisor"`) {
		t.Errorf("the condition message must name the class, got %q", cond.Message)
	}
}

// An Agent held for a missing class that then opts out loses the condition and the hold.
func TestRuntimeClassConditionClearsWhenTheAgentOptsOut(t *testing.T) {
	agent := rcAgentWith(ptr.To(""))
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type: ConditionRuntimeClassAvailable, Status: metav1.ConditionFalse, Reason: reasonRuntimeClassMissing, Message: "old",
	})
	held, a := rcSync(t, agent, rcGvisor)
	if held || meta.FindStatusCondition(a.Status.Conditions, ConditionRuntimeClassAvailable) != nil {
		t.Error("opting out must clear a stale RuntimeClassAvailable=False")
	}
}

// A held Agent reports Pending and not Ready, and ensureDeployment builds nothing.
func TestHeldAgentIsPendingAndGetsNoDeployment(t *testing.T) {
	agent := rcAgentWith(ptr.To(rcGvisor))
	r := rcReconciler(t, "")
	r.Client = fake.NewClientBuilder().WithScheme(rcScheme(t)).WithObjects(agent).WithStatusSubresource(agent).Build()
	ctx := context.Background()
	if err := r.syncRuntimeClassCondition(ctx, agent); err != nil {
		t.Fatal(err)
	}
	mulling, triage := harnessPick(testModelID, "rig0"), harnessPick("qwen3:4b", "rig1")
	if err := r.ensureDeployment(ctx, agent, mulling, triage, rcPolicyCM); err != nil {
		t.Fatal(err)
	}
	deps := &appsv1.DeploymentList{}
	if err := r.List(ctx, deps); err != nil || len(deps.Items) != 0 {
		t.Fatalf("a held agent must get no Deployment, got %d (err %v)", len(deps.Items), err)
	}
	if err := r.refreshStatus(ctx, agent, mulling, triage); err != nil {
		t.Fatal(err)
	}
	if agent.Status.Phase != phasePending || agent.Status.Ready {
		t.Errorf("a held agent is Pending and not Ready, got phase %q ready %v", agent.Status.Phase, agent.Status.Ready)
	}
}

// A RuntimeClass event enqueues exactly the Agents whose pods ask for that class, through the
// per-Agent field or the realm default; only create and delete events pass the predicate.
func TestRuntimeClassWatchMapping(t *testing.T) {
	own := rcAgentWith(ptr.To(rcGvisor))
	own.Name = "own"
	viaRealm := rcAgentWith(nil)
	viaRealm.Name = "realm"
	other := rcAgentWith(ptr.To(rcKata))
	other.Name = testMismatchName
	optedOut := rcAgentWith(ptr.To(""))
	optedOut.Name = "opted-out"
	s := rcScheme(t)
	r := &AgentReconciler{
		Client:      fake.NewClientBuilder().WithScheme(s).WithObjects(own, viaRealm, other, optedOut).Build(),
		ConfigCache: NewConfigCache(),
	}
	r.ConfigCache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{Defaults: kubemootv1alpha1.DefaultConfig{AgentRuntimeClassName: rcGvisor}},
	})
	reqs := r.runtimeClassRequests(context.Background(), &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: rcGvisor}})
	got := map[string]bool{}
	for _, q := range reqs {
		got[q.Name] = true
	}
	if len(got) != 2 || !got["own"] || !got["realm"] {
		t.Errorf("want own and realm enqueued, got %v", got)
	}
	if r.runtimeClassRequests(context.Background(), &corev1.Pod{}) != nil {
		t.Error("a non-RuntimeClass object must enqueue nothing")
	}
	p := runtimeClassAppearsOrGoes()
	rc := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: rcGvisor}}
	if !p.Create(event.CreateEvent{Object: rc}) || !p.Delete(event.DeleteEvent{Object: rc}) {
		t.Error("create and delete of a RuntimeClass must enqueue")
	}
	if p.Update(event.UpdateEvent{ObjectOld: rc, ObjectNew: rc}) {
		t.Error("an update of a RuntimeClass must not enqueue")
	}
}

// The Agent controller runs under a real manager: an Agent that asks for a RuntimeClass that
// does not exist gets RuntimeClassAvailable=False and no Deployment; creating the class makes
// the Deployment appear with the class on its pods and the condition True, and deleting the
// class from under a running agent flips the condition back without touching the Deployment.
var _ = Describe("Agent controller and a missing RuntimeClass", func() {
	It("holds the Deployment until the class exists", func() {
		ctx, stop := context.WithCancel(context.Background())
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "rc-missing-"}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		ns := namespace.Name
		server := &kubemootv1alpha1.ModelServer{
			ObjectMeta: metav1.ObjectMeta{Name: rcServer, Namespace: ns},
			Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeOllama, Endpoint: testProvEndpoint},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())
		server.Status.Ready = true
		Expect(k8sClient.Status().Update(ctx, server)).To(Succeed())
		model := &kubemootv1alpha1.Model{
			ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: ns},
			Spec:       kubemootv1alpha1.ModelSpec{Model: testModelID, ServerRef: rcServer},
		}
		Expect(k8sClient.Create(ctx, model)).To(Succeed())
		model.Status.Ready = true
		Expect(k8sClient.Status().Update(ctx, model)).To(Succeed())

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
			Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}},
			Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		r := &AgentReconciler{Client: mgr.GetClient(), ConfigCache: NewConfigCache()}
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

		class := "rc-envtest-" + ns
		agent := &kubemootv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: rcAgent, Namespace: ns, Labels: map[string]string{labelCrew: testCrewA}},
			Spec: kubemootv1alpha1.AgentSpec{
				Capabilities: []string{candReasoning},
				Deployment:   &kubemootv1alpha1.DeploymentConfig{RuntimeClassName: ptr.To(class)},
			},
		}
		Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		key := types.NamespacedName{Name: rcAgent, Namespace: ns}

		condition := func() *metav1.Condition {
			got := &kubemootv1alpha1.Agent{}
			if err := k8sClient.Get(ctx, key, got); err != nil {
				return nil
			}
			return meta.FindStatusCondition(got.Status.Conditions, ConditionRuntimeClassAvailable)
		}
		Eventually(condition, "10s").ShouldNot(BeNil())
		Expect(condition().Status).To(Equal(metav1.ConditionFalse))
		Expect(condition().Message).To(ContainSubstring(class))
		// The condition is saved first and the phase in the reconcile's closing status write, so the
		// phase is awaited as its own state instead of being read the moment the condition shows.
		Eventually(func() string {
			got := &kubemootv1alpha1.Agent{}
			if err := k8sClient.Get(ctx, key, got); err != nil {
				return ""
			}
			return got.Status.Phase
		}, "10s").Should(Equal(phasePending))
		err = k8sClient.Get(ctx, key, &appsv1.Deployment{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the Deployment is held while the class is missing")

		rc := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: class}, Handler: rcHandler}
		Expect(k8sClient.Create(ctx, rc)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), rc) })

		Eventually(func() string {
			if c := condition(); c != nil {
				return string(c.Status)
			}
			return ""
		}, "10s").Should(Equal(string(metav1.ConditionTrue)), "the condition clears when the class appears")
		dep := &appsv1.Deployment{}
		Eventually(func() error { return k8sClient.Get(ctx, key, dep) }, "10s").Should(Succeed())
		Expect(dep.Spec.Template.Spec.RuntimeClassName).To(Equal(ptr.To(class)))

		Expect(k8sClient.Delete(ctx, rc)).To(Succeed())
		Eventually(func() string {
			if c := condition(); c != nil {
				return string(c.Status)
			}
			return ""
		}, "10s").Should(Equal(string(metav1.ConditionFalse)), "a deleted class is reported again")
		Expect(k8sClient.Get(ctx, key, &appsv1.Deployment{})).To(Succeed(), "a running Deployment is left as it is")
	})
})
