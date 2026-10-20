package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	selNS      = "crew-ns"
	selCrew    = "pilot"
	selOpNS    = "kubemoot"
	selOllama  = "ollama-gpu"
	selVLLM    = "vllm-gpu"
	selCtxKey  = "kubemoot.ai/context-tokens"
	selModel8  = "qwen3:8b"
	selModel30 = "qwen3:30b"

	selToolLabel    = "capability/tool-calling"
	selReasonLabel  = "capability/reasoning"
	selBogus        = "Bogus"
	selK8s          = "kubernetes"
	selOwnProvider  = "own-provider"
	selCrewProvider = "crew-provider"
)

// catEntryOf is a catalog entry the way the provider reconciler publishes one.
func catEntryOf(name, size string, abilities ...string) aiv1alpha1.CatalogModel {
	lbls := map[string]string{aiv1alpha1.LabelEngine: candProvider}
	if size != "" {
		lbls[aiv1alpha1.LabelSize] = size
	}
	for _, a := range abilities {
		lbls[aiv1alpha1.CapabilityLabelPrefix+a] = valueTrue
	}
	return aiv1alpha1.CatalogModel{Name: name, Present: true, Size: size, Capabilities: abilities, Labels: lbls}
}

func opProvider(name string, typ aiv1alpha1.ProviderType, entries ...aiv1alpha1.CatalogModel) *aiv1alpha1.ModelProvider {
	p := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: selOpNS},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: typ, Endpoint: "http://" + name + ":11434"},
		Status:     aiv1alpha1.ModelProviderStatus{Ready: true, Models: entries},
	}
	if typ == aiv1alpha1.ProviderTypeVLLM {
		p.Spec.Endpoint = ""
		p.Spec.Servers = nil
		p.Spec.ServiceRef = &aiv1alpha1.VLLMServiceRef{Name: name}
		for _, e := range entries {
			p.Status.VLLMServers = append(p.Status.VLLMServers, aiv1alpha1.VLLMServer{Model: e.Name, Endpoint: "http://" + name + ".vllm.svc:8000"})
		}
	}
	return p
}

// ---- requirement matching ---------------------------------------------------

