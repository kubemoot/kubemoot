/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// report_server.go serves the fitness XLSX on demand, generated fresh from the
// per-iteration transcripts in NATS Object Store using the CURRENTLY DEPLOYED
// generator. No pre-baked artifact: every download reflects the latest report
// format/code. The dashboard's download endpoint delegates here.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

var reportLog = logf.Log.WithName("fitness-report")

// transcriptDoc is the subset of the runner's per-iteration transcript JSON
// (RunOutcome) the report needs. Scenario + iteration come from the object key.
// Events carry the full signal stream — synthesis text + per-agent signals —
// from which the objective behavior measures (adherence, consistency) are
// computed at report time.
type transcriptDoc struct {
	Assertions []transcriptAssertion `json:"assertions"`
	Events     []transcriptEvent     `json:"events"`
	Question   string                `json:"question"`
	DurationMs int64                 `json:"durationMs"`
	StartedAt  string                `json:"startedAt"`
}

// transcriptAssertion is one assertion result in a persisted transcript.
type transcriptAssertion struct {
	Raw     string `json:"raw"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// parseTranscriptKey extracts (scriptIdx, iter) from a transcript object key
// whose tail is "s{scriptIdx}-i{iter}.json". ok=false when the tail doesn't match.
func parseTranscriptKey(key string) (idx, iter int, ok bool) {
	base := key
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".json")
	if _, err := fmt.Sscanf(base, "s%d-i%d", &idx, &iter); err != nil {
		return 0, 0, false
	}
	return idx, iter, true
}

// tallyAssertions counts passing assertions and converts the transcript
// assertions to the CRD AssertionResult slice the report carries through.
func tallyAssertions(assertions []transcriptAssertion) (passed, total int, results []kubemootv1alpha1.AssertionResult) {
	results = make([]kubemootv1alpha1.AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		if a.Passed {
			passed++
		}
		results = append(results, kubemootv1alpha1.AssertionResult{Raw: a.Raw, Passed: a.Passed, Message: a.Message})
	}
	return passed, len(assertions), results
}

// phaseFromTally derives the iteration phase from the assertion tally: all-pass →
// Passed, any-fail → Failed, none recorded → Error.
func phaseFromTally(passed, total int) kubemootv1alpha1.CrewFitnessPhase {
	if total == 0 {
		return kubemootv1alpha1.CrewFitnessPhaseError
	}
	if passed == total {
		return kubemootv1alpha1.CrewFitnessPhasePassed
	}
	return kubemootv1alpha1.CrewFitnessPhaseFailed
}

// consensusGate finds the "≥N toolers agree" assertion (the consensus
// expectation). Its stored result gates quality — a run that failed the floor
// can't earn quality on coordinator-only text. expectedAgrees reads N. When no
// such assertion exists, consensusOK is true (no gate) and expectedAgrees is 0.
func consensusGate(assertions []transcriptAssertion) (consensusOK bool, expectedAgrees int) {
	consensusOK = true
	for _, a := range assertions {
		l := strings.ToLower(a.Raw)
		if strings.Contains(l, "tooler") && strings.Contains(l, "agree") {
			consensusOK = a.Passed
			expectedAgrees = extractLeadingInt(l)
			break
		}
	}
	return consensusOK, expectedAgrees
}

// iterationFromTranscript maps one transcript object (key + bytes) to an
// IterationResult. The key tail is "s{scriptIdx}-i{iter}.json"; the scenario
// name is resolved from the suite's ordered Scripts. Phase is derived from the
// assertions (all-pass → Passed, any-fail → Failed, none → Error).
func iterationFromTranscript(suite *kubemootv1alpha1.CrewFitnessSuite, key string, data []byte) (IterationResult, bool) {
	idx, iter, ok := parseTranscriptKey(key)
	if !ok {
		return IterationResult{}, false
	}
	scenario := fmt.Sprintf("script-%d", idx)
	if idx >= 0 && idx < len(suite.Spec.Scripts) {
		scenario = suite.Spec.Scripts[idx].TestRef
	}

	var doc transcriptDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return IterationResult{}, false
	}
	passed, total, asserts := tallyAssertions(doc.Assertions)
	phase := phaseFromTally(passed, total)
	started, _ := time.Parse(time.RFC3339, doc.StartedAt)

	consensusOK, expectedAgrees := consensusGate(doc.Assertions)
	// Participation is the TRUE per-agent agree depth from the persisted signal
	// stream (the gateway emits agree findings; the runner captures them), scored
	// against the scenario's expected N. This is an independent measure, no longer
	// a mirror of the consensus floor, so it now carries weight (see
	// defaultRubricWeights). See [[Persist Consensus Signals in Transcripts]].
	realAgrees := agreeCountFromEvents(doc.Events)

	return IterationResult{
		Scenario:         scenario,
		Iteration:        int32(iter),
		StartedAt:        started,
		DurationMs:       doc.DurationMs,
		Phase:            phase,
		AssertionsPassed: passed,
		AssertionsTotal:  total,
		Assertions:       asserts,
		// Objective measures from the event stream (zero when no events present).
		// Factuality + fabrication are computed in the scenario aggregator directly
		// from Assertions + Synthesis, so no per-run sentinel field can be forgotten.
		Synthesis:     synthesisFromEvents(doc.Events),
		Question:      doc.Question,
		Correctness:   correctnessScore(passed, total),
		Adherence:     adherenceScore(doc.Events),
		Efficiency:    efficiencyScore(doc.DurationMs),
		ConsensusOK:   consensusOK,
		Participation: participationScore(realAgrees, expectedAgrees),
		Selectivity:   selectivityScore(doc.Events),
	}, true
}

// objectStore is the slice of *nats.Publisher the report and artifact-purge
// paths need (so tests can substitute a fake).
type objectStore interface {
	ListObjects(bucket, prefix string) ([]string, error)
	GetObject(bucket, key string) ([]byte, error)
	PutObject(bucket, key string, data []byte, ttl time.Duration) (*nats.ObjectInfo, error)
	DeleteObject(bucket, key string) error
}

// consistencySidecarKey names the per-run cached semantic-consistency scores so
// only the first report generation embeds; the version suffix invalidates the
// cache if the scoring format changes.
func consistencySidecarKey(prefix string) string { return prefix + "consistency-v1.json" }

// resolveEmbedder returns an embedder built from the first ready EmbeddingModel
// in the cluster, or nil when none is available (caller falls back to lexical).
func resolveEmbedder(ctx context.Context, c client.Client) embedder {
	var list kubemootv1alpha1.EmbeddingModelList
	if err := c.List(ctx, &list); err != nil {
		return nil
	}
	for i := range list.Items {
		em := &list.Items[i]
		if em.Status.Ready && em.Status.Endpoint != "" {
			return newOllamaEmbedder(em.Status.Endpoint, em.Spec.Model)
		}
	}
	return nil
}

// scenarioConsistency returns per-scenario semantic self-consistency (0-100),
// cached in a per-run sidecar. Returns nil when no embedder is reachable or any
// embedding fails — the report then uses the lexical fallback for every scenario
// (one method per report, never a mix). A successful semantic computation is
// cached; a fallback is not (so a later run with a healthy embedder upgrades it).
func scenarioConsistency(ctx context.Context, c client.Client, store objectStore, prefix string, scenarioTexts map[string][]string) map[string]float64 {
	key := consistencySidecarKey(prefix)
	if data, err := store.GetObject(FitnessArtifactsBucket, key); err == nil && data != nil {
		var cached map[string]float64
		if json.Unmarshal(data, &cached) == nil && coversScenarios(cached, scenarioTexts) {
			return cached
		}
	}
	emb := resolveEmbedder(ctx, c)
	if emb == nil {
		return nil
	}
	out := make(map[string]float64, len(scenarioTexts))
	for scenario, texts := range scenarioTexts {
		v, err := semanticConsistency(ctx, texts, emb)
		if err != nil {
			reportLog.Info("semantic consistency unavailable, falling back to lexical", "scenario", scenario, "err", err.Error())
			return nil
		}
		out[scenario] = v
	}
	if blob, err := json.Marshal(out); err == nil {
		if _, err := store.PutObject(FitnessArtifactsBucket, key, blob, 720*time.Hour); err != nil {
			reportLog.Info("could not cache consistency scores", "err", err.Error())
		}
	}
	return out
}

// coversScenarios is true when every scenario with texts has a cached score.
func coversScenarios(cached map[string]float64, scenarioTexts map[string][]string) bool {
	for s := range scenarioTexts {
		if _, ok := cached[s]; !ok {
			return false
		}
	}
	return true
}

// Answer-quality judging lives in the DEFER post-suite engine
// (fitness_deferred_judge.go): keyword-resolved crews score synthesis against the
// scenario's reference and cache per-scenario scores at suite completion. The
// report reads that cache (readDeferredScores) — it no longer runs any judge inline
// or on download.

// GenerateSuiteReport builds the fitness XLSX for a suite ON DEMAND from its
// transcripts — no stored artifact involved.
// resolveCrewVersion reads the crew Helm chart version (the kubemoot.ai/crew-version
// label the crew chart stamps on every CR) from the Crew CR named crewRef in
// namespace. Returns "" if the crew is absent or carries no version (e.g. a
// hand-applied / kmctl-scaffolded crew) - provenance is best-effort and never
// blocks the artifact.
func resolveCrewVersion(ctx context.Context, c client.Reader, namespace, crewRef string) string {
	if crewRef == "" {
		return ""
	}
	crew := &kubemootv1alpha1.Crew{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: crewRef}, crew); err != nil {
		return ""
	}
	return crew.Labels[crewVersionLabel]
}

// stampCrewVersion enriches the in-memory suite with the crew chart version (from
// the Crew CR) so the artifact's Overview tab can record run provenance. In-memory
// only - never persisted back to the CR.
func stampCrewVersion(ctx context.Context, c client.Reader, suite *kubemootv1alpha1.CrewFitnessSuite) {
	if suite.Labels[crewVersionLabel] != "" {
		return
	}
	if v := resolveCrewVersion(ctx, c, suite.Namespace, suite.Spec.CrewRef); v != "" {
		if suite.Labels == nil {
			suite.Labels = map[string]string{}
		}
		suite.Labels[crewVersionLabel] = v
	}
}

// readTranscriptResults reads the suite's per-iteration transcript objects
// concurrently and maps each to an IterationResult (unsorted).
//
// On-demand generation is dominated by hundreds of small sequential object GETs
// (a full N=10/15 suite is ~360-405 reads); a bounded fan-out cuts that from
// minutes to seconds so the report stays freshly generated by the live code AND
// completes within the download timeout — no cached blob (which would freeze old
// runs against generator improvements).
func readTranscriptResults(suite *kubemootv1alpha1.CrewFitnessSuite, store objectStore, keys []string) []IterationResult {
	jsonKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.HasSuffix(key, ".json") {
			jsonKeys = append(jsonKeys, key) // skip the legacy .xlsx artifact / non-transcripts
		}
	}
	results := make([]IterationResult, 0, len(jsonKeys))
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 16) // bound concurrent object-store reads
	)
	for _, key := range jsonKeys {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, gErr := store.GetObject(FitnessArtifactsBucket, key)
			if gErr != nil || data == nil {
				return
			}
			if ir, ok := iterationFromTranscript(suite, key, data); ok {
				mu.Lock()
				results = append(results, ir)
				mu.Unlock()
			}
		}(key)
	}
	wg.Wait()
	return results
}

func GenerateSuiteReport(ctx context.Context, c client.Client, store objectStore, namespace, name string) ([]byte, error) {
	var suite kubemootv1alpha1.CrewFitnessSuite
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &suite); err != nil {
		return nil, fmt.Errorf("get suite %s/%s: %w", namespace, name, err)
	}
	return buildSuiteReport(ctx, c, store, &suite)
}

// buildSuiteReport builds the XLSX for one suite run from its transcripts and
// its deferred-judge checkpoint. The suite is enriched in memory only.
func buildSuiteReport(ctx context.Context, c client.Client, store objectStore, suite *kubemootv1alpha1.CrewFitnessSuite) ([]byte, error) {
	// Provenance: enrich the in-memory suite with the crew chart version so the
	// Overview tab attributes this run to a specific crew version.
	stampCrewVersion(ctx, c, suite)
	prefix := suiteRunPrefix(suite.Namespace, suite.Name, suite.Status.RunID)
	keys, err := store.ListObjects(FitnessArtifactsBucket, prefix)
	if err != nil {
		return nil, fmt.Errorf("list transcripts: %w", err)
	}
	results := readTranscriptResults(suite, store, keys)
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Scenario != results[j].Scenario {
			return results[i].Scenario < results[j].Scenario
		}
		return results[i].Iteration < results[j].Iteration
	})

	// Semantic self-consistency (embedding-based) with a per-run cache; nil ->
	// the builder uses the lexical fallback per scenario.
	scenarioTexts := map[string][]string{}
	for _, r := range results {
		if strings.TrimSpace(r.Synthesis) == "" {
			continue
		}
		scenarioTexts[r.Scenario] = append(scenarioTexts[r.Scenario], r.Synthesis)
	}
	consistency := scenarioConsistency(ctx, c, store, prefix, scenarioTexts)
	// Answer quality comes from the DEFER post-suite judging pass (crew-scored vs
	// the scenario's reference), cached at completion; empty until it finishes.
	quality := readDeferredScores(store, prefix)
	return BuildFitnessSuiteXLSXWithMeasures(suite, results, consistency, quality)
}

// ReportServer is a manager Runnable that serves on-demand fitness reports.
type ReportServer struct {
	Client    client.Client
	Publisher *kubemootnats.Publisher
	Addr      string
}

// ComponentStatus is one control-plane component's health, in the spirit of
// kubectl's (deprecated) componentstatuses but reported by the operator itself —
// derived from state the operator genuinely owns (its own informers, its NATS
// connection, ModelProvider CR status), never by dialing hardcoded ports. The
// cardinal rule: never a false negative — a check the operator cannot make
// reliably is reported neutrally, not as unhealthy.
type ComponentStatus struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

// buildComponentStatuses composes the control-plane health from sources the
// operator owns. natsConnected is passed in (rather than reaching into the
// Publisher) so the logic is unit-testable with a fake client.
func buildComponentStatuses(ctx context.Context, c client.Client, natsConnected bool) []ComponentStatus {
	// The operator is answering this request, so it is by definition up.
	out := []ComponentStatus{{Name: "operator", Healthy: true, Message: "ok"}}

	// NATS/JetStream — the discussion + artifact backbone. The operator holds a
	// live connection, so it reports reachability without probing ports.
	if natsConnected {
		out = append(out, ComponentStatus{Name: "nats", Healthy: true, Message: "connected"})
	} else {
		out = append(out, ComponentStatus{Name: "nats", Healthy: false, Message: "not reachable"})
	}

	// Model providers — the inference backends the scheduler targets. Readiness
	// comes straight from the CR status the operator maintains.
	var mps kubemootv1alpha1.ModelProviderList
	switch err := c.List(ctx, &mps); {
	case err != nil:
		out = append(out, ComponentStatus{Name: "modelproviders", Healthy: false, Message: "list failed: " + err.Error()})
	case len(mps.Items) == 0:
		// No providers configured is a config state, not a failure — neutral.
		out = append(out, ComponentStatus{Name: "modelproviders", Healthy: true, Message: "none configured"})
	default:
		ready := 0
		for i := range mps.Items {
			if mps.Items[i].Status.Ready {
				ready++
			}
		}
		out = append(out, ComponentStatus{
			Name:    "modelproviders",
			Healthy: ready > 0,
			Message: fmt.Sprintf("%d/%d ready", ready, len(mps.Items)),
		})
	}
	return out
}

// Start implements manager.Runnable.
func (s *ReportServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /componentstatuses", func(w http.ResponseWriter, r *http.Request) {
		natsConnected := s.Publisher != nil && s.Publisher.Connected()
		statuses := buildComponentStatuses(r.Context(), s.Client, natsConnected)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(statuses)
	})
	mux.HandleFunc("GET /report/{namespace}/{name}", func(w http.ResponseWriter, r *http.Request) {
		ns, name := r.PathValue("namespace"), r.PathValue("name")
		data, err := GenerateSuiteReport(r.Context(), s.Client, s.Publisher, ns, name)
		if err != nil {
			reportLog.Error(err, "report generation failed", "namespace", ns, "name", name)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.xlsx", name))
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})

	srv := &http.Server{Addr: s.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	reportLog.Info("Fitness report server listening", "addr", s.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
