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

// catalog_selection.go lets a crew choose its models from the cluster's catalog.
// An Agent's phase rules are matched against the catalog entries every provider
// publishes, unless the crew still ships Models of its own, in which case it uses
// exactly those. Both sources reach the scoring code as Model values, so one
// scorer serves both.
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Where a crew's models come from, as the CatalogMode condition on its
// CrewSchedulingPolicy reports.
const (
	catalogModeCluster   = "Cluster"
	catalogModeCrewLocal = "CrewLocal"

	conditionTypeCatalogMode        = "CatalogMode"
	conditionTypeLegacyLatencyClass = "LegacyLatencyClass"

	// defaultAbilityWeight is the points one matching ability earns when the
	// policy does not set abilityWeight.
	defaultAbilityWeight int64 = 20
)

// abilityNames are the agent capabilities that are also model abilities.
var abilityNames = []string{aiv1alpha1.AbilityToolCalling, aiv1alpha1.AbilityReasoning, aiv1alpha1.AbilityVision}

// modelSource is the models a crew selects from and where they came from.
type modelSource struct {
	// Mode is catalogModeCrewLocal or catalogModeCluster.
	Mode string
	// Models are the candidates: the crew's own Models, or one in-memory Model per
	// available catalog entry. A catalog Model lives in its provider's namespace.
	Models []aiv1alpha1.Model
}

// modelSource decides the crew's mode and lists its candidate models. A crew that
// has any Model of its own, outside the operator's namespace, uses exactly those
// until it drops them; every other crew uses the catalog.
func (r *AgentReconciler) modelSource(ctx context.Context, namespace string) (modelSource, error) {
	local, err := crewLocalModels(ctx, r.Client, namespace)
	if err != nil {
		return modelSource{}, err
	}
	if catalogModeFor(local) == catalogModeCrewLocal {
		return modelSource{Mode: catalogModeCrewLocal, Models: local}, nil
	}
	models, err := r.catalogCandidates(ctx, namespace)
	return modelSource{Mode: catalogModeCluster, Models: models}, err
}

// catalogModeFor is the mode of a crew whose own Models are local: CrewLocal when it
// has any, Cluster otherwise. It is the one place the mode is decided, for the pick
// and for the CatalogMode condition alike.
func catalogModeFor(local []aiv1alpha1.Model) string {
	if len(local) > 0 {
		return catalogModeCrewLocal
	}
	return catalogModeCluster
}

// crewLocalModels is the Models in namespace that are not being deleted; none in the
// operator's namespace.
func crewLocalModels(ctx context.Context, c client.Reader, namespace string) ([]aiv1alpha1.Model, error) {
	if namespace == operatorNamespace() {
		return nil, nil
	}
	models := &aiv1alpha1.ModelList{}
	if err := c.List(ctx, models, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing Models: %w", err)
	}
	var out []aiv1alpha1.Model
	for i := range models.Items {
		if models.Items[i].DeletionTimestamp.IsZero() {
			out = append(out, models.Items[i])
		}
	}
	return out, nil
}

// catalogCandidates turns the entries of the providers a crew can use (those in its
// own namespace and in the operator's) into candidate Models: only models the
// provider has now, and not ones calibration found do not fit.
func (r *AgentReconciler) catalogCandidates(ctx context.Context, namespace string) ([]aiv1alpha1.Model, error) {
	var out []aiv1alpha1.Model
	for _, ns := range catalogNamespaces(namespace) {
		providers := &aiv1alpha1.ModelProviderList{}
		if err := r.List(ctx, providers, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("listing ModelProviders: %w", err)
		}
		for i := range providers.Items {
			out = append(out, providerCandidates(&providers.Items[i])...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name
	})
	return out, nil
}

// catalogNamespaces are the namespaces whose providers a crew in namespace may
// use: its own, then the operator's.
func catalogNamespaces(namespace string) []string {
	if op := operatorNamespace(); op != namespace {
		return []string{namespace, op}
	}
	return []string{namespace}
}

// providerCandidates are the in-memory Models for the available entries of one
// provider's catalog.
func providerCandidates(p *aiv1alpha1.ModelProvider) []aiv1alpha1.Model {
	var out []aiv1alpha1.Model
	for _, e := range p.Status.Models {
		if !e.Present || calibrationSaysNoFit(p, e.Name) {
			continue
		}
		out = append(out, catalogEntryModel(p, e))
	}
	return out
}

// catalogEntryModel is a catalog entry as a Model value, never stored, so the
// scoring code that takes a Model serves the catalog as it is. Its namespace is the
// provider's, which is how the scheduler finds the provider again.
func catalogEntryModel(p *aiv1alpha1.ModelProvider, e aiv1alpha1.CatalogModel) aiv1alpha1.Model {
	lbls := make(map[string]string, len(e.Labels))
	for k, v := range e.Labels {
		lbls[k] = v
	}
	return aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.Name + "/" + e.Name,
			Namespace: p.Namespace,
			Labels:    lbls,
		},
		Spec: aiv1alpha1.ModelSpec{
			ProviderRef:   p.Name,
			Model:         e.Name,
			ContextLength: e.ContextLength,
			VRAMMib:       e.VRAMMib,
		},
		Status: aiv1alpha1.ModelStatus{Ready: true},
	}
}

