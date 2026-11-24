/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_path_coverage.go counts a suite run's model calls along the paths a call can
// take (kind, phase, model, provider, thinking) from the placement records the run's
// own transcripts hold, and names the paths the crew has that the run never took. A
// short tier such as smoke exists to exercise every path, so the table is how a gap
// is seen instead of assumed.
package controller

import (
	"context"
	"fmt"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

// Thinking settings a call is counted under.
const (
	thinkingOn      = "on"
	thinkingOff     = "off"
	thinkingDefault = "default"
	thinkingUnknown = "unknown"

	coordinatorAgentName = "coordinator"
	pathUnknown          = "unknown"
)

// The call kinds and phases the placement contract defines (placement-call.json).
var (
	pathKinds  = []string{"triage", "tool-turn", "simple"}
	pathPhases = []string{"advisory", "selection", "triage", "evaluation", "synthesis"}
)

// pathContext is what the run's calls are joined with: the thinking setting of each
// agent, the engine type of each model server, and the thinking settings and engines
// the crew declares, which a complete run would have exercised.
type pathContext struct {
	thinking  map[string]string // agent name -> on, off or default
	providers map[string]string // model server name -> engine type
	coord     string            // the coordinator agent's name
	declared  pathDeclared
}

// pathDeclared lists the paths the crew has, to name the ones the run missed.
type pathDeclared struct {
	providers []string
	thinking  []string
}

// callsFromEvents are the model calls the receipt of a transcript's done event lists.
func callsFromEvents(events []transcriptEvent) []fitnessscript.PlacedCall {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Receipt != nil {
			return events[i].Receipt.Calls
		}
	}
	return nil
}

// pathTally counts the paths of a set of calls.
type pathTally struct {
	kinds, phases, models, providers, thinking map[string]int32
	calls, transcripts                         int32
}

func newPathTally() *pathTally {
	return &pathTally{
		kinds: map[string]int32{}, phases: map[string]int32{}, models: map[string]int32{},
		providers: map[string]int32{}, thinking: map[string]int32{},
	}
}

// add counts one transcript's calls.
func (t *pathTally) add(calls []fitnessscript.PlacedCall, pc pathContext) {
	if len(calls) == 0 {
		return
	}
	t.transcripts++
	for _, c := range calls {
		t.calls++
		t.kinds[orUnknown(c.Kind)]++
		t.phases[orUnknown(c.Phase)]++
		t.models[orUnknown(c.Model)]++
		t.providers[pc.providerOf(c.Server)]++
		t.thinking[pc.thinkingOf(c.Agent)]++
	}
}

func orUnknown(s string) string {
	if s == "" {
		return pathUnknown
	}
	return s
}

// providerOf is the engine type of a model server, unknown when it is not in the realm.
func (pc pathContext) providerOf(server string) string {
	if p, ok := pc.providers[server]; ok && p != "" {
		return p
	}
	return pathUnknown
}

// thinkingOf is the thinking setting of the agent that made a call. The coordinator's
// own calls may carry its role name instead of its agent name.
func (pc pathContext) thinkingOf(agent string) string {
	if v, ok := pc.thinking[agent]; ok {
		return v
	}
	if (agent == "" || agent == coordinatorAgentName) && pc.coord != "" {
		if v, ok := pc.thinking[pc.coord]; ok {
			return v
		}
	}
	return thinkingUnknown
}