func TestRequirementsMatchOperators(t *testing.T) {
	lbls := map[string]string{"size": "small", selCtxKey: "40960", "tier": ""}
	req := func(op corev1.NodeSelectorOperator, key string, values ...string) []corev1.NodeSelectorRequirement {
		return []corev1.NodeSelectorRequirement{{Key: key, Operator: op, Values: values}}
	}
	for _, tc := range []struct {
		name string
		reqs []corev1.NodeSelectorRequirement
		want bool
	}{
		{"In hit", req(corev1.NodeSelectorOpIn, "size", "small", "medium"), true},
		{"In miss", req(corev1.NodeSelectorOpIn, "size", "large"), false},
		{"In on a missing label", req(corev1.NodeSelectorOpIn, "absent", "x"), false},
		{"NotIn hit", req(corev1.NodeSelectorOpNotIn, "size", "large"), true},
		{"NotIn miss", req(corev1.NodeSelectorOpNotIn, "size", "small"), false},
		{"Exists hit", req(corev1.NodeSelectorOpExists, "tier"), true},
		{"Exists miss", req(corev1.NodeSelectorOpExists, "absent"), false},
		{"DoesNotExist hit", req(corev1.NodeSelectorOpDoesNotExist, "absent"), true},
		{"DoesNotExist miss", req(corev1.NodeSelectorOpDoesNotExist, "size"), false},
		{"Gt below", req(corev1.NodeSelectorOpGt, selCtxKey, "16383"), true},
		{"Gt is strict", req(corev1.NodeSelectorOpGt, selCtxKey, "40960"), false},
		{"Lt above", req(corev1.NodeSelectorOpLt, selCtxKey, "40961"), true},
		{"Lt is strict", req(corev1.NodeSelectorOpLt, selCtxKey, "40960"), false},
		{"Gt on a missing label fails", req(corev1.NodeSelectorOpGt, "absent", "1"), false},
		{"all must hold", append(req(corev1.NodeSelectorOpExists, "size"), req(corev1.NodeSelectorOpIn, "size", "large")...), false},
		{"none is a match", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requirementsMatch(tc.reqs, lbls)
			if err != nil || got != tc.want {
				t.Errorf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestRequirementsMatchRejectsInvalidRequirements(t *testing.T) {
	for _, r := range []corev1.NodeSelectorRequirement{
		{Key: "k", Operator: "Between", Values: []string{"1"}},
		{Key: "k", Operator: corev1.NodeSelectorOpGt, Values: []string{"many"}},
		{Key: "k", Operator: corev1.NodeSelectorOpGt, Values: []string{"1", "2"}},
		{Key: "k", Operator: corev1.NodeSelectorOpLt},
		{Key: "k", Operator: corev1.NodeSelectorOpIn},
		{Key: "k", Operator: corev1.NodeSelectorOpExists, Values: []string{"x"}},
	} {
		if _, err := requirementsMatch([]corev1.NodeSelectorRequirement{r}, map[string]string{"k": "1"}); err == nil {
			t.Errorf("requirement %+v must be an error", r)
		}
	}
}

func TestRequireMatchesCombinesSelectorAndExpressions(t *testing.T) {
	rule := &aiv1alpha1.SchedulingRule{
		Phase:              phaseMulling,
		Require:            &metav1.LabelSelector{MatchLabels: map[string]string{selToolLabel: valueTrue}},
		RequireExpressions: []corev1.NodeSelectorRequirement{{Key: selCtxKey, Operator: corev1.NodeSelectorOpGt, Values: []string{"16383"}}},
	}
	for _, tc := range []struct {
		name string
		lbls map[string]string
		want bool
	}{
		{"both hold", map[string]string{selToolLabel: valueTrue, selCtxKey: "32768"}, true},
		{"selector fails", map[string]string{selCtxKey: "32768"}, false},
		{"expression fails", map[string]string{selToolLabel: valueTrue, selCtxKey: "8192"}, false},
		{"no context label", map[string]string{selToolLabel: valueTrue}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := requireMatches(rule, tc.lbls); err != nil || got != tc.want {
				t.Errorf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if ok, err := requireMatches(nil, nil); !ok || err != nil {
		t.Errorf("a nil rule admits everything, got %v %v", ok, err)
	}
	bad := &aiv1alpha1.SchedulingRule{Require: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "k", Operator: selBogus}}}}
	if _, err := requireMatches(bad, nil); err == nil {
		t.Error("a malformed selector must be an error")
	}
}

func TestPreferRuleScoreWithExpressionTerms(t *testing.T) {
	rule := &aiv1alpha1.SchedulingRule{Prefer: []aiv1alpha1.PreferenceTerm{
		{Weight: 40, Expressions: []corev1.NodeSelectorRequirement{{Key: selCtxKey, Operator: corev1.NodeSelectorOpGt, Values: []string{"32767"}}}},
		{Weight: 10, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{aiv1alpha1.LabelSize: aiv1alpha1.SizeLarge}},
			Expressions: []corev1.NodeSelectorRequirement{{Key: selCtxKey, Operator: corev1.NodeSelectorOpLt, Values: []string{"100"}}}},
		{Weight: 99}, // neither selector nor expressions: contributes nothing
	}}
	m := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{selCtxKey: "40960", aiv1alpha1.LabelSize: aiv1alpha1.SizeLarge}}}
	if got, _ := preferRuleScore(rule, m); got != 40 {
		t.Errorf("score = %d, want 40 (the second term needs both its selector and its expression)", got)
	}
	m.Labels[selCtxKey] = "50"
	if got, _ := preferRuleScore(rule, m); got != 10 {
		t.Errorf("score = %d, want 10", got)
	}
}

// ---- size in quality bias ---------------------------------------------------

func TestQualityBiasScoreReadsSizeAndMatchesTheLatencyClassTable(t *testing.T) {
	for _, bias := range []float64{0, 0.2, 0.4, 0.5, 0.7, 1} {
		for size, class := range map[string]string{aiv1alpha1.SizeLarge: candHigh, aiv1alpha1.SizeMedium: candMedium, aiv1alpha1.SizeSmall: candLow} {
			bySize, _ := qualityBiasScore(bias, map[string]string{aiv1alpha1.LabelSize: size})
			byClass, _ := qualityBiasScore(bias, map[string]string{latencyClassLabel: class})
			if bySize != byClass {
				t.Errorf("bias %v: size %s scores %d, latencyClass %s scores %d", bias, size, bySize, class, byClass)
			}
		}
	}
}

func TestQualityBiasScoreSizeBeatsLatencyClassAndUnknownScoresNothing(t *testing.T) {
	both := map[string]string{aiv1alpha1.LabelSize: aiv1alpha1.SizeLarge, latencyClassLabel: candLow}
	if s, r := qualityBiasScore(0.7, both); s != 70 || r != "bias-high+70" {
		t.Errorf("size must decide: %d %q", s, r)
	}
	for _, lbls := range []map[string]string{{}, {aiv1alpha1.LabelSize: "huge"}, {latencyClassLabel: "extreme"}} {
		if s, r := qualityBiasScore(0.7, lbls); s != 0 || r != "" {
			t.Errorf("labels %v: %d %q, want nothing", lbls, s, r)
		}
	}
}

// ---- abilities --------------------------------------------------------------

func TestNewPhaseScoringAbilityRules(t *testing.T) {
	agent := &aiv1alpha1.Agent{Spec: aiv1alpha1.AgentSpec{Capabilities: []string{selK8s, candReasoning, testToolCalling}}}
	on := newPhaseScoring(agent, nil, &aiv1alpha1.SchedulingRule{Phase: phaseMulling}, catalogModeCluster)
	if len(on.Abilities) != 2 || on.AbilityWeight != 20 {
		t.Errorf("default scoring = %+v, want tool-calling and reasoning at 20", on)
	}
	w := int32(35)
	custom := newPhaseScoring(agent, &aiv1alpha1.CrewSchedulingPolicy{Spec: aiv1alpha1.CrewSchedulingPolicySpec{AbilityWeight: &w}}, nil, catalogModeCluster)
	if custom.AbilityWeight != 35 {
		t.Errorf("weight = %d, want 35", custom.AbilityWeight)
	}
	local := newPhaseScoring(agent, nil, nil, catalogModeCrewLocal)
	if len(local.Abilities) != 0 {
		t.Errorf("a crew on its own Models scores as before: %+v", local)
	}
	explicit := newPhaseScoring(agent, nil, &aiv1alpha1.SchedulingRule{Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 1, Selector: &metav1.LabelSelector{}}}}, catalogModeCluster)
	if len(explicit.Abilities) != 0 {
		t.Errorf("an explicit preference is exactly what it says: %+v", explicit)
	}
	zero := int32(0)
	if s, _ := (phaseScoring{Abilities: []string{candReasoning}, AbilityWeight: int64(zero)}).abilityScore(map[string]string{selReasonLabel: valueTrue}); s != 0 {
		t.Errorf("weight 0 turns the term off, got %d", s)
	}
}

