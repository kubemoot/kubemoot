/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller implements the Agent reconciler.
//
// Per kubemoot/docs/scheduler.md, Agents declare what they can do via
// spec.capabilities and PromptModule references. Models declare what they
// are via labels. An optional CrewSchedulingPolicy expresses require/prefer
// rules with Kubernetes-style label selectors.
//
// This reconciler walks the lifecycle for each Agent CR:
//
//  1. Resolve the CrewSchedulingPolicy for the agent's crew (if any).
//  2. For each phase (mulling, triage), filter Models against the policy's
//     require rules and Model.vramMib vs ModelProvider capacity. Score the
//     feasible set against the prefer rules and pick the highest.
//  3. Build a policy ConfigMap (system.txt assembled from PromptModule refs)
//     and a Deployment with KUBEMOOT_* env vars pointing at the chosen
//     (model, provider, endpoint).
//  4. Update Agent.status with readiness, scheduling decision, and
//     observed dependencies.
package controller

import (
	"context"
	"fmt"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	"github.com/javajon/kubemoot/operator/internal/crewscope"
	kubemootnats "github.com/javajon/kubemoot/operator/internal/nats"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// AgentReconciler reconciles Agent objects.
type AgentReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ConfigCache *ConfigCache
	// NATSPublisher publishes crew resumes to NATS KV for embedding-based triage
	// selection (resume_sync.go). Nil in tests / when NATS is unconfigured — the
	// resume sync is a no-op and the agent-runtime degrades to keyword selection.
	NATSPublisher *kubemootnats.Publisher
}

// Phase names — the consent-3 archetype's phases. CrewSchedulingPolicy
// rule.phase strings are matched against these.
const (
	phaseMulling = "mulling"
	phaseTriage  = "triage"
)

// requeueUnscheduledInterval is how often the reconciler retries when no
// feasible Model exists yet — typically resolved when a ModelProvider
// becomes Ready or a Model CR is created.
const requeueUnscheduledInterval = 30 * time.Second

// imageLocalityBonus is added to a candidate's score when the model is
// already loaded on the provider (mirrors kube-scheduler's image-locality).
const imageLocalityBonus = 10

// agentLoadPenaltyPerAgent is subtracted from a candidate's score for each
// agent already bound to the provider (per phase). Drives load-aware
// bin-packing.
// Tunable; revisit if provider weights spread changes meaningfully.
const agentLoadPenaltyPerAgent = 10

// stickyHysteresis is the score margin an ALTERNATIVE provider must beat
// the agent's CURRENT pick by, before pickModel flips the binding. Without
// this, cache lag in the ModelProvider counts produces flip-back
// oscillation: many agents reconcile against stale counts, all migrate to
// the under-loaded provider, MP counts catch up, NEXT reconcile sees the
// other side under-loaded, they migrate back. Hysteresis breaks the loop —
// once an agent is on a provider, only a meaningfully-better alternative
// dislodges it. 25 ≈ 2.5× agentLoadPenaltyPerAgent, requiring roughly
// "3 agents' worth of load relief" to justify a flip. Has no effect on
// fresh picks for agents with no current binding.
const stickyHysteresis = 25

// scoreCandidate computes the score and human-readable reason tokens for a
// (model, provider) candidate under a given phase rule. It composes four
// sources: label-prefer weights, image-locality, provider weight, and (when
// applyLoadPenalty=true) a per-phase agent-count penalty that drives
// bin-packing across providers.
//
// applyLoadPenalty=false is for "critical-path" agents (currently:
// coordinators) whose work runs sequentially BEFORE the rest of the crew
// wakes up. For them, bin-packing is the wrong objective — there's no
// parallel load to balance, only end-to-end latency on a blocker, so they
// always pick the heaviest-weight provider their Model rules permit.
// Penalty applies to toolers, who run in parallel after triage and
// benefit from being spread across providers.
func scoreCandidate(phase string, rule *kubemootv1alpha1.SchedulingRule, m *kubemootv1alpha1.Model, prov *kubemootv1alpha1.ModelProvider, applyLoadPenalty bool) (int64, []string) {
	var score int64
	var reasonParts []string

	preferScore, preferReasons := preferRuleScore(rule, m)
	score += preferScore
	reasonParts = append(reasonParts, preferReasons...)

	if localityScore, ok := imageLocalityScore(m, prov); ok {
		score += localityScore
		reasonParts = append(reasonParts, fmt.Sprintf("locality+%d", localityScore))
	}
	if ps := providerScore(phase, prov); ps != 0 {
		score += ps
		reasonParts = append(reasonParts, fmt.Sprintf("provider%+d", ps))
	}
	if applyLoadPenalty {
		if penalty, ok := loadPenalty(phase, prov); ok {
			score -= penalty
			reasonParts = append(reasonParts, fmt.Sprintf("load-%d", penalty))
		}
	}
	return score, reasonParts
}

// preferRuleScore sums the weights of every Prefer selector in rule that matches
// the Model's labels, returning the total and a reason fragment per match. A nil
// rule, a nil selector, or an unparsable selector contributes nothing.
func preferRuleScore(rule *kubemootv1alpha1.SchedulingRule, m *kubemootv1alpha1.Model) (int64, []string) {
	if rule == nil {
		return 0, nil
	}
	var score int64
	var reasonParts []string
	for _, pref := range rule.Prefer {
		if pref.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pref.Selector)
		if err != nil {
			continue
		}
		if sel.Matches(labels.Set(m.Labels)) {
			score += int64(pref.Weight)
			reasonParts = append(reasonParts, fmt.Sprintf("prefer+%d", pref.Weight))
		}
	}
	return score, reasonParts
}

// imageLocalityScore returns the locality bonus (and ok=true) when the provider
// already has the candidate Model loaded, so reusing it avoids a model pull/load.
func imageLocalityScore(m *kubemootv1alpha1.Model, prov *kubemootv1alpha1.ModelProvider) (int64, bool) {
	if prov == nil || prov.Status.Capacity == nil {
		return 0, false
	}
	for _, lm := range prov.Status.Capacity.LoadedModels {
		if lm.Name == m.Spec.Model {
			return imageLocalityBonus, true
		}
	}
	return 0, false
}