// calibrationSaysNoFit reports whether calibration measured the model on the
// provider and found it does not fit the accelerator's memory.
func calibrationSaysNoFit(p *aiv1alpha1.ModelProvider, model string) bool {
	if p.Status.Calibration == nil {
		return false
	}
	for _, m := range p.Status.Calibration.Models {
		if m.Model == model {
			return !m.Fits
		}
	}
	return false
}

// providerOf resolves the provider of a candidate Model: the provider's own
// namespace for a catalog Model (it lives there), the usual search for a crew's own.
func (r *AgentReconciler) providerOf(ctx context.Context, namespace string, m *aiv1alpha1.Model) (*aiv1alpha1.ModelProvider, bool) {
	if m.Namespace != "" && m.Namespace != namespace {
		prov := &aiv1alpha1.ModelProvider{}
		key := client.ObjectKey{Namespace: m.Namespace, Name: m.Spec.ProviderRef}
		if err := r.Get(ctx, key, prov); err != nil {
			return nil, false
		}
		return prov, true
	}
	return r.resolveProvider(ctx, namespace, m.Spec.ProviderRef)
}

// ---- matching -------------------------------------------------------------

// requirementsMatch reports whether lbls satisfies every requirement. A label
// missing fails Gt, Lt and In, as for node affinity. An operator or value that is
// not valid is an error.
func requirementsMatch(reqs []corev1.NodeSelectorRequirement, lbls map[string]string) (bool, error) {
	set := labels.Set(lbls)
	for _, r := range reqs {
		req, err := aiv1alpha1.RequirementOf(r)
		if err != nil {
			return false, err
		}
		if !req.Matches(set) {
			return false, nil
		}
	}
	return true, nil
}

// labelSelectorMatches reports whether lbls satisfies the selector; a nil selector
// matches everything.
func labelSelectorMatches(sel *metav1.LabelSelector, lbls map[string]string) (bool, error) {
	if sel == nil {
		return true, nil
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false, err
	}
	return s.Matches(labels.Set(lbls)), nil
}

// requireMatches reports whether a Model's labels satisfy the rule's hard
// constraints: Require and RequireExpressions together. A nil rule admits all. The
// error is a constraint that is not valid.
func requireMatches(rule *aiv1alpha1.SchedulingRule, lbls map[string]string) (bool, error) {
	if rule == nil {
		return true, nil
	}
	ok, err := labelSelectorMatches(rule.Require, lbls)
	if err != nil || !ok {
		return false, err
	}
	return requirementsMatch(rule.RequireExpressions, lbls)
}

// termMatches reports whether a preference term applies to the labels: its selector
// and every expression hold. A term that is not valid applies to nothing.
func termMatches(term aiv1alpha1.PreferenceTerm, lbls map[string]string) bool {
	ok, err := labelSelectorMatches(term.Selector, lbls)
	if err != nil || !ok {
		return false
	}
	ok, err = requirementsMatch(term.Expressions, lbls)
	return err == nil && ok
}

// ---- scoring --------------------------------------------------------------

// phaseScoring is the agent- and policy-derived part of scoring one phase.
type phaseScoring struct {
	// Bias is the agent's quality bias; HaveBias says whether there is one.
	Bias     float64
	HaveBias bool
	// Abilities are the model abilities the agent declares, and AbilityWeight the
	// points each earns; no abilities means the term is off.
	Abilities     []string
	AbilityWeight int64
}