func TestAbilityScoreCountsMatchingAbilities(t *testing.T) {
	sc := phaseScoring{Abilities: []string{testToolCalling, candReasoning, "vision"}, AbilityWeight: 20}
	s, reason := sc.abilityScore(map[string]string{selToolLabel: valueTrue, selReasonLabel: valueTrue})
	if s != 40 || reason != "ability+40" {
		t.Errorf("score = %d %q", s, reason)
	}
	if s, reason := sc.abilityScore(nil); s != 0 || reason != "" {
		t.Errorf("no abilities: %d %q", s, reason)
	}
}

// ---- policy conditions ------------------------------------------------------

func TestSetCatalogConditions(t *testing.T) {
	policy := &aiv1alpha1.CrewSchedulingPolicy{Spec: aiv1alpha1.CrewSchedulingPolicySpec{Rules: []aiv1alpha1.SchedulingRule{
		{Phase: phaseTriage, Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 5, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{latencyClassLabel: candLow}}}}},
	}}}
	setCatalogConditions(policy, catalogModeCluster)
	if c := meta.FindStatusCondition(policy.Status.Conditions, conditionTypeCatalogMode); c == nil || c.Status != metav1.ConditionTrue || c.Reason != catalogModeCluster {
		t.Errorf("CatalogMode = %+v", c)
	}
	if c := meta.FindStatusCondition(policy.Status.Conditions, conditionTypeLegacyLatencyClass); c == nil || c.Status != metav1.ConditionTrue || !strings.Contains(c.Message, "kubemoot.ai/size") {
		t.Errorf("LegacyLatencyClass = %+v", c)
	}
	setCatalogConditions(policy, catalogModeCrewLocal)
	if c := meta.FindStatusCondition(policy.Status.Conditions, conditionTypeCatalogMode); c.Status != metav1.ConditionFalse || c.Reason != catalogModeCrewLocal {
		t.Errorf("CatalogMode = %+v", c)
	}
	if c := meta.FindStatusCondition(policy.Status.Conditions, conditionTypeLegacyLatencyClass); c.Status != metav1.ConditionFalse {
		t.Errorf("latencyClass on the crew's own Models is fine: %+v", c)
	}
}