// loadPenalty computes the load-aware bin-pack penalty for a provider in a phase:
// the per-phase agent count, scaled down by the provider's MaxParallel capacity.
// ok=false when there is no capacity info or no agents in this phase (no penalty).
//
// Load-aware bin-packing penalizes providers that already host many agents IN THIS
// PHASE. Per-phase counts (Mulling/TriageAgentCount) are populated by
// ModelProviderReconciler.countAssignedAgents from the per-phase deployment labels.
// Using a phase-specific count is critical: a single AgentCount totalling both
// phases grows in lockstep on both providers (each agent contributes once to each),
// so the penalties cancel and the bin-pack signal disappears.
func loadPenalty(phase string, prov *kubemootv1alpha1.ModelProvider) (int64, bool) {
	if prov == nil || prov.Status.Capacity == nil {
		return 0, false
	}
	var phaseCount int
	switch phase {
	case phaseMulling:
		phaseCount = prov.Status.Capacity.MullingAgentCount
	case phaseTriage:
		phaseCount = prov.Status.Capacity.TriageAgentCount
	}
	if phaseCount <= 0 {
		return 0, false
	}
	// Capacity-aware penalty: divide by MaxParallel (discovered from
	// OLLAMA_NUM_PARALLEL on the provider pod). A provider with
	// num_parallel=2 has 2x the concurrent-inference capacity of one
	// at num_parallel=1, so each agent assigned costs half as much
	// "saturation." Without this normalization the scheduler treats
	// a 5090@num_parallel=2 the same as a 4090@num_parallel=1,
	// pushing agents off the higher-capacity provider too eagerly.
	// Equilibrium under uniform penalty was ~10/16 across rig0/rig1
	// for 26 agents; with capacity-aware penalty it shifts to ~21/5,
	// biasing work toward the warmer, more-parallel GPU.
	parallel := prov.Status.Capacity.MaxParallel
	if parallel < 1 {
		parallel = 1 // defensive: pre-discovery providers default to 1
	}
	return int64(phaseCount) * agentLoadPenaltyPerAgent / int64(parallel), true
}

// isCoordinator reports whether an agent is its crew's coordinator: declared
// with discussRole coordinator, or with the kubemoot.ai/role=coordinator label
// that crews created before discussRole existed still carry. Every controller
// that needs the coordinator uses this one rule.
func isCoordinator(agent *kubemootv1alpha1.Agent) bool {
	return agent.Spec.DiscussRole == roleCoordinator || agent.Labels[annoRole] == roleCoordinator
}

// shouldBinPack returns true when the agent's pickModel run should apply
// the load-aware bin-pack penalty. Coordinators skip it because their work
// is sequential and there's no parallel load to balance; latency dominates
// and they should always pick the heaviest provider. Every other agent
// bin-packs.
func shouldBinPack(agent *kubemootv1alpha1.Agent) bool {
	if agent == nil {
		return true
	}
	return !isCoordinator(agent)
}

// providerScore returns the phase-aware contribution from a ModelProvider's
// scheduling weight. Mulling adds the weight (favoring heavy providers like
// a 5090 with weight=100); triage subtracts it (favoring lighter providers
// like a 4090 with weight=25). Without this term, providers carrying
// identically-labeled Models tie on the prefer rules and the alphabetical
// Model-name tiebreaker collapses every binding onto a single provider.
func providerScore(phase string, prov *kubemootv1alpha1.ModelProvider) int64 {
	if prov == nil || prov.Spec.Scheduling == nil {
		return 0
	}
	w := int64(prov.Spec.Scheduling.Weight)
	switch phase {
	case phaseMulling:
		return w
	case phaseTriage:
		return -w
	default:
		return 0
	}
}

const (
	defaultAgentRuntimeImage = "ghcr.io/kubemoot/agent-runtime:latest"
	defaultNATSURL           = "nats://nats.nats.svc.cluster.local:4222"
)

// +kubebuilder:rbac:groups=kubemoot.ai,resources=agents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=agents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=agents/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=models,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=modelproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=crewschedulingpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mootarchetypes,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=promptmodules,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=ragsources,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpgateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	agent := &kubemootv1alpha1.Agent{}
	if err := r.Get(ctx, req.NamespacedName, agent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Read existing Deployment labels to know the agent's current per-phase
	// provider picks. pickModel uses these for sticky scheduling so that
	// cache-lag during bulk reconciles doesn't produce flip-back oscillation.
	currentMulling, currentTriage := r.currentProviderPicks(ctx, agent)

	mullingPick, mullingErr := r.pickModel(ctx, agent, phaseMulling, currentMulling)
	triagePick, triageErr := r.pickModel(ctx, agent, phaseTriage, currentTriage)
	if triageErr != nil && mullingPick != nil {
		log.V(1).Info("Triage phase unschedulable; falling back to mulling pick", "reason", triageErr.Error())
		triagePick = mullingPick
	}
	if mullingPick == nil {
		return r.markUnschedulable(ctx, agent, mullingErr)
	}

	policyCMName := agent.Name + "-policy"
	if err := r.ensurePolicyConfigMap(ctx, agent, policyCMName); err != nil {
		log.Error(err, "Failed to ensure policy ConfigMap")
	}

	// Publish the crew's agent resumes for embedding-based triage selection
	// (coordinator only; hash-gated). Non-fatal — the agent-runtime degrades to
	// keyword self-selection if resumes/search are unavailable. See resume_sync.go.
	r.syncCrewResumes(ctx, agent)

	if err := r.ensureDeployment(ctx, agent, mullingPick, triagePick, policyCMName); err != nil {
		log.Error(err, "Failed to ensure Deployment")
		return ctrl.Result{}, err
	}
	if err := r.ensureService(ctx, agent); err != nil {
		log.Error(err, "Failed to ensure Service")
		return ctrl.Result{}, err
	}
	if err := r.refreshStatus(ctx, agent, mullingPick, triagePick); err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to refresh Agent status")
	}
	return ctrl.Result{}, nil
}

// markUnschedulable sets the Agent's status to Unschedulable (using mullingErr's
// message when present) and requeues for a later retry once a feasible Model or
// Ready ModelProvider appears.
func (r *AgentReconciler) markUnschedulable(ctx context.Context, agent *kubemootv1alpha1.Agent, mullingErr error) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	agent.Status.Phase = "Unschedulable"
	agent.Status.Ready = false
	if mullingErr != nil {
		agent.Status.Message = mullingErr.Error()
	} else {
		agent.Status.Message = "no feasible Model for mulling phase"
	}
	if err := r.Status().Update(ctx, agent); err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to update Agent status to Unschedulable")
	}
	return ctrl.Result{RequeueAfter: requeueUnscheduledInterval}, nil
}

