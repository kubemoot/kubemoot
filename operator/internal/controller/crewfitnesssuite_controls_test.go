/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"bytes"
	"context"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/xuri/excelize/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Envtest coverage for the suite controls (spec.suspend and spec.cancel). The
// reconciler runs with no NATS publisher, so no artifact is written and the
// deferred judge never starts; these tests pin the scheduling and phase
// behavior only.
var _ = Describe("CrewFitnessSuite pause, resume and cancel", func() {
	const namespace = "default"
	ctx := context.Background()

	newReconciler := func() *CrewFitnessSuiteReconciler {
		return &CrewFitnessSuiteReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}

	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Name: name, Namespace: namespace}
	}

	reconcileOnce := func(name string) reconcile.Result {
		res, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key(name)})
		Expect(err).NotTo(HaveOccurred())
		return res
	}

	getSuite := func(name string) *kubemootv1alpha1.CrewFitnessSuite {
		s := &kubemootv1alpha1.CrewFitnessSuite{}
		Expect(k8sClient.Get(ctx, key(name), s)).To(Succeed())
		return s
	}

	editSpec := func(name string, edit func(*kubemootv1alpha1.CrewFitnessSuiteSpec)) {
		s := getSuite(name)
		edit(&s.Spec)
		Expect(k8sClient.Update(ctx, s)).To(Succeed())
	}

	children := func(name string) []kubemootv1alpha1.CrewFitness {
		list := &kubemootv1alpha1.CrewFitnessList{}
		Expect(k8sClient.List(ctx, list, client.InNamespace(namespace),
			client.MatchingLabels{suiteOwnerLabel: name})).To(Succeed())
		return list.Items
	}

	childName := func(name string, iter int32) string {
		return iterationCRName(getSuite(name).Status.RunID, 0, iter)
	}

	finishChild := func(childName string, phase kubemootv1alpha1.CrewFitnessPhase) {
		cf := &kubemootv1alpha1.CrewFitness{}
		Expect(k8sClient.Get(ctx, key(childName), cf)).To(Succeed())
		cf.Status.Phase = phase
		Expect(k8sClient.Status().Update(ctx, cf)).To(Succeed())
	}

	// startRunning creates a suite and reconciles it through finalizer, start,
	// and the first scheduling tick, leaving iteration 1 in flight.
	startRunning := func(name string, iterations int32) {
		suite := &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
				CrewRef:    "controls-crew",
				Iterations: iterations,
				Scripts:    []kubemootv1alpha1.SuiteScript{{TestRef: "probe", TestContent: probeScript}},
			},
		}
		Expect(k8sClient.Create(ctx, suite)).To(Succeed())
		reconcileOnce(name) // add finalizer
		reconcileOnce(name) // Pending -> Running
		reconcileOnce(name) // schedule iteration 1
		Expect(getSuite(name).Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseRunning))
		Expect(children(name)).To(HaveLen(1))
	}

	// pauseAfterFirst suspends a running suite and lets iteration 1 finish,
	// ending in Paused with one completed iteration.
	pauseAfterFirst := func(name string) {
		editSpec(name, func(s *kubemootv1alpha1.CrewFitnessSuiteSpec) { s.Suspend = true })
		finishChild(childName(name, 1), kubemootv1alpha1.CrewFitnessPhasePassed)
		res := reconcileOnce(name)
		Expect(res.RequeueAfter).To(BeZero(), "a Paused suite waits for a spec edit, not a timer")
		Expect(getSuite(name).Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhasePaused))
	}

	cleanup := func(name string) {
		s := getSuite(name)
		Expect(k8sClient.Delete(ctx, s)).To(Succeed())
		reconcileOnce(name) // finalizer removal
		Eventually(func() bool {
			return errors.IsNotFound(k8sClient.Get(ctx, key(name), &kubemootv1alpha1.CrewFitnessSuite{}))
		}).Should(BeTrue())
		leftover := children(name)
		for i := range leftover {
			_ = k8sClient.Delete(ctx, &leftover[i])
		}
	}

	It("pauses mid-run: the running iteration finishes and no new one starts", func() {
		const name = "controls-pause"
		startRunning(name, 3)

		editSpec(name, func(s *kubemootv1alpha1.CrewFitnessSuiteSpec) { s.Suspend = true })
		reconcileOnce(name)
		s := getSuite(name)
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseRunning),
			"still Running while iteration 1 is in flight")
		Expect(children(name)).To(HaveLen(1), "no new iteration while suspended")

		finishChild(childName(name, 1), kubemootv1alpha1.CrewFitnessPhasePassed)
		reconcileOnce(name)
		s = getSuite(name)
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhasePaused))
		Expect(s.Status.IterationsCompleted).To(Equal(int32(1)))
		Expect(s.Status.Passed).To(Equal(int32(1)))
		Expect(s.Status.CompletedAt).To(BeNil(), "Paused is not terminal")

		reconcileOnce(name)
		Expect(children(name)).To(HaveLen(1), "a Paused suite schedules nothing")
		Expect(getSuite(name).Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhasePaused))

		cleanup(name)
	})

	It("resumes where it left off and runs to Completed", func() {
		const name = "controls-resume"
		startRunning(name, 2)
		pauseAfterFirst(name)

		editSpec(name, func(s *kubemootv1alpha1.CrewFitnessSuiteSpec) { s.Suspend = false })
		reconcileOnce(name) // Paused -> Running
		Expect(getSuite(name).Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseRunning))
		reconcileOnce(name) // schedule iteration 2
		Expect(children(name)).To(HaveLen(2))
		Expect(k8sClient.Get(ctx, key(childName(name, 2)), &kubemootv1alpha1.CrewFitness{})).To(Succeed(),
			"resume schedules the next unscheduled iteration, not a repeat of iteration 1")

		finishChild(childName(name, 2), kubemootv1alpha1.CrewFitnessPhaseFailed)
		reconcileOnce(name)
		s := getSuite(name)
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted))
		Expect(s.Status.IterationsCompleted).To(Equal(int32(2)))
		Expect(s.Status.Passed).To(Equal(int32(1)))
		Expect(s.Status.Failed).To(Equal(int32(1)))
		Expect(s.Status.CompletedAt).NotTo(BeNil())

		cleanup(name)
	})

	It("cancels mid-iteration: deletes the in-flight iteration and keeps completed results", func() {
		const name = "controls-cancel-running"
		startRunning(name, 3)
		finishChild(childName(name, 1), kubemootv1alpha1.CrewFitnessPhasePassed)
		reconcileOnce(name) // schedules iteration 2
		Expect(children(name)).To(HaveLen(2))
		inFlight := childName(name, 2)

		editSpec(name, func(s *kubemootv1alpha1.CrewFitnessSuiteSpec) { s.Cancel = true })
		reconcileOnce(name)

		s := getSuite(name)
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCancelled))
		Expect(s.Status.CompletedAt).NotTo(BeNil())
		Expect(s.Status.IterationsCompleted).To(Equal(int32(1)))
		Expect(s.Status.Passed).To(Equal(int32(1)))
		Expect(s.Status.IterationsTotal).To(Equal(int32(3)), "total stays planned so the run reads as partial")
		Expect(errors.IsNotFound(k8sClient.Get(ctx, key(inFlight), &kubemootv1alpha1.CrewFitness{}))).
			To(BeTrue(), "the in-flight iteration is deleted")
		Expect(k8sClient.Get(ctx, key(childName(name, 1)), &kubemootv1alpha1.CrewFitness{})).
			To(Succeed(), "the completed iteration is kept for the XLSX")

		reconcileOnce(name) // terminal upkeep
		Expect(children(name)).To(HaveLen(1), "no new iteration after cancel")
		Expect(getSuite(name).Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCancelled))

		cleanup(name)
	})

	It("cancels while paused", func() {
		const name = "controls-cancel-paused"
		startRunning(name, 3)
		pauseAfterFirst(name)

		editSpec(name, func(s *kubemootv1alpha1.CrewFitnessSuiteSpec) { s.Cancel = true })
		reconcileOnce(name)

		s := getSuite(name)
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCancelled))
		Expect(s.Status.CompletedAt).NotTo(BeNil())
		Expect(s.Status.IterationsCompleted).To(Equal(int32(1)))
		Expect(children(name)).To(HaveLen(1), "the completed iteration is kept")

		// Clearing suspend after cancel does not revive the suite.
		editSpec(name, func(s *kubemootv1alpha1.CrewFitnessSuiteSpec) { s.Suspend = false })
		reconcileOnce(name)
		Expect(getSuite(name).Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCancelled))
		Expect(children(name)).To(HaveLen(1))

		cleanup(name)
	})

	It("cancels a suite before it starts", func() {
		const name = "controls-cancel-pending"
		suite := &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
				CrewRef:    "controls-crew",
				Iterations: 2,
				Cancel:     true,
				Scripts:    []kubemootv1alpha1.SuiteScript{{TestRef: "probe", TestContent: probeScript}},
			},
		}
		Expect(k8sClient.Create(ctx, suite)).To(Succeed())
		reconcileOnce(name) // add finalizer
		reconcileOnce(name)
		s := getSuite(name)
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCancelled))
		Expect(s.Status.StartedAt).To(BeNil(), "a cancelled Pending suite never starts")
		Expect(s.Status.RunID).NotTo(BeEmpty(), "the run identity is recorded so any report key is well formed")
		Expect(s.Status.IterationsTotal).To(Equal(int32(2)), "the planned total is recorded so the run reads 0 of 2")
		Expect(s.Status.IterationsCompleted).To(BeZero())
		Expect(children(name)).To(BeEmpty())

		reconcileOnce(name) // terminal upkeep with no children: nothing to harvest
		Expect(getSuite(name).Status.ArtifactRef).To(BeNil())

		cleanup(name)
	})

	It("ignores cancel on a Completed suite", func() {
		const name = "controls-cancel-completed"
		startRunning(name, 1)
		finishChild(childName(name, 1), kubemootv1alpha1.CrewFitnessPhasePassed)
		reconcileOnce(name)
		Expect(getSuite(name).Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted))

		editSpec(name, func(s *kubemootv1alpha1.CrewFitnessSuiteSpec) { s.Cancel = true })
		reconcileOnce(name)
		Expect(getSuite(name).Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted))

		cleanup(name)
	})
})

