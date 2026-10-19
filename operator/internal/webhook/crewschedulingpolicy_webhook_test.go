package webhook

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	ctxKey  = "kubemoot.ai/context-tokens"
	reqPath = "requireExpressions[0]"
)

func policyWith(rule aiv1alpha1.SchedulingRule) *aiv1alpha1.CrewSchedulingPolicy {
	rule.Phase = "mulling"
	return &aiv1alpha1.CrewSchedulingPolicy{Spec: aiv1alpha1.CrewSchedulingPolicySpec{CrewRef: "c", Rules: []aiv1alpha1.SchedulingRule{rule}}}
}

func req(op corev1.NodeSelectorOperator, values ...string) []corev1.NodeSelectorRequirement {
	return []corev1.NodeSelectorRequirement{{Key: ctxKey, Operator: op, Values: values}}
}

func TestValidateCrewSchedulingPolicyAdmitsGoodExpressions(t *testing.T) {
	for name, rule := range map[string]aiv1alpha1.SchedulingRule{
		"no expressions at all":  {},
		"Gt with an integer":     {RequireExpressions: req(corev1.NodeSelectorOpGt, "16383")},
		"Lt with an integer":     {RequireExpressions: req(corev1.NodeSelectorOpLt, "65536")},
		"In with values":         {RequireExpressions: req(corev1.NodeSelectorOpIn, "a", "b")},
		"Exists without values":  {RequireExpressions: req(corev1.NodeSelectorOpExists)},
		"a prefer by expression": {Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 5, Expressions: req(corev1.NodeSelectorOpGt, "1")}}},
		"a prefer by selector":   {Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 5, Selector: &metav1.LabelSelector{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateCrewSchedulingPolicy(policyWith(rule)); err != nil {
				t.Errorf("rejected: %v", err)
			}
		})
	}
}

func TestValidateCrewSchedulingPolicyRejectsBadExpressions(t *testing.T) {
	for name, tc := range map[string]struct {
		rule aiv1alpha1.SchedulingRule
		want string
	}{
		"Gt with text":        {aiv1alpha1.SchedulingRule{RequireExpressions: req(corev1.NodeSelectorOpGt, "many")}, reqPath},
		"Gt with two values":  {aiv1alpha1.SchedulingRule{RequireExpressions: req(corev1.NodeSelectorOpGt, "1", "2")}, "exactly one"},
		"Lt without a value":  {aiv1alpha1.SchedulingRule{RequireExpressions: req(corev1.NodeSelectorOpLt)}, reqPath},
		"an unknown operator": {aiv1alpha1.SchedulingRule{RequireExpressions: req("Between", "1")}, "not one of"},
		"In without values":   {aiv1alpha1.SchedulingRule{RequireExpressions: req(corev1.NodeSelectorOpIn)}, reqPath},
		"Exists with values":  {aiv1alpha1.SchedulingRule{RequireExpressions: req(corev1.NodeSelectorOpExists, "x")}, reqPath},
		"a bad prefer":        {aiv1alpha1.SchedulingRule{Prefer: []aiv1alpha1.PreferenceTerm{{Weight: 5, Expressions: req(corev1.NodeSelectorOpGt, "x")}}}, "prefer[0].expressions[0]"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateCrewSchedulingPolicy(policyWith(tc.rule))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestCrewSchedulingPolicyValidatorCreateUpdateDelete(t *testing.T) {
	v := &CrewSchedulingPolicyValidator{}
	bad := policyWith(aiv1alpha1.SchedulingRule{RequireExpressions: req(corev1.NodeSelectorOpGt, "x")})
	good := policyWith(aiv1alpha1.SchedulingRule{})
	ctx := context.Background()
	if _, err := v.ValidateCreate(ctx, bad); err == nil {
		t.Error("create must reject")
	}
	if _, err := v.ValidateUpdate(ctx, good, bad); err == nil {
		t.Error("update must reject")
	}
	if _, err := v.ValidateUpdate(ctx, bad, good); err != nil {
		t.Errorf("an update that fixes the policy must pass: %v", err)
	}
	if _, err := v.ValidateDelete(ctx, bad); err != nil {
		t.Errorf("delete must always pass: %v", err)
	}
}