// modelPick records the resolved (Model, ModelProvider) for one phase.
type modelPick struct {
	ModelID  string
	Endpoint string
	Reason   string
	Model    *kubemootv1alpha1.Model
	Provider *kubemootv1alpha1.ModelProvider
	// Candidates is the phase's ranked candidate list (ModelID first), handed to
	// the runtime so its per-call pick can use a warm in-tolerance model.
	Candidates []modelCandidate
}

// currentProviderPicks returns the mulling/triage provider names recorded
// on the agent's existing Deployment labels (the operator's prior decision).
// Empty strings on first-deploy (no existing Deployment) — pickModel
// degrades to a normal best-score pick in that case.
func (r *AgentReconciler) currentProviderPicks(ctx context.Context, agent *kubemootv1alpha1.Agent) (mulling, triage string) {
	existing := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, existing); err != nil {
		return "", ""
	}
	return existing.Labels["kubemoot.ai/mulling-provider"], existing.Labels["kubemoot.ai/triage-provider"]
}

// scheduleCandidate is one feasible (Model, ModelProvider) pairing under
// consideration during pickModel scoring. Exported at package scope (vs.
// originally being local to pickModel) so the sticky-scheduling helper
// applySticky can take it as input and be unit-tested without spinning up
// a fake client + scheme.
type scheduleCandidate struct {
	model    *kubemootv1alpha1.Model
	provider *kubemootv1alpha1.ModelProvider
	score    int64
	reason   string
}

// candidateBefore orders feasible candidates: the higher score first; on a tie the
// Model with the smaller declared VRAM footprint (faster, and it leaves the GPU more
// room), a Model that declares none after those that do; then the Model name, so
// the order is deterministic. Without the footprint step a tie was settled by name,
// so whether a tooler landed on the small or the large Model depended on how the
// Models happened to be spelled.
func candidateBefore(a, b scheduleCandidate) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	av, bv := a.model.Spec.VRAMMib, b.model.Spec.VRAMMib
	if av != bv {
		if av == 0 || bv == 0 {
			return bv == 0
		}
		return av < bv
	}
	return a.model.Name < b.model.Name
}

// applySticky implements anti-oscillation: when the best-scoring candidate
// uses a DIFFERENT provider than the agent's current binding, keep the
// current binding unless the alternative beats it by more than
// stickyHysteresis points. feasible MUST be sorted descending by score;
// best is feasible[0]. Returns the candidate to actually use. When
// currentProviderName is empty or no candidate matches it, returns best
// unchanged (fresh-pick path for new agents).
func applySticky(best scheduleCandidate, feasible []scheduleCandidate, currentProviderName string, hysteresis int64) scheduleCandidate {
	if currentProviderName == "" || best.provider.Name == currentProviderName {
		return best
	}
	for _, c := range feasible {
		if c.provider.Name != currentProviderName {
			continue
		}
		if best.score-c.score <= hysteresis {
			c.reason = c.reason + ",sticky"
			return c
		}
		break
	}
	return best
}

// pickModel runs filter+score for one phase and returns the best feasible pick.
// currentProviderName, if non-empty, is the provider this agent is CURRENTLY
// bound to for this phase. When the highest-scoring candidate uses a different
// provider, pickModel applies applySticky to keep the existing binding unless
// the alternative beats it by stickyHysteresis points, preventing
// reconcile-cycle oscillation under MP-cache lag.
func (r *AgentReconciler) pickModel(ctx context.Context, agent *kubemootv1alpha1.Agent, phase string, currentProviderName string) (*modelPick, error) {
	policy, rule := r.findPolicyAndRule(ctx, agent, phase)

	models := &kubemootv1alpha1.ModelList{}
	if err := r.List(ctx, models, client.InNamespace(agent.Namespace)); err != nil {
		return nil, fmt.Errorf("listing Models: %w", err)
	}

	bias, haveBias := phaseQualityBias(agent, policy, rule)
	feasible, err := r.feasibleCandidates(ctx, agent, phase, rule, models, bias, haveBias)
	if err != nil {
		return nil, err
	}

	if len(feasible) == 0 {
		if rule != nil && rule.Require != nil {
			return nil, fmt.Errorf("no Ready Model matches phase=%s require selectors", phase)
		}
		return nil, fmt.Errorf("no Ready Model with a Ready ModelProvider in namespace %s", agent.Namespace)
	}

	sort.SliceStable(feasible, func(i, j int) bool { return candidateBefore(feasible[i], feasible[j]) })
	pick := applySticky(feasible[0], feasible, currentProviderName, stickyHysteresis)

	reason := pick.reason
	if reason == "" {
		reason = fmt.Sprintf("first feasible Model for phase=%s", phase)
	}
	return &modelPick{
		ModelID:    pick.model.Spec.Model,
		Endpoint:   pick.provider.Spec.Endpoint,
		Reason:     reason,
		Model:      pick.model,
		Provider:   pick.provider,
		Candidates: rankedCandidates(pick.model.Spec.Model, rule, models.Items, bias, haveBias),
	}, nil
}

// feasibleCandidates filters the Models against the phase's require selector and
// VRAM capacity, resolves each Model's provider, and scores the survivors. It
// returns the scored candidate set (unsorted) for pickModel to rank. effBias
// and haveBias come from phaseQualityBias.
func (r *AgentReconciler) feasibleCandidates(ctx context.Context, agent *kubemootv1alpha1.Agent, phase string, rule *kubemootv1alpha1.SchedulingRule, models *kubemootv1alpha1.ModelList, effBias float64, haveBias bool) ([]scheduleCandidate, error) {
	var feasible []scheduleCandidate
	for i := range models.Items {
		m := &models.Items[i]
		cand, ok, err := r.evaluateCandidate(ctx, agent, phase, rule, m, effBias, haveBias)
		if err != nil {
			return nil, err
		}
		if ok {
			feasible = append(feasible, cand)
		}
	}
	return feasible, nil
}

// evaluateCandidate decides whether a single Model is feasible for the phase and,
// if so, returns its scored candidate. ok=false means the Model was filtered out
// (not ready, require-mismatch, no ready provider, or VRAM-too-large). A non-nil
// error is only an invalid require selector (which aborts the whole pick).
func (r *AgentReconciler) evaluateCandidate(ctx context.Context, agent *kubemootv1alpha1.Agent, phase string, rule *kubemootv1alpha1.SchedulingRule, m *kubemootv1alpha1.Model, effBias float64, haveBias bool) (scheduleCandidate, bool, error) {
	prov, feasible, err := r.candidateFeasible(ctx, agent, phase, rule, m)
	if err != nil || !feasible {
		return scheduleCandidate{}, false, err
	}
	score, reasonParts := scoreCandidate(phase, rule, m, prov, shouldBinPack(agent))
	if haveBias {
		if biasScore, biasReason := qualityBiasScore(effBias, m.Labels); biasReason != "" {
			score += biasScore
			reasonParts = append(reasonParts, biasReason)
		}
	}
	return scheduleCandidate{
		model:    m,
		provider: prov,
		score:    score,
		reason:   strings.Join(reasonParts, ","),
	}, true, nil
}

