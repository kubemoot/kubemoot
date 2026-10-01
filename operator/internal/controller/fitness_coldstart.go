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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// coldStart brings the crew to a clean starting state once, before the suite's
// first iteration runs. It purges the crew's working memory (default ON) so a
// baseline is not biased by facts learned in earlier runs; it is best-effort, a
// failure is logged but does not fail the suite.
//
// It deliberately does NOT touch model residency. Loading and unloading models
// on the GPUs is the exclusive concern of the JIT scheduler, which weighs the
// best model per inference and tracks residency in its own state. A fitness run
// evicting models directly via Ollama would bypass that accounting (leaving the
// scheduler with a stale view) and, because providers are cluster-shared, would
// also disturb every other crew's resident models. Cold-start convergence is the
// system's job: a cold start must perform as well as a warm one.
func (r *CrewFitnessSuiteReconciler) coldStart(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) {
	log := logf.FromContext(ctx)

	if kubemootv1alpha1.BoolOrTrue(suite.Spec.PurgeMemory) {
		if r.NATSPublisher == nil {
			log.Info("cold-start: purgeMemory requested but NATS publisher unavailable; skipping")
		} else if n, err := r.NATSPublisher.PurgeKVPrefix(crewMemoryBucket, crewscope.Scope{Namespace: suite.Namespace, Crew: suite.Spec.CrewRef}.MemoryPrefix()); err != nil {
			log.Info("cold-start: purge crew memory failed", "crew", suite.Spec.CrewRef, "error", err)
		} else {
			log.Info("cold-start: purged crew memory", "crew", suite.Spec.CrewRef, "deleted", n)
		}
	}
}