// probeScript is a minimal inline fitness script.
const probeScript = "ASSERT(true)"

// cacheTestNamespace is the namespace of the stale-cache pause test.
const (
	cacheTestNamespace = "crew-cache"
	cacheTestSuite     = "stale"
)

// TestPauseWaitsForChildMissingFromCache pins that Paused is decided from an
// uncached read: the informer cache may not yet hold a child created on the
// previous tick, and reporting Paused then would claim the crew is idle while
// an iteration runs.
func TestPauseWaitsForChildMissingFromCache(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	const runID = "cache123"
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: cacheTestSuite, Namespace: cacheTestNamespace},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			CrewRef: "c", Iterations: 3, Suspend: true,
			Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "x", TestContent: probeScript}},
		},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{
			Phase: kubemootv1alpha1.CrewFitnessSuitePhaseRunning, RunID: runID, IterationsTotal: 3,
		},
	}
	running := &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{
			Name: iterationCRName(runID, 0, 1), Namespace: cacheTestNamespace,
			Labels: map[string]string{suiteOwnerLabel: cacheTestSuite},
		},
		Status: kubemootv1alpha1.CrewFitnessStatus{Phase: kubemootv1alpha1.CrewFitnessPhaseRunning},
	}
	cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(suite).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitnessSuite{}).Build()
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(suite.DeepCopy(), running).Build()
	r := &CrewFitnessSuiteReconciler{Client: cached, APIReader: live, Scheme: scheme}

	if _, err := r.advanceRunning(context.Background(), suite); err != nil {
		t.Fatalf("advanceRunning: %v", err)
	}
	got := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := cached.Get(context.Background(), client.ObjectKey{Namespace: cacheTestNamespace, Name: cacheTestSuite}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseRunning {
		t.Errorf("phase = %q, want Running while the live read shows an iteration in flight", got.Status.Phase)
	}

	// Once the live read agrees nothing is in flight, the suite pauses.
	r.APIReader = cached
	if _, err := r.advanceRunning(context.Background(), got); err != nil {
		t.Fatalf("advanceRunning: %v", err)
	}
	if err := cached.Get(context.Background(), client.ObjectKey{Namespace: cacheTestNamespace, Name: cacheTestSuite}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhasePaused {
		t.Errorf("phase = %q, want Paused", got.Status.Phase)
	}
}

// TestDeleteInFlightKeepsChildThatFinished pins the conditional delete: a
// child whose resourceVersion moved (it finished after the list) is not
// deleted, and the Conflict surfaces so the next reconcile recounts.
func TestDeleteInFlightKeepsChildThatFinished(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	child := &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{Name: "run-x-s0-i1", Namespace: "crew-race"},
		Status:     kubemootv1alpha1.CrewFitnessStatus{Phase: kubemootv1alpha1.CrewFitnessPhaseRunning},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(child).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitness{}).Build()
	ctx := context.Background()
	listed := &kubemootv1alpha1.CrewFitness{}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(child), listed); err != nil {
		t.Fatalf("get: %v", err)
	}
	// The child finishes after the reconciler listed it.
	finished := listed.DeepCopy()
	finished.Status.Phase = kubemootv1alpha1.CrewFitnessPhasePassed
	if err := cli.Status().Update(ctx, finished); err != nil {
		t.Fatalf("status update: %v", err)
	}

	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme}
	n, err := r.deleteInFlight(ctx, []kubemootv1alpha1.CrewFitness{*listed})
	if err == nil {
		t.Fatal("want a Conflict error for a stale child, got nil")
	}
	if n != 0 {
		t.Errorf("deleted = %d, want 0", n)
	}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(child), &kubemootv1alpha1.CrewFitness{}); err != nil {
		t.Errorf("the finished child must survive: %v", err)
	}

	// A NotFound child is not counted as deleted.
	gone := listed.DeepCopy()
	gone.Name = "run-x-s0-i2"
	if n, err := r.deleteInFlight(ctx, []kubemootv1alpha1.CrewFitness{*gone}); err != nil || n != 0 {
		t.Errorf("deleteInFlight(missing) = (%d, %v), want (0, nil)", n, err)
	}
}