// candidateFeasible applies the feasibility gates for one (Model, phase): the
// Model must be Ready, satisfy the rule's Require selector, resolve to a Ready
// ModelProvider, and fit the provider's VRAM. Returns the resolved provider with
// feasible=true only when every gate passes; a malformed Require selector is the
// one error (it can't be silently skipped without masking a misconfiguration).
func (r *AgentReconciler) candidateFeasible(ctx context.Context, agent *kubemootv1alpha1.Agent, phase string, rule *kubemootv1alpha1.SchedulingRule, m *kubemootv1alpha1.Model) (*kubemootv1alpha1.ModelProvider, bool, error) {
	if !m.Status.Ready {
		return nil, false, nil
	}
	if rule != nil && rule.Require != nil {
		sel, err := metav1.LabelSelectorAsSelector(rule.Require)
		if err != nil {
			return nil, false, fmt.Errorf("invalid require selector for phase %s: %w", phase, err)
		}
		if !sel.Matches(labels.Set(m.Labels)) {
			return nil, false, nil
		}
	}
	prov, ok := r.resolveProvider(ctx, agent.Namespace, m.Spec.ProviderRef)
	if !ok || !prov.Status.Ready {
		return nil, false, nil
	}
	if !modelFitsProvider(m, prov) {
		return nil, false, nil
	}
	return prov, true, nil
}

// modelFitsProvider reports whether the Model's declared VRAM fits the provider's
// total VRAM. Unknown VRAM on either side (0) is treated as a fit (not enough
// information to exclude).
func modelFitsProvider(m *kubemootv1alpha1.Model, prov *kubemootv1alpha1.ModelProvider) bool {
	if m.Spec.VRAMMib > 0 && prov.Status.Capacity != nil && prov.Status.Capacity.VRAMTotalMiB > 0 {
		return int64(m.Spec.VRAMMib) <= prov.Status.Capacity.VRAMTotalMiB
	}
	return true
}

// resolveProvider looks up a ModelProvider in the agent's namespace then
// cluster-wide. Returns (provider, true) on success or (nil, false) if absent.
func (r *AgentReconciler) resolveProvider(ctx context.Context, namespace, name string) (*kubemootv1alpha1.ModelProvider, bool) {
	prov := &kubemootv1alpha1.ModelProvider{}
	// Try agent's namespace first.
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, prov); err == nil {
		return prov, true
	}
	// Then try the kubemoot system namespace.
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: "kubemoot"}, prov); err == nil {
		return prov, true
	}
	// Last try: cluster-scoped lookup with empty namespace.
	if err := r.Get(ctx, types.NamespacedName{Name: name}, prov); err == nil {
		return prov, true
	}
	return nil, false
}

// findPolicyAndRule returns the CrewSchedulingPolicy and the rule within it
// matching the phase for this agent's crew. Returns (nil, nil) if no policy
// applies or no rule for the phase. The policy is returned alongside the
// rule so callers can read policy-scoped fields (e.g. QualityBias) without
// re-listing the policy.
func (r *AgentReconciler) findPolicyAndRule(ctx context.Context, agent *kubemootv1alpha1.Agent, phase string) (*kubemootv1alpha1.CrewSchedulingPolicy, *kubemootv1alpha1.SchedulingRule) {
	crew := agent.Labels[labelCrew]
	policies := &kubemootv1alpha1.CrewSchedulingPolicyList{}
	if err := r.List(ctx, policies, client.InNamespace(agent.Namespace)); err != nil {
		return nil, nil
	}
	for i := range policies.Items {
		p := &policies.Items[i]
		if crew != "" && p.Spec.CrewRef != "" && p.Spec.CrewRef != crew {
			continue
		}
		for j := range p.Spec.Rules {
			rule := &p.Spec.Rules[j]
			if rule.Phase == phase {
				return p, rule
			}
		}
	}
	return nil, nil
}

// ensurePolicyConfigMap assembles system.txt from PromptModule refs and
// writes a ConfigMap mounted at /etc/kubemoot/policy.
func (r *AgentReconciler) ensurePolicyConfigMap(ctx context.Context, agent *kubemootv1alpha1.Agent, cmName string) error {
	systemTxt, err := r.assembleSystemPrompt(ctx, agent)
	if err != nil {
		return err
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: agent.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kubemoot-operator",
				labelAgent:                     agent.Name,
			},
		},
		Data: map[string]string{"system.txt": systemTxt},
	}
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner ref on ConfigMap: %w", err)
	}
	existing := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: agent.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if existing.Data["system.txt"] == systemTxt {
		return nil
	}
	existing.Data = desired.Data
	return r.Update(ctx, existing)
}

// assembleSystemPrompt resolves PromptModule references, sorts by Order, and
// concatenates their content into a single system prompt string.
func (r *AgentReconciler) assembleSystemPrompt(ctx context.Context, agent *kubemootv1alpha1.Agent) (string, error) {
	if len(agent.Spec.PromptRefs) == 0 {
		return "", nil
	}
	type orderedModule struct {
		order   int32
		content string
		name    string
	}
	var modules []orderedModule
	for _, ref := range agent.Spec.PromptRefs {
		pm := &kubemootv1alpha1.PromptModule{}
		if err := r.Get(ctx, types.NamespacedName{Name: ref, Namespace: agent.Namespace}, pm); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return "", err
		}
		modules = append(modules, orderedModule{
			order:   pm.Spec.Order,
			content: pm.Spec.Content,
			name:    pm.Name,
		})
	}
	sort.SliceStable(modules, func(i, j int) bool {
		if modules[i].order != modules[j].order {
			return modules[i].order < modules[j].order
		}
		return modules[i].name < modules[j].name
	})
	var sb strings.Builder
	for i, m := range modules {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(m.content)
	}
	return sb.String(), nil
}

