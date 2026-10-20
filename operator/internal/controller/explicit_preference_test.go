package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestExplicitPreference(t *testing.T) {
	if explicitPreference(nil) || explicitPreference(&aiv1alpha1.SchedulingRule{}) {
		t.Error("no rule, or a rule with neither, is not explicit")
	}
	if !explicitPreference(&aiv1alpha1.SchedulingRule{PreferFaster: 1}) {
		t.Error("preferFaster is an explicit preference")
	}
	if !explicitPreference(&aiv1alpha1.SchedulingRule{Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 1}}}) {
		t.Error("a prefer list is an explicit preference")
	}
}

func TestPreferFasterZeroIsNotAnExplicitPreference(t *testing.T) {
	if explicitPreference(&aiv1alpha1.SchedulingRule{PreferFaster: 0, Prefer: []aiv1alpha1.PreferenceTerm{}}) {
		t.Error("0 or unset turns the term off, and an empty prefer list is none")
	}
}

func TestAbilityScoreIsOffForAPreferFasterRuleAndOnForNeither(t *testing.T) {
	agent := &aiv1alpha1.Agent{Spec: aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}}}
	if sc := newPhaseScoring(agent, nil, &aiv1alpha1.SchedulingRule{PreferFaster: 30}, catalogModeCluster); len(sc.Abilities) != 0 {
		t.Errorf("a preferFaster rule gets no ability points: %+v", sc)
	}
	if sc := newPhaseScoring(agent, nil, &aiv1alpha1.SchedulingRule{}, catalogModeCluster); len(sc.Abilities) != 1 {
		t.Errorf("a rule with neither still does: %+v", sc)
	}
}

func TestPhaseQualityBiasIsOffForAPreferFasterRule(t *testing.T) {
	agent := &aiv1alpha1.Agent{Spec: aiv1alpha1.AgentSpec{Capabilities: []string{candReasoning}}}
	policy := &aiv1alpha1.CrewSchedulingPolicy{Spec: aiv1alpha1.CrewSchedulingPolicySpec{QualityBias: map[string]string{candReasoning: candBias}}}
	if _, ok := phaseQualityBias(agent, policy, &aiv1alpha1.SchedulingRule{PreferFaster: 50}); ok {
		t.Error("a rule that sets preferFaster gets no qualityBias")
	}
	if _, ok := phaseQualityBias(agent, policy, &aiv1alpha1.SchedulingRule{}); !ok {
		t.Error("a rule with neither still does")
	}
}

// candidateScores picks for the rule in a policy with qualityBias and returns the
// published candidate scores by model.
func candidateScores(t *testing.T, rule aiv1alpha1.SchedulingRule) map[string]int64 {
	t.Helper()
	rule.Phase = phaseMulling
	policy := pilotPolicy()
	policy.Spec.Rules = []aiv1alpha1.SchedulingRule{rule}
	r, _ := catalogReconciler(t,
		opServer(selOllama, aiv1alpha1.ServerTypeOllama,
			catEntryOf(selModel8, aiv1alpha1.SizeSmall, testToolCalling), catEntryOf(selModel30, aiv1alpha1.SizeLarge, testToolCalling, candReasoning)),
		policy)
	pick, err := r.pickModel(context.Background(), pilotAgent(candAgent, candReasoning), phaseMulling)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, c := range pick.Candidates {
		out[c.Model] = c.Score
	}
	return out
}

func TestACandidateScoreHasNoQualityBiasOrAbilityForAPreferFasterRule(t *testing.T) {
	plain := candidateScores(t, aiv1alpha1.SchedulingRule{})
	if plain[selModel30] <= plain[selModel8] || plain[selModel30] == 0 {
		t.Fatalf("a rule with neither is scored by size and ability: %v", plain)
	}
	faster := candidateScores(t, aiv1alpha1.SchedulingRule{PreferFaster: 50})
	if faster[selModel30] != 0 || faster[selModel8] != 0 {
		t.Errorf("a rule with only preferFaster scores the models equally: %v", faster)
	}
}

func TestARuleWithBothIsItsPreferListPlusTheSpeedRerank(t *testing.T) {
	both := candidateScores(t, aiv1alpha1.SchedulingRule{
		PreferFaster: 50,
		Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 40,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{aiv1alpha1.LabelSize: aiv1alpha1.SizeSmall}}}},
	})
	if both[selModel8] != 40 || both[selModel30] != 0 {
		t.Errorf("only the prefer list scores here (the speed is the runtime's): %v", both)
	}
}
