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

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestParseSuccessRate(t *testing.T) {
	cases := map[string]float64{
		"85%":    0.85,
		"100%":   1,
		" 50% ":  0.5,
		"72":     0.72,
		"N/A":    0,
		"":       0,
		"lots%":  0,
		"12.5%%": 0,
	}
	for in, want := range cases {
		if got := parseSuccessRate(in); got != want {
			t.Errorf("parseSuccessRate(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTestedMinSuccessRate(t *testing.T) {
	cases := map[string]float64{
		"":      defaultTestedMinSuccessRate,
		"0.65":  0.65,
		"0":     0,
		"seven": defaultTestedMinSuccessRate,
	}
	for in, want := range cases {
		if got := testedMinSuccessRate(in); got != want {
			t.Errorf("testedMinSuccessRate(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestEvaluateTestedTier(t *testing.T) {
	const server = "tier-probe"
	const minRate = "0.9"
	report := func(verdict kubemootv1alpha1.MCPServerReportVerdict, rate string) *kubemootv1alpha1.MCPServerReport {
		return &kubemootv1alpha1.MCPServerReport{
			ObjectMeta: metav1.ObjectMeta{Name: sanitizeK8sName(server), Namespace: "tier-ns"},
			Status:     kubemootv1alpha1.MCPServerReportStatus{Verdict: string(verdict), FailureCount: 4, SuccessRate: rate},
		}
	}
	cases := []struct {
		name        string
		report      *kubemootv1alpha1.MCPServerReport
		blockBroken *bool
		minRate     string
		want        string
	}{
		{"no report passes through", nil, nil, "", ""},
		{"avoid blocked by default", report(kubemootv1alpha1.VerdictAvoid, "10%"), nil, "", policyActionDeny},
		{"avoid blocked when enabled", report(kubemootv1alpha1.VerdictAvoid, "10%"), ptr.To(true), "", policyActionDeny},
		{"avoid passes when blocking is off", report(kubemootv1alpha1.VerdictAvoid, "10%"), ptr.To(false), "", ""},
		{"use at the minimum allows", report(kubemootv1alpha1.VerdictUse, "90%"), nil, minRate, policyActionAllow},
		{"use above the minimum allows", report(kubemootv1alpha1.VerdictUse, "95%"), nil, minRate, policyActionAllow},
		{"use below the minimum passes", report(kubemootv1alpha1.VerdictUse, "89%"), nil, minRate, ""},
		{"use below the default minimum passes", report(kubemootv1alpha1.VerdictUse, "79%"), nil, "", ""},
		{"use with an unparsable rate passes", report(kubemootv1alpha1.VerdictUse, "N/A"), nil, "", ""},
		{"caution passes through", report(kubemootv1alpha1.VerdictCaution, "99%"), nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(agentReconcileScheme(t))
			if tc.report != nil {
				builder = builder.WithObjects(tc.report)
			}
			policy := &kubemootv1alpha1.MCPQualityPolicy{
				ObjectMeta: metav1.ObjectMeta{Namespace: "tier-ns"},
				Spec: kubemootv1alpha1.MCPQualityPolicySpec{
					Tested: &kubemootv1alpha1.TestedConfig{Enabled: true, BlockBroken: tc.blockBroken, MinSuccessRate: tc.minRate},
				},
			}
			got := evaluateTestedTier(context.Background(), builder.Build(), policy, server)
			if got.Action != tc.want {
				t.Errorf("action = %q (%s), want %q", got.Action, got.Reason, tc.want)
			}
		})
	}
}