// ensureDeployment creates or updates the agent's Deployment.
func (r *AgentReconciler) ensureDeployment(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick, policyCMName string) error {
	// Assemble the system prompt so its hash can ride in the pod template; a
	// PromptModule edit then changes the hash -> rolls the pod (agents cache the
	// prompt at startup). Same assembly function as ensurePolicyConfigMap, so the
	// hash always matches the written system.txt.
	systemTxt, err := r.assembleSystemPrompt(ctx, agent)
	if err != nil {
		return fmt.Errorf("assembling system prompt for deployment hash: %w", err)
	}
	desired := r.buildDeployment(ctx, agent, mulling, triage, policyCMName, hashString(systemTxt))
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner ref on Deployment: %w", err)
	}
	existing := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	currentHash := existing.Annotations[deploymentHashAnnotation]
	desiredHash := computeDeploymentHash(desired)
	// Independently of the spec hash, check whether Deployment.metadata.Labels
	// diverge from desired. The spec hash covers Spec.Template.Labels (those
	// match the pod selector) but NOT the top-level Deployment metadata
	// labels. countAssignedAgents in ModelProviderReconciler queries on the
	// top-level labels via label selectors — they must reflect the latest
	// scheduling decision even when no spec change otherwise warrants an
	// update.
	labelsChanged := !maps.Equal(existing.Labels, desired.Labels)
	// The spec hash covers the pod template + replicas but NOT Spec.Strategy, so a
	// coordinator switching to Recreate (see buildDeployment) must reconcile even
	// when the template is unchanged. Only fires when the operator sets an explicit
	// strategy (coordinators), so non-coordinators with a defaulted strategy are
	// never needlessly updated.
	strategyChanged := desired.Spec.Strategy.Type != "" &&
		existing.Spec.Strategy.Type != desired.Spec.Strategy.Type
	if currentHash == desiredHash && !labelsChanged && !strategyChanged {
		return nil
	}
	existing.Spec = desired.Spec
	existing.Labels = desired.Labels
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	existing.Annotations[deploymentHashAnnotation] = desiredHash
	return r.Update(ctx, existing)
}

func (r *AgentReconciler) buildDeployment(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick, policyCMName, promptHash string) *appsv1.Deployment {
	replicas, port, image, serviceAccountName := r.resolveDeploymentParams(agent)
	env := r.buildEnvVars(ctx, agent, mulling, triage, port)
	labelsMap := buildDeploymentLabels(agent, mulling, triage)

	volumeMounts := []corev1.VolumeMount{{
		Name:      "policy",
		MountPath: "/etc/kubemoot/policy",
		ReadOnly:  true,
	}}
	volumes := []corev1.Volume{{
		Name: "policy",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: policyCMName},
				Optional:             pointerTo(true),
			},
		},
	}}

	// Skills volume: crew agents get the per-crew skills ConfigMap mounted at a
	// fixed path. The mount is optional so a crew with no skills (ConfigMap not
	// yet created by SkillReconciler) never blocks pod startup. Agents with no
	// crew label have no skills ConfigMap, so skip the mount entirely.
	if crew := agent.Labels[labelCrew]; crew != "" {
		skillsCMName := "crew-" + crew + "-skills"
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "skills",
			MountPath: skillsMountPath,
			ReadOnly:  true,
		})
		volumes = append(volumes, corev1.Volume{
			Name: "skills",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: skillsCMName},
					Optional:             pointerTo(true),
				},
			},
		})
	}

	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agent.Name,
			Namespace: agent.Namespace,
			Labels:    labelsMap,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{labelAgent: agent.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labelsMap},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccountName,
					ImagePullSecrets:   r.imagePullSecrets(),
					Containers: []corev1.Container{{
						Name:            "agent",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Env:             env,
						Ports: []corev1.ContainerPort{{
							Name:          "http",
							ContainerPort: port,
						}},
						VolumeMounts: volumeMounts,
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/q/health/ready",
									Port: intstr.FromInt32(port),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
						},
					}},
					Volumes: volumes,
				},
			},
		},
	}
	// Coordinators own the crew's single durable request consumer (request-<crew>).
	// A RollingUpdate briefly overlaps the old and new pod; both reset that consumer
	// and delete it out from under each other, churning 409 Consumer Deleted and
	// dropping in-flight requests. A single-replica coordinator must roll with
	// Recreate (terminate old, THEN start new) so the pods never overlap. Queued
	// requests persist in the stream and the new pod processes them; nothing drops.
	// See the [[Coordinator NATS Consumer 409 Deleted]] note.
	if agent.Spec.DiscussRole == "coordinator" {
		d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}

	if agent.Spec.Deployment != nil && agent.Spec.Deployment.Resources != nil {
		d.Spec.Template.Spec.Containers[0].Resources = *agent.Spec.Deployment.Resources
	}
	// Prompt hash on the pod template: changing it rolls the pod (agents cache
	// the system prompt at startup) and feeds computeDeploymentHash so a
	// PromptModule-only edit triggers a Deployment update.
	if d.Spec.Template.Annotations == nil {
		d.Spec.Template.Annotations = map[string]string{}
	}
	d.Spec.Template.Annotations[promptHashAnnotation] = promptHash
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	d.Annotations[deploymentHashAnnotation] = computeDeploymentHash(d)
	return d
}

// imagePullSecrets returns the pull secrets KubemootConfig declares for operator-managed
// workloads; nil when none are configured (the public registry needs none).
func (r *AgentReconciler) imagePullSecrets() []corev1.LocalObjectReference {
	if r.ConfigCache == nil {
		return nil
	}
	return r.ConfigCache.GetImagePullSecrets()
}

// resolveDeploymentParams derives replicas, port, image, and serviceAccountName
// from the Agent spec, KubemootConfig, and defaults (Agent.spec.deployment wins).
func (r *AgentReconciler) resolveDeploymentParams(agent *kubemootv1alpha1.Agent) (replicas, port int32, image, serviceAccountName string) {
	replicas = int32(1)
	port = int32(8080)
	// Image preference: Agent.spec.deployment.image > KubemootConfig.spec.images.agentRuntime > hardcoded :latest fallback.
	image = defaultAgentRuntimeImage
	if r.ConfigCache != nil {
		if configured := r.ConfigCache.GetAgentRuntimeImage(); configured != "" {
			image = configured
		}
	}
	if agent.Spec.Deployment != nil {
		if agent.Spec.Deployment.Replicas > 0 {
			replicas = agent.Spec.Deployment.Replicas
		}
		if agent.Spec.Deployment.Port > 0 {
			port = agent.Spec.Deployment.Port
		}
		if agent.Spec.Deployment.Image != "" {
			image = agent.Spec.Deployment.Image
		}
		serviceAccountName = agent.Spec.Deployment.ServiceAccountName
	}
	return replicas, port, image, serviceAccountName
}