// pathRows turns counts into a table, most calls first, capped at MaxPathCoverageRows.
func pathRows(m map[string]int32) []kubemootv1alpha1.PathCount {
	out := make([]kubemootv1alpha1.PathCount, 0, len(m))
	for name, n := range m {
		out = append(out, kubemootv1alpha1.PathCount{Name: statusName(name), Calls: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > kubemootv1alpha1.MaxPathCoverageRows {
		out = out[:kubemootv1alpha1.MaxPathCoverageRows]
	}
	return out
}

// missing returns the wanted names with no calls.
func missing(wanted []string, seen map[string]int32) []string {
	var out []string
	for _, w := range wanted {
		if seen[w] == 0 {
			out = append(out, w)
		}
	}
	return out
}

// gaps names what the crew has that the run never took.
func (t *pathTally) gaps(d pathDeclared) []string {
	if t.calls == 0 {
		return []string{"no placed model call in the run's transcripts: nothing to cover"}
	}
	var out []string
	for _, k := range missing(pathKinds, t.kinds) {
		out = append(out, fmt.Sprintf("no %s call", k))
	}
	for _, p := range missing(pathPhases, t.phases) {
		out = append(out, fmt.Sprintf("no call in the %s phase", p))
	}
	for _, p := range missing(d.providers, t.providers) {
		out = append(out, fmt.Sprintf("no call on provider %s", p))
	}
	for _, th := range missing(d.thinking, t.thinking) {
		out = append(out, fmt.Sprintf("no call with thinking %s", th))
	}
	if len(out) > kubemootv1alpha1.MaxPathCoverageRows {
		out = out[:kubemootv1alpha1.MaxPathCoverageRows]
	}
	return out
}

// buildPathCoverage counts the calls of every result. Its rows are empty and its
// gaps say so when the transcripts held no call.
func buildPathCoverage(results []IterationResult, pc pathContext) *kubemootv1alpha1.PathCoverage {
	t := newPathTally()
	for i := range results {
		t.add(results[i].Calls, pc)
	}
	return &kubemootv1alpha1.PathCoverage{
		Calls:       t.calls,
		Transcripts: t.transcripts,
		Kinds:       pathRows(t.kinds),
		Phases:      pathRows(t.phases),
		Models:      pathRows(t.models),
		Providers:   pathRows(t.providers),
		Thinking:    pathRows(t.thinking),
		Gaps:        t.gaps(pc.declared),
	}
}

// thinkingSetting is how an Agent's spec.think is counted.
func thinkingSetting(think *bool) string {
	switch {
	case think == nil:
		return thinkingDefault
	case *think:
		return thinkingOn
	default:
		return thinkingOff
	}
}

// loadPathContext joins the run with the cluster: the crew's agents (thinking), the
// coordinator's name, and the model servers the realm recorded (provider). A read that
// fails leaves that join empty, so its calls count as unknown rather than the table
// being withheld. The agents and the coordinator are read when the table is written,
// at the end of the run, not when it started; an agent changed during the run is
// counted by its last setting.
func (r *CrewFitnessSuiteReconciler) loadPathContext(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) pathContext {
	pc := pathContext{thinking: map[string]string{}, providers: map[string]string{}}
	r.loadAgentThinking(ctx, suite, &pc)
	var crew kubemootv1alpha1.Crew
	if err := r.Get(ctx, client.ObjectKey{Namespace: suite.Namespace, Name: suite.Spec.CrewRef}, &crew); err == nil {
		pc.coord = crew.Status.CoordinatorRef
	}
	loadRealmProviders(suite, &pc)
	return pc
}

// loadAgentThinking records the thinking setting of each of the crew's agents, and the
// on and off settings the crew declares.
func (r *CrewFitnessSuiteReconciler) loadAgentThinking(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite, pc *pathContext) {
	var agents kubemootv1alpha1.AgentList
	if err := r.List(ctx, &agents, client.InNamespace(suite.Namespace), client.MatchingLabels{labelCrew: suite.Spec.CrewRef}); err != nil {
		return
	}
	seen := map[string]bool{}
	for i := range agents.Items {
		th := thinkingSetting(agents.Items[i].Spec.Think)
		pc.thinking[agents.Items[i].Name] = th
		if th != thinkingDefault && !seen[th] {
			seen[th] = true
			pc.declared.thinking = append(pc.declared.thinking, th)
		}
	}
	sort.Strings(pc.declared.thinking)
}

// loadRealmProviders records the engine type of each model server the run's realm
// listed, and the engine types the crew can reach.
func loadRealmProviders(suite *kubemootv1alpha1.CrewFitnessSuite, pc *pathContext) {
	realm := suite.Status.Realm
	if realm == nil || realm.Start == nil {
		return
	}
	seen := map[string]bool{}
	for _, ms := range realm.Start.ModelServers {
		pc.providers[ms.Name] = ms.Engine
		if ms.Engine != "" && !seen[ms.Engine] {
			seen[ms.Engine] = true
			pc.declared.providers = append(pc.declared.providers, ms.Engine)
		}
	}
	sort.Strings(pc.declared.providers)
}

// recordTranscriptStatus reads the run's transcripts once and sets the two things only
// they hold: status.scenarios[].inventedNameRuns and status.pathCoverage. Without a
// store, a failed listing, or a transcript that could not be read, the status keeps
// what it holds: a transient failure must not lower a correct count or replace a table
// with an empty one.
func (r *CrewFitnessSuiteReconciler) recordTranscriptStatus(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) {
	results, ok := readRunResults(r.store(), suite)
	if !ok {
		return
	}
	setInventedCounts(suite, results)
	suite.Status.PathCoverage = buildPathCoverage(results, r.loadPathContext(ctx, suite))
}

// readRunResults reads every transcript of the suite's current run. ok is false when
// there is no store or run, the listing failed, or a transcript could not be read.
func readRunResults(store objectStore, suite *kubemootv1alpha1.CrewFitnessSuite) ([]IterationResult, bool) {
	if store == nil || suite.Status.RunID == "" || len(suite.Status.Scenarios) == 0 {
		return nil, false
	}
	keys, err := store.ListObjects(FitnessArtifactsBucket, suiteRunPrefix(suite.Namespace, suite.Name, suite.Status.RunID))
	if err != nil {
		return nil, false
	}
	results := readTranscriptResults(suite, store, keys)
	if len(results) < countTranscriptKeys(keys) {
		return nil, false
	}
	return results, true
}
