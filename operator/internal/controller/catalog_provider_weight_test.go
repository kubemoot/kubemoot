package controller

import (
	"context"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// pilotCatalog is a catalog of three providers: an Ollama server with the 8B model and
// scheduling weight 50, and two vLLM servers with no weight, one holding the 32B model
// and one the 14B.
func pilotCatalog() []*aiv1alpha1.ModelProvider {
	both := []string{testToolCalling, candReasoning}
	cpu := opProvider("ollama-cpu", aiv1alpha1.ProviderTypeOllama, catEntryOf(selModel8, aiv1alpha1.SizeSmall, both...))
	cpu.Spec.Scheduling = &aiv1alpha1.ProviderScheduling{Weight: 50}
	rig0 := opProvider("vllm-rig0", aiv1alpha1.ProviderTypeVLLM, catEntryOf(candModel32, aiv1alpha1.SizeLarge, both...))
	rig1 := opProvider("vllm-rig1", aiv1alpha1.ProviderTypeVLLM, catEntryOf(candModel14, aiv1alpha1.SizeMedium, both...))
	return []*aiv1alpha1.ModelProvider{cpu, rig0, rig1}
}

// pilotObjects is the catalog's providers and the pilot's policy with triage on preferFaster.
func pilotObjects() []client.Object {
	providers := pilotCatalog()
	objs := make([]client.Object, 0, len(providers)+1)
	objs = append(objs, pilotComputePolicy(aiv1alpha1.SchedulingRule{PreferFaster: 50}))
	for _, p := range providers {
		objs = append(objs, p)
	}
	return objs
}

func pilotComputePolicy(triage aiv1alpha1.SchedulingRule) *aiv1alpha1.CrewSchedulingPolicy {
	toolCalling := &metav1.LabelSelector{MatchLabels: map[string]string{selToolLabel: valueTrue}}
	triage.Phase = phaseTriage
	triage.Require = toolCalling
	return &aiv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pilot-scheduling", Namespace: selNS},
		Spec: aiv1alpha1.CrewSchedulingPolicySpec{
			CrewRef:     selCrew,
			QualityBias: map[string]string{candReasoning: fuzzBiasSeed},
			Rules:       []aiv1alpha1.SchedulingRule{{Phase: phaseMulling, Require: toolCalling}, triage},
		},
	}
}

// A provider's scheduling weight does not outrank the model the policy ranks higher:
// the 8B model on the weight-50 server scores 70 under the policy and the 32B 110, so
// the pick is the 32B although summing the weight would give the 8B 120. The agent
// runtime keeps the bound model as the crew's choice, so the bound model must be the
// policy's.
func TestCatalogPickBindsTheModelThePolicyRanksHighestNotTheHeaviestProvider(t *testing.T) {
	r, _ := catalogReconciler(t, pilotObjects()...)
	agent := pilotAgent("compute", testToolCalling, candReasoning)

	pick, err := r.pickModel(context.Background(), agent, phaseMulling)
	if err != nil {
		t.Fatal(err)
	}
	if pick.ModelID != candModel32 || pick.Provider.Name != "vllm-rig0" {
		t.Errorf("mulling bound to %s on %s, want qwen3:32b on vllm-rig0", pick.ModelID, pick.Provider.Name)
	}
	want := []modelCandidate{{Model: candModel32, Score: 110}, {Model: candModel14, Score: 100}, {Model: selModel8, Score: 70}}
	if len(pick.Candidates) != 3 || pick.Candidates[0] != want[0] || pick.Candidates[1] != want[1] || pick.Candidates[2] != want[2] {
		t.Errorf("candidates = %+v, want %+v", pick.Candidates, want)
	}
}

// Guard: triage scores every model 0 (preferFaster alone), so the provider weight
// decides among them as a tie-break, and a triage weight lowers a server's rank: the
// weighted server is not the pick.
func TestCatalogTriagePickIsNotTheWeightedCPUServer(t *testing.T) {
	r, _ := catalogReconciler(t, pilotObjects()...)
	pick, err := r.pickModel(context.Background(), pilotAgent("compute", testToolCalling, candReasoning), phaseTriage)
	if err != nil {
		t.Fatal(err)
	}
	if pick.Provider.Name == "ollama-cpu" || pick.PreferFaster != 50 {
		t.Errorf("triage bound to %s on %s (preferFaster %d)", pick.ModelID, pick.Provider.Name, pick.PreferFaster)
	}
	for _, c := range pick.Candidates {
		if c.Score != 0 {
			t.Errorf("a preferFaster-only rule scores every model 0, got %+v", pick.Candidates)
		}
	}
}

// Guard: among providers that carry the same model the policy score is equal, so the
// weight still decides: the heavier provider for mulling, the lighter for triage.
func TestCatalogWeightStillChoosesTheProviderForTheSameModel(t *testing.T) {
	both := []string{testToolCalling, candReasoning}
	light := opProvider("rig-light", aiv1alpha1.ProviderTypeOllama, catEntryOf(candModel14, aiv1alpha1.SizeMedium, both...))
	light.Spec.Scheduling = &aiv1alpha1.ProviderScheduling{Weight: 25}
	heavy := opProvider("rig-heavy", aiv1alpha1.ProviderTypeOllama, catEntryOf(candModel14, aiv1alpha1.SizeMedium, both...))
	heavy.Spec.Scheduling = &aiv1alpha1.ProviderScheduling{Weight: 100}
	r, _ := catalogReconciler(t, light, heavy, pilotComputePolicy(aiv1alpha1.SchedulingRule{}))
	agent := pilotAgent("compute", testToolCalling, candReasoning)

	if pick, err := r.pickModel(context.Background(), agent, phaseMulling); err != nil || pick.Provider.Name != "rig-heavy" {
		t.Errorf("mulling pick = %+v err %v, want the heavier provider", pick, err)
	}
	if pick, err := r.pickModel(context.Background(), agent, phaseTriage); err != nil || pick.Provider.Name != "rig-light" {
		t.Errorf("triage pick = %+v err %v, want the lighter provider", pick, err)
	}
}

func TestCatalogBeforeOrdersByPolicyThenTheOldOrder(t *testing.T) {
	withPolicy := func(name string, vram int32, total, policy int64) scheduleCandidate {
		c := cand(name, vram, total)
		c.policy = policy
		return c
	}
	order := func(cs ...scheduleCandidate) []string {
		sort.SliceStable(cs, func(i, j int) bool { return catalogBefore(cs[i], cs[j]) })
		names := make([]string, 0, len(cs))
		for _, c := range cs {
			names = append(names, c.model.Name)
		}
		return names
	}
	// The higher policy score wins whatever the totals (the first has a big weight).
	if got := order(withPolicy("weighted", 0, 120, 70), withPolicy("ranked", 0, 110, 110)); got[0] != "ranked" {
		t.Errorf("order = %v, want the policy's first", got)
	}
	// Equal policy: the total (weight) decides, then the smaller declared memory, then the name.
	if got := order(withPolicy("light", 0, 100, 100), withPolicy("heavy", 0, 150, 100)); got[0] != "heavy" {
		t.Errorf("order = %v, want the weight to decide a policy tie", got)
	}
	if got := order(withPolicy("big", 20000, 100, 100), withPolicy("small", 8000, 100, 100)); got[0] != "small" {
		t.Errorf("order = %v, want the smaller declared memory on a tie", got)
	}
	if got := order(withPolicy("b", 0, 100, 100), withPolicy("a", 0, 100, 100)); got[0] != "a" {
		t.Errorf("order = %v, want the name last", got)
	}
}