// buildDeploymentLabels builds the Deployment/pod labels, including the per-phase
// provider bindings used by ModelProviderReconciler for the load-aware bin-pack
// penalty in scoreCandidate. The provider labels are NOT in the selector
// (selector immutability would block legitimate re-scheduling).
func buildDeploymentLabels(agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) map[string]string {
	labelsMap := map[string]string{
		"app":                         agent.Name,
		"app.kubernetes.io/name":      agent.Name,
		"app.kubernetes.io/component": "agent",
		"app.kubernetes.io/part-of":   "kubemoot",
		labelAgent:                    agent.Name,
	}
	if crew, ok := agent.Labels[labelCrew]; ok {
		labelsMap[labelCrew] = crew
	}
	if mulling != nil && mulling.Provider != nil {
		labelsMap["kubemoot.ai/mulling-provider"] = mulling.Provider.Name
	}
	if triage != nil && triage.Provider != nil {
		labelsMap["kubemoot.ai/triage-provider"] = triage.Provider.Name
	}
	return labelsMap
}

// skillsMountPath is the fixed volume-mount path for the per-crew skills
// ConfigMap. The agent-runtime reads this via KUBEMOOT_SKILLS_DIR so the
// mount path and the runtime read path cannot drift.
const skillsMountPath = "/app/config/skills"

// buildEnvVars constructs the KUBEMOOT_* env list for the agent runtime.
func (r *AgentReconciler) buildEnvVars(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick, port int32) []corev1.EnvVar {
	crew := agent.Labels[labelCrew]
	env := baseAgentEnvVars(agent, crew, mulling, triage, port)
	env = append(env, candidateEnvVars(mulling, triage)...)
	env = append(env, agentModelEnvVars(agent)...)
	env = append(env, agentDiscussRelevanceEnvVars(agent)...)
	env = append(env, r.agentDiscussRoleEnvVars(ctx, agent, crew)...)
	env = append(env, r.agentGatewayEnvVars(ctx, agent)...)
	env = append(env, r.ragSourceEnvVars(ctx, agent)...)
	// Crew agents get the skills dir env so the agent-runtime read path
	// matches the mount without the path being hardcoded in the Java code.
	if crew != "" {
		env = append(env, corev1.EnvVar{
			Name:  "KUBEMOOT_SKILLS_DIR",
			Value: skillsMountPath,
		})
	}

	// User-provided env on Agent.spec.deployment.env wins via append-last semantics.
	if agent.Spec.Deployment != nil {
		env = append(env, agent.Spec.Deployment.Env...)
	}
	// Crew working-memory config (Crew.spec.memory) → agent env. Defaults
	// applied when the Crew or its memory block is absent. See [[Crew Working Memory]].
	env = append(env, r.memoryEnvVars(ctx, agent.Namespace)...)

	return env
}

// agentNATSURL is the NATS endpoint handed to agent pods: the operator's own
// NATS_URL (the chart's nats.url), so agents and operator always share one server.
// The in-cluster default applies only when the operator has none.
func agentNATSURL() string {
	if url := os.Getenv("NATS_URL"); url != "" {
		return url
	}
	return defaultNATSURL
}

// fieldPathNamespace is the downward-API field holding a pod's namespace.
const fieldPathNamespace = "metadata.namespace"

// namespaceEnvVar sets KUBEMOOT_NAMESPACE from the downward API, the namespace
// every Kubemoot subject and key a workload builds is scoped to.
func namespaceEnvVar() corev1.EnvVar {
	return corev1.EnvVar{
		Name: crewscope.NamespaceEnv,
		ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: fieldPathNamespace},
		},
	}
}

// baseAgentEnvVars returns the always-present KUBEMOOT_* env plus the crew-version
// provenance var (when the crew chart stamped it).
func baseAgentEnvVars(agent *kubemootv1alpha1.Agent, crew string, mulling, triage *modelPick, port int32) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "KUBEMOOT_AGENT_NAME", Value: agent.Name},
		{Name: "KUBEMOOT_AGENT_TYPE", Value: stringOrDefault(string(agent.Spec.Type), "chat")},
		{Name: "KUBEMOOT_AGENT_DESCRIPTION", Value: agent.Spec.Description},
		{Name: "KUBEMOOT_CREW", Value: crew},
		namespaceEnvVar(),
		{Name: "KUBEMOOT_SYSTEM_PROMPT_FILE", Value: "/etc/kubemoot/policy/system.txt"},
		{Name: "KUBEMOOT_MODEL_MODEL", Value: mulling.ModelID},
		{Name: "KUBEMOOT_MODEL_ENDPOINT", Value: mulling.Endpoint},
		{Name: "KUBEMOOT_TRIAGE_MODEL_MODEL_ID", Value: triage.ModelID},
		{Name: "KUBEMOOT_TRIAGE_MODEL_ENDPOINT", Value: triage.Endpoint},
		{Name: "QUARKUS_HTTP_PORT", Value: fmt.Sprintf("%d", port)},
		{Name: "QUARKUS_LANGCHAIN4J_OLLAMA_BASE_URL", Value: mulling.Endpoint},
		{Name: "KUBEMOOT_NATS_URL", Value: agentNATSURL()},
	}
	// Crew chart version (provenance): the crew Helm chart stamps
	// kubemoot.ai/crew-version on every CR via its labels helper. Surface it to the
	// runtime so the coordinator can record which crew VERSION produced a thread.
	// Absent for hand-applied (e.g. kmctl-scaffolded) crews - omit then.
	if crewVersion := agent.Labels[crewVersionLabel]; crewVersion != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_CREW_VERSION", Value: crewVersion})
	}
	return env
}

// agentModelEnvVars returns the optional model-tuning and tool-filter env vars
// (temperature, max tokens, think, enabled/disabled tools).
func agentModelEnvVars(agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	var env []corev1.EnvVar
	if temp := stringOrDefault(agent.Spec.Temperature, "0.3"); temp != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_MODEL_TEMPERATURE", Value: temp})
	}
	if agent.Spec.MaxTokens > 0 {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_MODEL_MAX_TOKENS", Value: fmt.Sprintf("%d", agent.Spec.MaxTokens)})
	}
	if agent.Spec.Think != nil {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_MODEL_THINK", Value: strconv.FormatBool(*agent.Spec.Think)})
	}
	if len(agent.Spec.EnabledTools) > 0 {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_ENABLED_TOOLS", Value: strings.Join(agent.Spec.EnabledTools, ",")})
	}
	if len(agent.Spec.DisabledTools) > 0 {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_DISABLED_TOOLS", Value: strings.Join(agent.Spec.DisabledTools, ",")})
	}
	return env
}