func TestSelectsOnLabelFindsEveryPlace(t *testing.T) {
	sel := func(key string) *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: key, Operator: metav1.LabelSelectorOpExists}}}
	}
	expr := []corev1.NodeSelectorRequirement{{Key: "latencyClass", Operator: corev1.NodeSelectorOpExists}}
	for name, rule := range map[string]aiv1alpha1.SchedulingRule{
		"require matchLabels":      {Require: &metav1.LabelSelector{MatchLabels: map[string]string{"latencyClass": "low"}}},
		"require matchExpressions": {Require: sel("latencyClass")},
		"requireExpressions":       {RequireExpressions: expr},
		"prefer selector":          {Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 1, Selector: sel("latencyClass")}}},
		"prefer expressions":       {Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 1, Expressions: expr}}},
	} {
		p := &aiv1alpha1.CrewSchedulingPolicy{Spec: aiv1alpha1.CrewSchedulingPolicySpec{Rules: []aiv1alpha1.SchedulingRule{rule}}}
		if !selectsOnLabel(p, latencyClassLabel) {
			t.Errorf("%s: not found", name)
		}
		if selectsOnLabel(p, "other") {
			t.Errorf("%s: found a label it does not use", name)
		}
	}
}

// ---- catalog candidates -----------------------------------------------------

func TestProviderCandidatesSkipAbsentAndNonFittingModels(t *testing.T) {
	p := opProvider(selOllama, aiv1alpha1.ProviderTypeOllama, catEntryOf(selModel8, aiv1alpha1.SizeSmall), catEntryOf(candModel14, aiv1alpha1.SizeMedium), catEntryOf("gone", ""))
	p.Status.Models[2].Present = false
	p.Status.Calibration = &aiv1alpha1.ProviderCalibration{Models: []aiv1alpha1.ModelCalibration{{Model: candModel14, Fits: false}, {Model: selModel8, Fits: true}}}
	got := providerCandidates(p)
	if len(got) != 1 || got[0].Spec.Model != selModel8 {
		t.Fatalf("candidates = %+v, want only the small model", got)
	}
	m := got[0]
	if m.Namespace != selOpNS || m.Spec.ProviderRef != selOllama || !m.Status.Ready || m.Labels[aiv1alpha1.LabelSize] != aiv1alpha1.SizeSmall {
		t.Errorf("candidate = %+v", m)
	}
}

func TestModelSourceCrewLocalReplacesTheCatalogAndFlipsWhenDropped(t *testing.T) {
	scheme := agentReconcileScheme(t)
	local := candModel("own", candModel14, true, nil)
	local.Namespace = selNS
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&local, opProvider(selOllama, aiv1alpha1.ProviderTypeOllama, catEntryOf(selModel8, aiv1alpha1.SizeSmall))).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme}
	src, err := r.modelSource(context.Background(), selNS)
	if err != nil || src.Mode != catalogModeCrewLocal || len(src.Models) != 1 || src.Models[0].Spec.Model != candModel14 {
		t.Fatalf("source = %+v err %v, want only the crew's own Model", src, err)
	}
	if err := cli.Delete(context.Background(), &local); err != nil {
		t.Fatal(err)
	}
	src, err = r.modelSource(context.Background(), selNS)
	if err != nil || src.Mode != catalogModeCluster || len(src.Models) != 1 || src.Models[0].Spec.Model != selModel8 {
		t.Fatalf("source = %+v err %v, want the catalog once the crew drops its Models", src, err)
	}
}

func TestModelSourceOperatorNamespaceModelsAreTheCatalogNotACrews(t *testing.T) {
	scheme := agentReconcileScheme(t)
	catalogModel := candModel("cat", candModel14, true, nil)
	catalogModel.Namespace = selOpNS
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&catalogModel).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme}
	src, err := r.modelSource(context.Background(), selOpNS)
	if err != nil || src.Mode != catalogModeCluster {
		t.Fatalf("source = %+v err %v", src, err)
	}
}

// ---- why nothing matched ----------------------------------------------------

