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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Envtest coverage for spec.rejudge: a judge-only pass over an earlier run's
// saved transcripts, scored against the re-judge suite's own scenarios. The
// object store is an in-memory fake; no crew is asked anything.
var _ = Describe("CrewFitnessSuite re-judge", func() {
	const (
		namespace = "default"
		sourceRun = "srcrun01"
	)
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
	script := func(ref, content string) kubemootv1alpha1.SuiteScript {
		return kubemootv1alpha1.SuiteScript{TestRef: ref, TestContent: content}
	}

	// createSource makes a source suite with a recorded run in the given phase
	// and its transcripts in the store: alpha (s0) and beta (s1) answered, plus
	// "retired" (s2), a scenario the re-judge suite no longer has.
	createSource := func(name string, phase kubemootv1alpha1.CrewFitnessSuitePhase) {
		src := &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
				CrewRef:    testCrewPilot,
				Iterations: 1,
				Scripts: []kubemootv1alpha1.SuiteScript{
					script(rjAlpha, "old"), script(rjBeta, "old"), script("retired", "old"),
				},
			},
		}
		Expect(k8sClient.Create(ctx, src)).To(Succeed())
		src.Status.Phase = phase
		src.Status.RunID = sourceRun
		Expect(k8sClient.Status().Update(ctx, src)).To(Succeed())
		prefix := suiteRunPrefix(namespace, name, sourceRun)
		store.objs[prefix+"s0-i1.json"] = sourceTranscript("cilium and metrics-server")
		store.objs[prefix+"s1-i1.json"] = sourceTranscript("harbor, cilium")
		store.objs[prefix+"s2-i1.json"] = sourceTranscript("retired answer")
		store.objs[prefix+"deferred-scores-v2.json"] = []byte(`{"scores":{"alpha":10},"complete":true}`)
	}

	// createRejudge makes a re-judge suite against the CURRENT scenarios: beta
	// and alpha swap places, "fresh" is new, and "retired" is gone.
	createRejudge := func(name, source, runID string) {
		suite := &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
				CrewRef:    testCrewPilot,
				Iterations: 1,
				Scripts: []kubemootv1alpha1.SuiteScript{
					script(rjBeta, newScenario(`synthesis does NOT CONTAIN "harbor"`)),
					script(rjAlpha, newScenario(`synthesis CONTAINS "metrics-server"`)),
					script("fresh", newScenario(`synthesis is non-empty`)),
				},
				Rejudge: &kubemootv1alpha1.RejudgeSource{Suite: source, RunID: runID},
			},
		}
		Expect(k8sClient.Create(ctx, suite)).To(Succeed())
		reconcileOnce(name) // add finalizer
		reconcileOnce(name) // validate, copy, complete (or fail)
	}

	expectFailure := func(name, reason string) {
		s := getSuite(name)
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseError))
		Expect(s.Status.Error).NotTo(BeEmpty())
		c := meta.FindStatusCondition(s.Status.Conditions, rejudgeConditionType)
		Expect(c).NotTo(BeNil())
		Expect(c.Status).To(Equal(metav1.ConditionFalse))
		Expect(c.Reason).To(Equal(reason))
		Expect(s.Status.RunID).To(BeEmpty(), "a failed re-judge records no run")
	}

	cleanup := func(names ...string) {
		for _, name := range names {
			s := &kubemootv1alpha1.CrewFitnessSuite{}
			if k8sClient.Get(ctx, key(name), s) != nil {
				continue
			}
			Expect(k8sClient.Delete(ctx, s)).To(Succeed())
			reconcileOnce(name) // finalizer removal
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, key(name), &kubemootv1alpha1.CrewFitnessSuite{}))
			}).Should(BeTrue())
		}
	}

	BeforeEach(func() {
		store = newLockedStore(map[string][]byte{})
	})

	It("copies the source answers re-indexed to its own scenarios and completes", func() {
		createSource("rj-src-ok", kubemootv1alpha1.CrewFitnessSuitePhaseCompleted)
		createRejudge("rj-ok", "rj-src-ok", sourceRun)

		s := getSuite("rj-ok")
		Expect(s.Status.Phase).To(Equal(kubemootv1alpha1.CrewFitnessSuitePhaseCompleted))
		Expect(s.Status.RunID).NotTo(BeEmpty())
		Expect(s.Status.IterationsTotal).To(Equal(int32(2)))
		Expect(s.Status.IterationsCompleted).To(Equal(int32(2)))
		Expect(s.Status.Passed).To(Equal(int32(1)), "alpha names metrics-server")
		Expect(s.Status.Failed).To(Equal(int32(1)), "beta names harbor, which the current scenario forbids")
		Expect(s.Status.Rejudge).NotTo(BeNil())
		Expect(s.Status.Rejudge.Source).To(Equal(kubemootv1alpha1.RejudgeSource{Suite: "rj-src-ok", RunID: sourceRun}))
		Expect(s.Status.Rejudge.Transcripts).To(Equal(int32(2)))
		Expect(s.Status.Rejudge.NotInSource).To(Equal([]string{"fresh"}))
		Expect(meta.IsStatusConditionTrue(s.Status.Conditions, rejudgeConditionType)).To(BeTrue())
		Expect(s.Status.Scenarios).To(Equal([]kubemootv1alpha1.SuiteScenarioResult{
			{Name: rjBeta, Iterations: 1, Failed: 1, MeanDurationMs: 42, AssertionsPassed: 2, AssertionsTotal: 3},
			{Name: rjAlpha, Iterations: 1, Passed: 1, MeanDurationMs: 42, AssertionsPassed: 3, AssertionsTotal: 3},
			{Name: "fresh"},
		}), "status.scenarios rolls up the copied transcripts in spec order")
		Expect(s.Status.Judge).To(Equal(&kubemootv1alpha1.FitnessJudgeStatus{Phase: kubemootv1alpha1.FitnessJudgePhaseJudging}))

		prefix := suiteRunPrefix(namespace, "rj-ok", s.Status.RunID)
		beta := store.get(prefix + "s0-i1.json")
		Expect(beta).NotTo(BeNil(), "beta is script 0 of the re-judge suite")
		Expect(store.get(prefix+"s2-i1.json")).To(BeNil(), "fresh has no source transcript")
		var td transcriptDoc
		Expect(json.Unmarshal(beta, &td)).To(Succeed())
		Expect(referenceForKeyword(td, deferKeyword(newDeferRaw))).To(Equal("new reference"))
		var from struct {
			RejudgedFrom rejudgedFrom `json:"rejudgedFrom"`
		}
		Expect(json.Unmarshal(beta, &from)).To(Succeed())
		Expect(from.RejudgedFrom.Key).To(Equal(suiteRunPrefix(namespace, "rj-src-ok", sourceRun) + "s1-i1.json"))
		Expect(store.get(prefix+"deferred-scores-v2.json")).To(BeNil(),
			"the source's judge scores are not carried over")

		cleanup("rj-ok", "rj-src-ok")
	})

	It("rejects adding, changing, or removing spec.rejudge after creation", func() {
		createSource("rj-src-immutable", kubemootv1alpha1.CrewFitnessSuitePhaseCompleted)
		createRejudge("rj-immutable", "rj-src-immutable", sourceRun)
		s := getSuite("rj-immutable")
		s.Spec.Rejudge.RunID = "otherrun"
		Expect(k8sClient.Update(ctx, s)).NotTo(Succeed(), "changing the source is rejected")
		s = getSuite("rj-immutable")
		s.Spec.Rejudge = nil
		Expect(k8sClient.Update(ctx, s)).NotTo(Succeed(), "removing it is rejected")

		src := getSuite("rj-src-immutable")
		src.Spec.Rejudge = &kubemootv1alpha1.RejudgeSource{Suite: "rj-immutable", RunID: sourceRun}
		Expect(k8sClient.Update(ctx, src)).NotTo(Succeed(), "adding it to an existing suite is rejected")
		src = getSuite("rj-src-immutable")
		src.Spec.Description = "other edits still work"
		Expect(k8sClient.Update(ctx, src)).To(Succeed())

		cleanup("rj-immutable", "rj-src-immutable")
	})

	It("fails with SourceNotFound when the source suite does not exist", func() {
		createRejudge("rj-nosrc", "rj-absent-src", sourceRun)
		expectFailure("rj-nosrc", reasonSourceNotFound)
		cleanup("rj-nosrc")
	})

	It("fails with SourceNotCompleted when the source run is still running", func() {
		createSource("rj-src-running", kubemootv1alpha1.CrewFitnessSuitePhaseRunning)
		createRejudge("rj-running", "rj-src-running", sourceRun)
		expectFailure("rj-running", reasonSourceNotCompleted)
		cleanup("rj-running", "rj-src-running")
	})

	It("fails with RunIDMismatch when the run is not the source's run", func() {
		createSource("rj-src-other", kubemootv1alpha1.CrewFitnessSuitePhaseCompleted)
		createRejudge("rj-other", "rj-src-other", "otherrun")
		expectFailure("rj-other", reasonRunIDMismatch)
		cleanup("rj-other", "rj-src-other")
	})

	It("fails with NoTranscripts when no scenario of this suite is in the source", func() {
		createSource("rj-src-none", kubemootv1alpha1.CrewFitnessSuitePhaseCompleted)
		suite := &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: "rj-none", Namespace: namespace},
			Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
				CrewRef:    testCrewPilot,
				Iterations: 1,
				Scripts:    []kubemootv1alpha1.SuiteScript{script("fresh", newScenario(`synthesis is non-empty`))},
				Rejudge:    &kubemootv1alpha1.RejudgeSource{Suite: "rj-src-none", RunID: sourceRun},
			},
		}
		Expect(k8sClient.Create(ctx, suite)).To(Succeed())
		reconcileOnce("rj-none")
		reconcileOnce("rj-none")
		expectFailure("rj-none", reasonNoTranscripts)
		cleanup("rj-none", "rj-src-none")
	})

	It("resumes the judge from its checkpoint and then writes the artifact", func() {
		createSource("rj-src-resume", kubemootv1alpha1.CrewFitnessSuitePhaseCompleted)
		createRejudge("rj-resume", "rj-src-resume", sourceRun)
		s := getSuite("rj-resume")
		prefix := suiteRunPrefix(namespace, "rj-resume", s.Status.RunID)

		// A prior pass scored both copied scenarios and stopped before marking
		// the checkpoint complete (an operator restart). No judge crew exists in
		// this test, so a re-dispatch would leave the pass pending: completing
		// proves the pass resumed from the checkpoint without judging again.
		store.objs[deferredSidecarKey(prefix)] = []byte(
			`{"scores":{"alpha":90,"beta":40},"reasons":{"beta":"names harbor"},"complete":false}`)

		res := reconcileOnce("rj-resume") // terminal upkeep starts the judge worker
		Expect(res.RequeueAfter).To(Equal(artifactRetryInterval), "waits on judge completion")
		Eventually(func() kubemootv1alpha1.FitnessJudgePhase {
			if _, running := deferredInFlight.Load(prefix); running {
				return ""
			}
			if js := getSuite("rj-resume").Status.Judge; js != nil {
				return js.Phase
			}
			return ""
		}).Should(Equal(kubemootv1alpha1.FitnessJudgePhaseComplete), "the worker writes status.judge at completion")
		js := getSuite("rj-resume").Status.Judge
		Expect(js.Judged).To(Equal(int32(2)))
		Expect(js.Total).To(Equal(int32(2)), "fresh has no transcript, so nothing to judge")
		Expect(js.Mean).To(HaveValue(Equal(int32(65))))
		Expect(js.Zeros).To(BeZero())
		Expect(js.CompletedAt).NotTo(BeNil())
		Expect(js.Scores).To(Equal([]kubemootv1alpha1.FitnessJudgeScore{
			{Scenario: rjBeta, Score: 40, Reason: "names harbor"},
			{Scenario: rjAlpha, Score: 90},
		}), "scores follow the re-judge suite's spec order")
		cache := loadDeferredCache(store, prefix)
		Expect(cache.Scores).To(Equal(map[string]float64{rjAlpha: 90, rjBeta: 40}))
		Expect(cache.Reasons[rjBeta]).To(Equal("names harbor"))

		reconcileOnce("rj-resume") // judge complete: build the XLSX from the copies
		s = getSuite("rj-resume")
		Expect(s.Status.ArtifactRef).NotTo(BeNil())
		Expect(store.get(s.Status.ArtifactRef.ObjectKey)).NotTo(BeEmpty())
		Expect(reconcileOnce("rj-resume").RequeueAfter).To(BeZero(), "steady state: nothing left to do")

		cleanup("rj-resume", "rj-src-resume")
	})
})