// agentDiscussRelevanceEnvVars returns the discuss keyword/relevance/role/summary
// env vars derived from the agent's discuss-relevance and triage spec.
func agentDiscussRelevanceEnvVars(agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	var env []corev1.EnvVar
	if len(agent.Spec.DiscussKeywords) > 0 {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_DISCUSS_KEYWORDS", Value: strings.Join(agent.Spec.DiscussKeywords, ",")})
	}
	if agent.Spec.DiscussRelevance != nil {
		if agent.Spec.DiscussRelevance.Mode != "" {
			env = append(env, corev1.EnvVar{Name: "KUBEMOOT_DISCUSS_RELEVANCE_MODE", Value: agent.Spec.DiscussRelevance.Mode})
		}
		if agent.Spec.DiscussRelevance.PromptHint != "" {
			env = append(env, corev1.EnvVar{Name: "KUBEMOOT_DISCUSS_RELEVANCE_PROMPT_HINT", Value: agent.Spec.DiscussRelevance.PromptHint})
		}
	}
	if agent.Spec.DiscussRole != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_DISCUSS_ROLE", Value: agent.Spec.DiscussRole})
	}
	if agent.Spec.TriageSummary != "" {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_TRIAGE_SUMMARY", Value: agent.Spec.TriageSummary})
	}
	return env
}

// agentDiscussRoleEnvVars derives the DiscussionOrchestrator/DiscussionSubscriber
// gates and (for coordinators) the resume-search endpoint + analyst flag, plus the
// discuss-channels subscription set.
//
// The agent-runtime reads kubemoot.discuss.coordinator (default false) and
// kubemoot.discuss.tooler (default true). Without these, the
// orchestrator/subscriber bail out at startup and no discussion happens.
func (r *AgentReconciler) agentDiscussRoleEnvVars(ctx context.Context, agent *kubemootv1alpha1.Agent, crew string) []corev1.EnvVar {
	var env []corev1.EnvVar
	if agent.Spec.DiscussRole == "coordinator" {
		env = append(env,
			corev1.EnvVar{Name: "KUBEMOOT_DISCUSS_COORDINATOR", Value: "true"},
			corev1.EnvVar{Name: "KUBEMOOT_DISCUSS_TOOLER", Value: "false"},
		)
		// Point the coordinator at its crew's resume query service so the
		// DiscussionOrchestrator vector pre-filter (resume_sync.go provisions the
		// per-crew RAGSource + query service crew-<crew>-resumes-query). Absent
		// endpoint → agent-runtime falls back to keyword self-selection.
		crewName := crew
		if crewName == "" {
			crewName = agent.Namespace
		}
		resumeQuerySvc := resumeRAGSourceName(crewName) + "-query"
		env = append(env, corev1.EnvVar{
			Name:  "KUBEMOOT_RESUME_SEARCH_ENDPOINT",
			Value: fmt.Sprintf("http://%s.%s:%d", resumeQuerySvc, agent.Namespace, resumeQueryPort),
		})
		// If the crew declares any analyst-role agent, the coordinator must run
		// the REVIEW phase (where analysts self-select) rather than taking the
		// single-agree fast path that skips it.
		if r.crewHasAnalysts(ctx, agent.Namespace) {
			env = append(env, corev1.EnvVar{Name: "KUBEMOOT_DISCUSS_HAS_ANALYSTS", Value: "true"})
		}
	}
	// A researcher keeps its discussion subscriber, like a tooler: when the
	// coordinator convenes it, it answers, and its signals carry role=researcher,
	// which the coordinator folds into the synthesis without counting them toward
	// settling. Disabling the subscriber left a convened researcher silent.
	// DiscussChannels feeds the orchestrator/subscriber's NATS subscription set.
	if len(agent.Spec.DiscussChannels) > 0 {
		env = append(env, corev1.EnvVar{Name: "KUBEMOOT_DISCUSS_CHANNELS", Value: strings.Join(agent.Spec.DiscussChannels, ",")})
	}
	return env
}

// agentGatewayEnvVars wires the agent-runtime to a crew MCPGateway when one exists
// in the namespace, switching McpClientService into gateway mode.
//
// Without this, agents fall through to direct mode with an empty server list and
// report "0 tools".
func (r *AgentReconciler) agentGatewayEnvVars(ctx context.Context, agent *kubemootv1alpha1.Agent) []corev1.EnvVar {
	gwName, gwPort, ok := r.findCrewGateway(ctx, agent.Namespace)
	if !ok {
		return nil
	}
	endpoint := fmt.Sprintf("http://%s.%s:%d", gwName, agent.Namespace, gwPort)
	return []corev1.EnvVar{
		{Name: "KUBEMOOT_GATEWAY_ENABLED", Value: "true"},
		{Name: "KUBEMOOT_GATEWAY_ENDPOINT", Value: endpoint},
	}
}

// memoryEnvVars resolves the agent's crew working-memory config and emits the
// KUBEMOOT_MEMORY_* env the agent-runtime CrewMemoryClient reads. Defaults
// mirror the MemoryConfig kubebuilder defaults; an explicit false on the
// pointer bools is honoured (that's why they are *bool).
// crewHasAnalysts reports whether any Agent in the namespace declares
// discuss-role=analyst. A crew maps to a namespace, so a namespace-scoped list
// is the crew roster. The coordinator uses this to keep the REVIEW phase (where
// analysts self-select) instead of taking the single-agree fast path.
func (r *AgentReconciler) crewHasAnalysts(ctx context.Context, namespace string) bool {
	list := &kubemootv1alpha1.AgentList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return false
	}
	for i := range list.Items {
		if list.Items[i].Spec.DiscussRole == "analyst" {
			return true
		}
	}
	return false
}

// applyMemoryOverrides overlays the crew's CrewMemoryConfig onto the default
// memory settings in place. A nil config (no Crew or no memory block) leaves the
// defaults untouched; an explicit false on the pointer bools is honoured.
func applyMemoryOverrides(m *kubemootv1alpha1.CrewMemoryConfig, enabled, verify *bool, maxFacts, ttlDays, injectLimit *int32) {
	if m == nil {
		return
	}
	if m.Enabled != nil {
		*enabled = *m.Enabled
	}
	if m.VerifyOnAdd != nil {
		*verify = *m.VerifyOnAdd
	}
	if m.MaxFacts > 0 {
		*maxFacts = m.MaxFacts
	}
	if m.TTLDays > 0 {
		*ttlDays = m.TTLDays
	}
	if m.InjectLimit > 0 {
		*injectLimit = m.InjectLimit
	}
}

