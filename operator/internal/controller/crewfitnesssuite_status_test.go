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
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	statusKeyword   = "STATUSKW"
	statusJudgeCrew = "status-judge-crew"
)

// statusTranscript is a stored 10 ms iteration with the given assertions (raw
// text and pass flag pairs) and a synthesis.
func statusTranscript(assertions ...string) []byte {
	parts := make([]string, 0, len(assertions)/2)
	for i := 0; i+1 < len(assertions); i += 2 {
		raw, _ := json.Marshal(assertions[i])
		parts = append(parts, `{"raw":`+string(raw)+`,"passed":`+assertions[i+1]+`}`)
	}
	return []byte(`{"question":"q","durationMs":10,"assertions":[` + strings.Join(parts, ",") +
		`],"events":[{"type":"synthesis","content":"answer"},{"type":"done"}]}`)
}

// Envtest coverage for the results the operator puts on CrewFitnessSuite
// status: status.judge (the deferred judge pass) and status.scenarios (the
// per-scenario rollup). The object store is an in-memory fake; the judge crew
// is never called (gated and checkpointed scenarios need no dispatch).
var _ = Describe("CrewFitnessSuite results in status", func() {
	const namespace = "default"
	ctx := context.Background()

	var store *lockedStore

	reconciler := func() *CrewFitnessSuiteReconciler {
		return &CrewFitnessSuiteReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), artifacts: store}
	}
	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Name: name, Namespace: namespace}
	}
	reconcileOnce := func(name string) reconcile.Result {
		res, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: key(name)})
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	getSuite := func(name string) *kubemootv1alpha1.CrewFitnessSuite {
		s := &kubemootv1alpha1.CrewFitnessSuite{}
		Expect(k8sClient.Get(ctx, key(name), s)).To(Succeed())
		return s
	}
	cleanup := func(name string) {
		s := &kubemootv1alpha1.CrewFitnessSuite{}
		if k8sClient.Get(ctx, key(name), s) != nil {
			return
		}
		Expect(k8sClient.Delete(ctx, s)).To(Succeed())
		reconcileOnce(name) // finalizer removal
		Eventually(func() bool {
			return errors.IsNotFound(k8sClient.Get(ctx, key(name), &kubemootv1alpha1.CrewFitnessSuite{}))
		}).Should(BeTrue())
	}
	// workerIdle waits until no judge worker runs for the suite's run.
	workerIdle := func(prefix string) {
		Eventually(func() bool {
			_, running := deferredInFlight.Load(prefix)
			return running
		}).Should(BeFalse())
	}

	BeforeEach(func() {
		store = newLockedStore(map[string][]byte{})
	})

	It("writes status.judge as the judge scores, at completion, and keeps it after the checkpoint expires", func() {
		crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{
			Name: statusJudgeCrew, Namespace: namespace,
			Labels: map[string]string{keywordCrewLabel: statusKeyword},
		}}
		Expect(k8sClient.Create(ctx, crew)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, crew))).To(Succeed()) })

		const name, runID = "js-incremental", "jsrun001"
		suite := &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
				CrewRef: "status-crew", Iterations: 1,
				Scripts: []kubemootv1alpha1.SuiteScript{
					{TestRef: "gated", TestContent: probeScript},
					{TestRef: "resumed", TestContent: probeScript},
					{TestRef: "unjudged", TestContent: probeScript},
					{TestRef: "plain", TestContent: probeScript},
				},
			},
		}
		Expect(k8sClient.Create(ctx, suite)).To(Succeed())
		reconcileOnce(name) // add finalizer
		s := getSuite(name)
		s.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseCompleted
		s.Status.RunID = runID
		Expect(k8sClient.Status().Update(ctx, s)).To(Succeed())

		prefix := suiteRunPrefix(namespace, name, runID)
		deferOn := func(kw string) string { return "DEFER synthesis " + kw + ` "the reference"` }
		// gated: the agree floor failed, so the worker records 0 without a dispatch.
		store.objs[prefix+"s0-i1.json"] = statusTranscript(deferOn(statusKeyword), "true", "at least 2 toolers agree", "false")
		// resumed: scored by an earlier pass (in the checkpoint below).
		store.objs[prefix+"s1-i1.json"] = statusTranscript(deferOn(statusKeyword), "true")
		// unjudged: no crew declares its keyword, so it stays pending.
		store.objs[prefix+"s2-i1.json"] = statusTranscript(deferOn("NOJUDGE"), "true")
		// plain: no DEFER assertion, nothing to judge.
		store.objs[prefix+"s3-i1.json"] = statusTranscript("synthesis is non-empty", "true")
		longReason := "first line\nsecond line " + strings.Repeat("x", 300)
		cp, _ := json.Marshal(deferredScoreCache{
			Scores: map[string]float64{"resumed": 90}, Reasons: map[string]string{"resumed": longReason},
		})
		store.objs[deferredSidecarKey(prefix)] = cp

		reconcileOnce(name) // terminal upkeep starts the judge worker
		workerIdle(prefix)

		js := getSuite(name).Status.Judge
		Expect(js).NotTo(BeNil())
		Expect(js.Phase).To(Equal(kubemootv1alpha1.FitnessJudgePhaseJudging), "one scenario is still pending")
		Expect(js.Judged).To(Equal(int32(2)))
		Expect(js.Total).To(Equal(int32(3)), "plain has no DEFER assertion")
		Expect(js.Zeros).To(Equal(int32(1)))
		Expect(js.Mean).To(HaveValue(Equal(int32(45))))
		Expect(js.CompletedAt).To(BeNil())
		Expect(js.Scores).To(HaveLen(2))
		Expect(js.Scores[0].Scenario).To(Equal("gated"))
		Expect(js.Scores[0].Score).To(BeZero())
		Expect(js.Scores[0].Reason).To(ContainSubstring("all iterations gated"))
		Expect(js.Scores[1].Scenario).To(Equal("resumed"))
		Expect(js.Scores[1].Score).To(Equal(int32(90)))
		Expect(js.Scores[1].Reason).To(HavePrefix("first line second line xxx"))
		Expect(js.Scores[1].Reason).To(HaveSuffix("..."))
		Expect([]rune(js.Scores[1].Reason)).To(HaveLen(kubemootv1alpha1.MaxJudgeReasonLength))
		Expect(loadDeferredCache(store, prefix).Reasons["resumed"]).To(Equal(longReason),
			"the full reason stays in the object store")

		// A later pass scores the last scenario and marks the checkpoint complete.
		cache := loadDeferredCache(store, prefix)
		cache.Scores["unjudged"] = 60
		cache.Complete = true
		cp, _ = json.Marshal(cache)
		store.objs[deferredSidecarKey(prefix)] = cp
		reconcileOnce(name)

		js = getSuite(name).Status.Judge
		Expect(js.Phase).To(Equal(kubemootv1alpha1.FitnessJudgePhaseComplete))
		Expect(js.Judged).To(Equal(int32(3)))
		Expect(js.Total).To(Equal(int32(3)))
		Expect(js.Mean).To(HaveValue(Equal(int32(50))))
		Expect(js.CompletedAt).NotTo(BeNil())
		completedAt := js.CompletedAt.DeepCopy()

		// The checkpoint passes its object-store retention; the worker runs again
		// over what is left, but status.judge is the durable record.
		store.mu.Lock()
		delete(store.objs, deferredSidecarKey(prefix))
		store.mu.Unlock()
		reconcileOnce(name)
		workerIdle(prefix)
		js = getSuite(name).Status.Judge
		Expect(js.Phase).To(Equal(kubemootv1alpha1.FitnessJudgePhaseComplete))
		Expect(js.Judged).To(Equal(int32(3)))
		Expect(js.CompletedAt.Equal(completedAt)).To(BeTrue())

		cleanup(name)
	})

	It("rolls up iterations per scenario during a run and skips the judge when cancelled", func() {
		const name = "js-cancel"
		suite := &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
				CrewRef: "status-crew", Iterations: 2,
				Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "probe", TestContent: probeScript}},
			},
		}
		Expect(k8sClient.Create(ctx, suite)).To(Succeed())
		reconcileOnce(name) // add finalizer
		reconcileOnce(name) // Pending -> Running
		s := getSuite(name)
		Expect(s.Status.Judge).To(Equal(&kubemootv1alpha1.FitnessJudgeStatus{Phase: kubemootv1alpha1.FitnessJudgePhasePending}))
		Expect(s.Status.Scenarios).To(Equal([]kubemootv1alpha1.SuiteScenarioResult{{Name: "probe"}}))

		reconcileOnce(name) // schedule iteration 1
		cf := &kubemootv1alpha1.CrewFitness{}
		Expect(k8sClient.Get(ctx, key(iterationCRName(getSuite(name).Status.RunID, 0, 1)), cf)).To(Succeed())
		cf.Status.Phase = kubemootv1alpha1.CrewFitnessPhasePassed
		cf.Status.DurationMs = 1500
		cf.Status.Assertions = []kubemootv1alpha1.AssertionResult{{Raw: "a", Passed: true}, {Raw: "b", Passed: true}}
		Expect(k8sClient.Status().Update(ctx, cf)).To(Succeed())

		reconcileOnce(name) // count iteration 1, schedule iteration 2
		want := []kubemootv1alpha1.SuiteScenarioResult{{
			Name: "probe", Iterations: 1, Passed: 1, MeanDurationMs: 1500, AssertionsPassed: 2, AssertionsTotal: 2,
		}}
		Expect(getSuite(name).Status.Scenarios).To(Equal(want))

		s = getSuite(name)
		s.Spec.Cancel = true
		Expect(k8sClient.Update(ctx, s)).To(Succeed())
		reconcileOnce(name) // stop: iteration 2 is deleted in flight
		s = getSuite(name)
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCancelled))
		Expect(s.Status.Judge).To(Equal(&kubemootv1alpha1.FitnessJudgeStatus{Phase: kubemootv1alpha1.FitnessJudgePhaseSkipped}))
		Expect(s.Status.Scenarios).To(Equal(want), "the in-flight iteration is not counted")

		reconcileOnce(name) // terminal upkeep: no judge pass for a cancelled run
		prefix := suiteRunPrefix(namespace, name, s.Status.RunID)
		_, running := deferredInFlight.Load(prefix)
		Expect(running).To(BeFalse())
		Expect(store.get(deferredSidecarKey(prefix))).To(BeNil())
		Expect(getSuite(name).Status.Judge.Phase).To(Equal(kubemootv1alpha1.FitnessJudgePhaseSkipped))

		cleanup(name)
	})

	It("accepts status at the size bound, far below 1 MiB, and rejects lists past it", func() {
		const name = "js-size"
		suite := &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
				CrewRef: "status-crew", Iterations: 1,
				Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "probe", TestContent: probeScript}},
			},
		}
		Expect(k8sClient.Create(ctx, suite)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, suite))).To(Succeed()) })

		s := getSuite(name)
		s.Status.Judge, s.Status.Scenarios = maximalResults(kubemootv1alpha1.MaxStatusScenarios)
		Expect(k8sClient.Status().Update(ctx, s)).To(Succeed())
		raw, err := json.Marshal(getSuite(name))
		Expect(err).NotTo(HaveOccurred())
		Expect(len(raw)).To(BeNumerically("<", 500*1024), "worst-case status stays under 500 KiB")

		s = getSuite(name)
		s.Status.Judge, s.Status.Scenarios = maximalResults(kubemootv1alpha1.MaxStatusScenarios + 1)
		Expect(k8sClient.Status().Update(ctx, s)).NotTo(Succeed(), "the schema caps the lists")
	})
})
