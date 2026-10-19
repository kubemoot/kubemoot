package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The API server's own checks on the new scheduling fields: the schema accepts them
// and a preference needs a selector or expressions. The operator and value checks on
// the expressions are the admission webhook's, tested in the webhook package.
var _ = Describe("CrewSchedulingPolicy admission", func() {
	newCSP := func(name string, rule aiv1alpha1.SchedulingRule) *aiv1alpha1.CrewSchedulingPolicy {
		rule.Phase = phaseMulling
		return &aiv1alpha1.CrewSchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec:       aiv1alpha1.CrewSchedulingPolicySpec{CrewRef: "c", Rules: []aiv1alpha1.SchedulingRule{rule}},
		}
	}
	create := func(p *aiv1alpha1.CrewSchedulingPolicy) error {
		err := k8sClient.Create(ctx, p)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, p) })
		}
		return err
	}
	gt := []corev1.NodeSelectorRequirement{{Key: "kubemoot.ai/context-tokens", Operator: corev1.NodeSelectorOpGt, Values: []string{"16383"}}}

	It("accepts requireExpressions, a preference by expressions, and abilityWeight", func() {
		weight := int32(30)
		p := newCSP("adm-csp-expr", aiv1alpha1.SchedulingRule{
			RequireExpressions: gt,
			Prefer:             []aiv1alpha1.PreferenceTerm{{Weight: 10, Expressions: gt}},
		})
		p.Spec.AbilityWeight = &weight
		Expect(create(p)).To(Succeed())
	})

	It("still accepts a preference with only a selector", func() {
		p := newCSP("adm-csp-sel", aiv1alpha1.SchedulingRule{
			Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 10, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}}},
		})
		Expect(create(p)).To(Succeed())
	})

	It("rejects a preference with neither selector nor expressions", func() {
		err := create(newCSP("adm-csp-empty", aiv1alpha1.SchedulingRule{Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 10}}}))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("a preference needs a selector or expressions"))
	})

	It("rejects an abilityWeight above 100", func() {
		weight := int32(101)
		p := newCSP("adm-csp-weight", aiv1alpha1.SchedulingRule{})
		p.Spec.AbilityWeight = &weight
		Expect(create(p)).NotTo(Succeed())
	})
})