// newPhaseScoring derives the scoring of one phase for an agent. The ability term
// applies only to a crew on the catalog and to a rule with no explicit prefer block,
// so an explicit rule is exactly what it says and an unmigrated crew scores as before.
func newPhaseScoring(agent *aiv1alpha1.Agent, policy *aiv1alpha1.CrewSchedulingPolicy, rule *aiv1alpha1.SchedulingRule, mode string) phaseScoring {
	bias, have := phaseQualityBias(agent, policy, rule)
	sc := phaseScoring{Bias: bias, HaveBias: have}
	if mode != catalogModeCluster || (rule != nil && len(rule.Prefer) > 0) {
		return sc
	}
	sc.Abilities = agentAbilities(agent.Spec.Capabilities)
	sc.AbilityWeight = defaultAbilityWeight
	if policy != nil && policy.Spec.AbilityWeight != nil {
		sc.AbilityWeight = int64(*policy.Spec.AbilityWeight)
	}
	return sc
}

// agentAbilities are the declared capabilities that are model abilities.
func agentAbilities(capabilities []string) []string {
	var out []string
	for _, a := range abilityNames {
		for _, c := range capabilities {
			if c == a {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// abilityScore is the points a model earns for the abilities the agent wants and the
// model has, with a reason token; zero and "" when none apply.
func (sc phaseScoring) abilityScore(lbls map[string]string) (int64, string) {
	var n int64
	for _, a := range sc.Abilities {
		if lbls[aiv1alpha1.CapabilityLabelPrefix+a] == valueTrue {
			n++
		}
	}
	if n == 0 || sc.AbilityWeight == 0 {
		return 0, ""
	}
	s := n * sc.AbilityWeight
	return s, fmt.Sprintf("ability+%d", s)
}

// ---- why nothing matched ---------------------------------------------------

// noMatchReason explains an empty candidate set for a phase. Against the catalog it
// says what the catalog holds and which constraint nothing met, naming a missing or
// unknown size; for a crew's own Models it keeps the plain message.
func noMatchReason(phase string, rule *aiv1alpha1.SchedulingRule, src modelSource, namespace string) string {
	if src.Mode != catalogModeCluster {
		if rule != nil && (rule.Require != nil || len(rule.RequireExpressions) > 0) {
			return fmt.Sprintf("no Ready Model matches phase=%s require selectors", phase)
		}
		return fmt.Sprintf("no Ready Model with a Ready ModelProvider in namespace %s", namespace)
	}
	if len(src.Models) == 0 {
		return fmt.Sprintf("the cluster's model catalog has no available model for phase=%s: no ModelProvider in namespace %s or %s reports one", phase, namespace, operatorNamespace())
	}
	msg := fmt.Sprintf("no model in the cluster catalog (%d available) satisfies the require of phase=%s%s", len(src.Models), phase, describeRequire(rule))
	if unsized := unsizedCount(src.Models); unsized > 0 && requiresSize(rule) {
		msg += fmt.Sprintf("; %d model(s) have no known size (set spec.parameters on a Model)", unsized)
	}
	return msg
}

// describeRequire renders a rule's hard constraints for a message.
func describeRequire(rule *aiv1alpha1.SchedulingRule) string {
	if rule == nil {
		return ""
	}
	var parts []string
	if rule.Require != nil {
		parts = append(parts, metav1.FormatLabelSelector(rule.Require))
	}
	for _, e := range rule.RequireExpressions {
		parts = append(parts, fmt.Sprintf("%s %s %v", e.Key, e.Operator, e.Values))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// requiresSize reports whether the rule's hard constraints mention the size label.
func requiresSize(rule *aiv1alpha1.SchedulingRule) bool {
	return rule != nil && (selectorUsesKey(rule.Require, aiv1alpha1.LabelSize) ||
		requirementsUseKey(rule.RequireExpressions, aiv1alpha1.LabelSize))
}

// unsizedCount is how many candidate Models carry no size label.
func unsizedCount(models []aiv1alpha1.Model) int {
	n := 0
	for i := range models {
		if models[i].Labels[aiv1alpha1.LabelSize] == "" {
			n++
		}
	}
	return n
}