// TestRunningNextPhase pins the phase decision for a Running tick.
func TestRunningNextPhase(t *testing.T) {
	cases := []struct {
		name    string
		suspend bool
		p       childProgress
		total   int32
		want    kubemootv1alpha1.CrewFitnessSuitePhase
	}{
		{"running, not suspended", false, childProgress{completed: 1, inFlight: 1}, 3, kubemootv1alpha1.CrewFitnessSuitePhaseRunning},
		{"not suspended, idle between ticks", false, childProgress{completed: 1}, 3, kubemootv1alpha1.CrewFitnessSuitePhaseRunning},
		{"suspended, draining", true, childProgress{completed: 1, inFlight: 1}, 3, kubemootv1alpha1.CrewFitnessSuitePhaseRunning},
		{"suspended, idle", true, childProgress{completed: 1}, 3, kubemootv1alpha1.CrewFitnessSuitePhasePaused},
		{"suspended, zero done, idle", true, childProgress{}, 3, kubemootv1alpha1.CrewFitnessSuitePhasePaused},
		{"all done", false, childProgress{completed: 3}, 3, kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
		{"all done while suspended", true, childProgress{completed: 3}, 3, kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runningNextPhase(c.suspend, c.p, c.total); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestIsSuiteTerminal pins which phases stop scheduling for good. Paused is
// not terminal: it resumes.
func TestIsSuiteTerminal(t *testing.T) {
	terminal := map[kubemootv1alpha1.CrewFitnessSuitePhase]bool{
		"": false,
		kubemootv1alpha1.CrewFitnessSuitePhasePending:   false,
		kubemootv1alpha1.CrewFitnessSuitePhaseRunning:   false,
		kubemootv1alpha1.CrewFitnessSuitePhasePaused:    false,
		kubemootv1alpha1.CrewFitnessSuitePhaseCompleted: true,
		kubemootv1alpha1.CrewFitnessSuitePhaseFailed:    true,
		kubemootv1alpha1.CrewFitnessSuitePhaseError:     true,
		kubemootv1alpha1.CrewFitnessSuitePhaseCancelled: true,
	}
	for phase, want := range terminal {
		if got := isSuiteTerminal(phase); got != want {
			t.Errorf("isSuiteTerminal(%q) = %v, want %v", phase, got, want)
		}
	}
}

// TestJudgeSkippedOnlyForCancelled pins the deferred-judge rule: a cancelled
// suite is never judged; every other phase follows the normal gate.
func TestJudgeSkippedOnlyForCancelled(t *testing.T) {
	for _, phase := range []kubemootv1alpha1.CrewFitnessSuitePhase{
		kubemootv1alpha1.CrewFitnessSuitePhaseCompleted,
		kubemootv1alpha1.CrewFitnessSuitePhaseRunning,
		kubemootv1alpha1.CrewFitnessSuitePhasePaused,
	} {
		s := &kubemootv1alpha1.CrewFitnessSuite{Status: kubemootv1alpha1.CrewFitnessSuiteStatus{Phase: phase}}
		if judgeSkipped(s) {
			t.Errorf("judgeSkipped for %q = true, want false", phase)
		}
	}
	s := &kubemootv1alpha1.CrewFitnessSuite{Status: kubemootv1alpha1.CrewFitnessSuiteStatus{Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCancelled}}
	if !judgeSkipped(s) {
		t.Error("judgeSkipped for Cancelled = false, want true")
	}
	// With a NATS publisher absent the state is complete either way; the skip
	// also reports complete with no scores so the reap gate never waits.
	r := &CrewFitnessSuiteReconciler{}
	s.Status.RunID = "run-cancel"
	if complete, scores := r.deferredJudgeState(s); !complete || scores != nil {
		t.Errorf("deferredJudgeState for Cancelled = (%v, %v), want (true, nil)", complete, scores)
	}
}

// TestTerminalChildrenDropsInFlightAndDeleting pins the harvest filter: only
// finished, not-deleting children feed a cancelled suite's counts and XLSX.
func TestTerminalChildrenDropsInFlightAndDeleting(t *testing.T) {
	mk := func(name string, phase kubemootv1alpha1.CrewFitnessPhase, deleting bool) kubemootv1alpha1.CrewFitness {
		cf := kubemootv1alpha1.CrewFitness{ObjectMeta: metav1.ObjectMeta{Name: name}}
		cf.Status.Phase = phase
		if deleting {
			now := metav1.Now()
			cf.DeletionTimestamp = &now
		}
		return cf
	}
	in := []kubemootv1alpha1.CrewFitness{
		mk("passed", kubemootv1alpha1.CrewFitnessPhasePassed, false),
		mk("failed", kubemootv1alpha1.CrewFitnessPhaseFailed, false),
		mk("error", kubemootv1alpha1.CrewFitnessPhaseError, false),
		mk("running", kubemootv1alpha1.CrewFitnessPhaseRunning, false),
		mk("pending", kubemootv1alpha1.CrewFitnessPhasePending, false),
		mk("empty", "", false),
		mk("passed-deleting", kubemootv1alpha1.CrewFitnessPhasePassed, true),
	}
	got := terminalChildren(in)
	names := make([]string, 0, len(got))
	for _, c := range got {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "passed,failed,error" {
		t.Errorf("terminalChildren = %v, want [passed failed error]", names)
	}
	if len(terminalChildren(nil)) != 0 {
		t.Error("terminalChildren(nil) should be empty")
	}
}

// cancelledOverviewValue builds an XLSX for a suite in the given phase (1 of 3
// iterations completed) and returns the Overview value for label.
func cancelledOverviewValue(t *testing.T, phase kubemootv1alpha1.CrewFitnessSuitePhase, label string) (string, bool) {
	t.Helper()
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "crew-partial"},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: "c", Iterations: 3},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{
			RunID: "r1", Phase: phase, IterationsCompleted: 1, IterationsTotal: 3,
		},
	}
	results := []IterationResult{{Scenario: "s1", Iteration: 1,
		Phase: kubemootv1alpha1.CrewFitnessPhasePassed, AssertionsPassed: 1, AssertionsTotal: 1}}
	b, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatalf("build XLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("open XLSX: %v", err)
	}
	defer func() { _ = f.Close() }()
	rows, err := f.GetRows("Overview")
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	for _, row := range rows {
		if len(row) >= 2 && row[0] == label {
			return row[1], true
		}
	}
	return "", false
}

// TestOverviewMarksCancelledSuitePartial pins the XLSX partial marker: a
// Cancelled suite's Overview carries a Partial row with completed/total; a
// Completed suite has none.
func TestOverviewMarksCancelledSuitePartial(t *testing.T) {
	v, ok := cancelledOverviewValue(t, kubemootv1alpha1.CrewFitnessSuitePhaseCancelled, "Partial")
	if !ok {
		t.Fatal("Cancelled suite Overview missing the Partial row")
	}
	if !strings.Contains(v, "1 of 3") || !strings.Contains(v, "judge skipped") {
		t.Errorf("Partial = %q, want completed/total and the judge note", v)
	}
	if status, _ := cancelledOverviewValue(t, kubemootv1alpha1.CrewFitnessSuitePhaseCancelled, "Status"); status != "Cancelled" {
		t.Errorf("Status = %q, want Cancelled", status)
	}
	if _, ok := cancelledOverviewValue(t, kubemootv1alpha1.CrewFitnessSuitePhaseCompleted, "Partial"); ok {
		t.Error("Completed suite Overview must not carry a Partial row")
	}
}