func TestNoMatchReasonNamesWhatIsMissing(t *testing.T) {
	rule := &aiv1alpha1.SchedulingRule{Phase: phaseMulling, Require: &metav1.LabelSelector{MatchLabels: map[string]string{aiv1alpha1.LabelSize: aiv1alpha1.SizeLarge}}}
	catalog := modelSource{Mode: catalogModeCluster, Models: []aiv1alpha1.Model{{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}}}
	got := noMatchReason(phaseMulling, rule, catalog, selNS)
	for _, want := range []string{"cluster catalog", "kubemoot.ai/size=large", "1 model(s) have no known size"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q lacks %q", got, want)
		}
	}
	if got := noMatchReason(phaseMulling, rule, modelSource{Mode: catalogModeCluster}, selNS); !strings.Contains(got, "no available model") {
		t.Errorf("empty catalog: %q", got)
	}
	local := modelSource{Mode: catalogModeCrewLocal}
	if got := noMatchReason(phaseMulling, rule, local, selNS); got != "no Ready Model matches phase=mulling require selectors" {
		t.Errorf("crew-local with a require keeps the old message, got %q", got)
	}
	if got := noMatchReason(phaseMulling, nil, local, selNS); got != "no Ready Model with a Ready ModelProvider in namespace crew-ns" {
		t.Errorf("crew-local without a require keeps the old message, got %q", got)
	}
	exprRule := &aiv1alpha1.SchedulingRule{RequireExpressions: []corev1.NodeSelectorRequirement{{Key: selCtxKey, Operator: corev1.NodeSelectorOpGt, Values: []string{"16383"}}}}
	if got := noMatchReason(phaseMulling, exprRule, catalog, selNS); !strings.Contains(got, selCtxKey+" Gt [16383]") {
		t.Errorf("expressions are named: %q", got)
	}
}

// ---- propagation ------------------------------------------------------------

func TestCatalogDigestIgnoresProbeNoiseButSeesLabelsAndReadiness(t *testing.T) {
	base := opProvider(selOllama, aiv1alpha1.ProviderTypeOllama, catEntryOf(selModel8, aiv1alpha1.SizeSmall))
	noisy := base.DeepCopy()
	noisy.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{VRAMUsedMiB: 9000, AgentCount: 3}
	if catalogDigest(base) != catalogDigest(noisy) {
		t.Error("capacity changes on every probe and must not change the digest")
	}
	relabeled := base.DeepCopy()
	relabeled.Status.Models[0].Labels[aiv1alpha1.LabelSize] = aiv1alpha1.SizeLarge
	notReady := base.DeepCopy()
	notReady.Status.Ready = false
	absent := base.DeepCopy()
	absent.Status.Models[0].Present = false
	bigger := base.DeepCopy()
	bigger.Status.Models[0].VRAMMib = 24000
	if catalogDigest(base) != catalogDigest(notReady) {
		t.Error("one failed probe flips readiness; it must not change the digest or the pods bound to the provider would roll twice")
	}
	for name, p := range map[string]*aiv1alpha1.ModelProvider{"relabeled": relabeled, "absent": absent, "vram": bigger} {
		if catalogDigest(base) == catalogDigest(p) {
			t.Errorf("%s must change the digest", name)
		}
	}
	pred := catalogChanged()
	if pred.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: noisy}) || !pred.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: relabeled}) {
		t.Error("the predicate follows the digest")
	}
	if pred.Generic(event.GenericEvent{Object: base}) {
		t.Error("generic events are ignored")
	}
}

func TestCatalogAgentRequestsReachCrewsByProviderNamespace(t *testing.T) {
	scheme := agentReconcileScheme(t)
	mk := func(ns, name string) *aiv1alpha1.Agent {
		return &aiv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mk(selNS, "a"), mk("other-ns", "b"), mk(selOpNS, "c")).Build()
	cluster := opProvider(selOllama, aiv1alpha1.ProviderTypeOllama)
	if got := catalogAgentRequests(context.Background(), cli, cluster); len(got) != 3 {
		t.Errorf("a provider in the operator's namespace reaches every crew, got %d", len(got))
	}
	local := &aiv1alpha1.ModelProvider{ObjectMeta: metav1.ObjectMeta{Namespace: selNS, Name: selOwnProvider}}
	if got := catalogAgentRequests(context.Background(), cli, local); len(got) != 1 || got[0].Namespace != selNS {
		t.Errorf("a crew's own provider reaches only that namespace, got %+v", got)
	}
	if got := catalogAgentRequests(context.Background(), cli, &aiv1alpha1.Agent{}); got != nil {
		t.Errorf("a non-provider maps to %+v", got)
	}
}