func (r *AgentReconciler) memoryEnvVars(ctx context.Context, namespace string) []corev1.EnvVar {
	enabled, verify := true, true
	maxFacts, ttlDays, injectLimit := int32(5000), int32(365), int32(8)
	list := &kubemootv1alpha1.CrewList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err == nil && len(list.Items) > 0 {
		applyMemoryOverrides(list.Items[0].Spec.Memory, &enabled, &verify, &maxFacts, &ttlDays, &injectLimit)
	}
	return []corev1.EnvVar{
		{Name: "KUBEMOOT_MEMORY_ENABLED", Value: fmt.Sprintf("%t", enabled)},
		{Name: "KUBEMOOT_MEMORY_MAX_FACTS", Value: fmt.Sprintf("%d", maxFacts)},
		{Name: "KUBEMOOT_MEMORY_TTL_DAYS", Value: fmt.Sprintf("%d", ttlDays)},
		{Name: "KUBEMOOT_MEMORY_INJECT_LIMIT", Value: fmt.Sprintf("%d", injectLimit)},
		{Name: "KUBEMOOT_MEMORY_VERIFY_ON_ADD", Value: fmt.Sprintf("%t", verify)},
	}
}

// findCrewGateway returns the first MCPGateway CR in the given namespace,
// along with its service name and port. The Service that the MCPGateway
// reconciler creates shares the gateway's name; the port comes from spec
// (defaulting to 8080).
//
// Returns ok=false when no gateway exists in the namespace. We don't fail
// reconciliation on absence — agents without a gateway just run with the
// direct-mode default (empty MCP server list, 0 tools).
func (r *AgentReconciler) findCrewGateway(ctx context.Context, namespace string) (name string, port int32, ok bool) {
	list := &kubemootv1alpha1.MCPGatewayList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		// Logged at debug — an unreadable list shouldn't block agent reconciliation.
		logf.FromContext(ctx).V(1).Info("list MCPGateways failed; agent will run without gateway wiring",
			"namespace", namespace, "err", err)
		return "", 0, false
	}
	if len(list.Items) == 0 {
		return "", 0, false
	}
	gw := &list.Items[0]
	p := gw.Spec.Port
	if p == 0 {
		p = 8080
	}
	return gw.Name, p, true
}

// ensureService creates or updates a ClusterIP Service for the agent.
func (r *AgentReconciler) ensureService(ctx context.Context, agent *kubemootv1alpha1.Agent) error {
	port := int32(8080)
	if agent.Spec.Deployment != nil && agent.Spec.Deployment.Port > 0 {
		port = agent.Spec.Deployment.Port
	}
	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agent.Name,
			Namespace: agent.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kubemoot-operator",
				labelAgent:                     agent.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{labelAgent: agent.Name},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       port,
				TargetPort: intstr.FromInt32(port),
			}},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
	if err := controllerutil.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return err
	}
	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Selector = desired.Spec.Selector
	return r.Update(ctx, existing)
}

// refreshStatus updates Agent.status based on the scheduling outcome.
func (r *AgentReconciler) refreshStatus(ctx context.Context, agent *kubemootv1alpha1.Agent, mulling, triage *modelPick) error {
	port := int32(8080)
	if agent.Spec.Deployment != nil && agent.Spec.Deployment.Port > 0 {
		port = agent.Spec.Deployment.Port
	}
	agent.Status.Phase = "Running"
	agent.Status.Ready = true
	agent.Status.Endpoint = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", agent.Name, agent.Namespace, port)
	agent.Status.Message = fmt.Sprintf("mulling=%s@%s; triage=%s@%s",
		mulling.ModelID, mulling.Provider.Name, triage.ModelID, triage.Provider.Name)
	return r.Status().Update(ctx, agent)
}

// ----- helpers --------------------------------------------------------------

func stringOrDefault(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

func pointerTo[T any](v T) *T { return &v }

// SetupWithManager registers the reconciler.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Scheme = mgr.GetScheme()
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.Agent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		// Re-reconcile every Agent when KubemootConfig changes so an
		// agentRuntime image bump (e.g. via chart upgrade) propagates
		// to the Deployment spec without per-Agent annotation or
		// operator restart. Closes the [[Operator Does Not Propagate
		// KubemootConfig Image Bumps]] gap. See kubemootconfig_propagation.go.
		Watches(
			&kubemootv1alpha1.KubemootConfig{},
			enqueueAllOnKubemootConfigChange(mgr.GetClient(),
				func() client.ObjectList { return &kubemootv1alpha1.AgentList{} },
				"agent"),
		).
		// Re-reconcile a crew's Agents when its CrewSchedulingPolicy changes so an
		// edited rule (e.g. a triage `prefer` block) re-derives each agent's
		// per-phase model binding immediately, not on the next resync.
		Watches(
			&kubemootv1alpha1.CrewSchedulingPolicy{},
			enqueueAgentsOnCSPChange(mgr.GetClient()),
		).
		// Re-reconcile an Agent when a PromptModule it references changes so an
		// edited prompt re-renders system.txt AND rolls the pod (via the prompt
		// hash in the Deployment pod template), instead of staying inert until
		// the next unrelated change. See promptmodule_propagation.go.
		Watches(
			&kubemootv1alpha1.PromptModule{},
			enqueueAgentsOnPromptModuleChange(mgr.GetClient()),
		).
		// Re-reconcile the Agents in a namespace when its Models are added, removed,
		// relabeled, or become usable, so each binding and candidate list follows
		// the Models that exist. See model_propagation.go.
		Watches(
			&kubemootv1alpha1.Model{},
			enqueueAgentsOnModelChange(mgr.GetClient()),
			builder.WithPredicates(modelBindingChanged()),
		).
		// Re-reconcile a crew's coordinator when one of its specialists is added,
		// changed, or removed, so the crew's resumes (and so selection) include it.
		// See agent_resume_propagation.go.
		Watches(
			&kubemootv1alpha1.Agent{},
			enqueueCoordinatorOnSpecialistChange(mgr.GetClient()),
			builder.WithPredicates(specialistResumeChanged()),
		).
		// Re-reconcile an Agent when a RAGSource it references reports a new query
		// endpoint or changes its query service, so the agent's RAG env follows.
		// See ragsource_propagation.go.
		Watches(
			&kubemootv1alpha1.RAGSource{},
			enqueueAgentsOnRAGSourceChange(mgr.GetClient()),
			builder.WithPredicates(ragSourceEndpointChanged()),
		).
		Complete(r)
}
