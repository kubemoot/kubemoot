package controller

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestPreferFasterOf(t *testing.T) {
	if preferFasterOf(nil) != 0 || preferFasterOf(&aiv1alpha1.SchedulingRule{}) != 0 {
		t.Error("no rule or no value is off")
	}
	if got := preferFasterOf(&aiv1alpha1.SchedulingRule{PreferFaster: 40}); got != 40 {
		t.Errorf("preferFaster = %d", got)
	}
}

// The rule's preferFaster reaches the runtime per phase as an env var, with the
// candidate list; a phase without it carries only its list.
func TestCandidateEnvVarsCarryPreferFasterPerPhase(t *testing.T) {
	withBoth := &modelPick{Candidates: []modelCandidate{{Model: candModel14, Score: 50}}, PreferFaster: 50}
	listOnly := &modelPick{Candidates: []modelCandidate{{Model: selModel8, Score: 40}}}
	env := candidateEnvVars(withBoth, listOnly)
	if v, ok := envValue(env, envModelPreferFasterMulling); !ok || v != "50" {
		t.Errorf("%s = %q %v", envModelPreferFasterMulling, v, ok)
	}
	if _, ok := envValue(env, envModelPreferFasterTriage); ok {
		t.Errorf("%s must be absent when the rule sets none", envModelPreferFasterTriage)
	}
	if _, ok := envValue(env, envModelCandidatesTriage); !ok {
		t.Errorf("%s missing", envModelCandidatesTriage)
	}
	// A pick without candidates but with preferFaster still hands the number over.
	if v, ok := envValue(candidateEnvVars(nil, &modelPick{PreferFaster: 10}), envModelPreferFasterTriage); !ok || v != "10" {
		t.Errorf("triage preferFaster = %q %v", v, ok)
	}
	if got := candidateEnvVars(nil, nil); len(got) != 0 {
		t.Errorf("nothing to say: %+v", got)
	}
}

func TestPickModelCarriesTheRulesPreferFaster(t *testing.T) {
	policy := pilotPolicy()
	policy.Spec.Rules[1].PreferFaster = 60
	r, _ := catalogReconciler(t,
		opProvider(selOllama, aiv1alpha1.ProviderTypeOllama, catEntryOf(selModel8, aiv1alpha1.SizeSmall, testToolCalling)), policy)
	agent := pilotAgent(candAgent)
	mulling, err := r.pickModel(context.Background(), agent, phaseMulling)
	if err != nil || mulling.PreferFaster != 0 {
		t.Fatalf("mulling = %+v %v", mulling, err)
	}
	triage, err := r.pickModel(context.Background(), agent, phaseTriage)
	if err != nil || triage.PreferFaster != 60 {
		t.Fatalf("triage = %+v %v", triage, err)
	}
	env := r.buildEnvVars(context.Background(), agent, mulling, triage, 8080)
	if v, ok := envValue(env, envModelPreferFasterTriage); !ok || v != "60" {
		t.Errorf("%s = %q %v", envModelPreferFasterTriage, v, ok)
	}
}

var _ = Describe("CrewSchedulingPolicy preferFaster admission", func() {
	newCSP := func(name string, faster int32) *aiv1alpha1.CrewSchedulingPolicy {
		return &aiv1alpha1.CrewSchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: aiv1alpha1.CrewSchedulingPolicySpec{CrewRef: "c", Rules: []aiv1alpha1.SchedulingRule{
				{Phase: phaseTriage, PreferFaster: faster},
			}},
		}
	}

	It("accepts preferFaster from 0 to 100", func() {
		for name, v := range map[string]int32{"adm-faster-0": 0, "adm-faster-50": 50, "adm-faster-100": 100} {
			p := newCSP(name, v)
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, p) })
		}
	})

	It("rejects preferFaster above 100 and below 0", func() {
		Expect(k8sClient.Create(ctx, newCSP("adm-faster-101", 101))).NotTo(Succeed())
		Expect(k8sClient.Create(ctx, newCSP("adm-faster-neg", -1))).NotTo(Succeed())
	})
})
