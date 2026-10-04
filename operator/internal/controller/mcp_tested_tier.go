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
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Policy decision actions shared by the MCP catalog and gateway quality tiers.
const (
	policyActionAllow = "allow"
	policyActionDeny  = "deny"
)

// defaultTestedMinSuccessRate is the success rate a "use" verdict needs when the
// policy does not set tested.minSuccessRate.
const defaultTestedMinSuccessRate = 0.8

// evaluateTestedTier decides on a server from its MCPServerReport: an "avoid"
// verdict is denied when the policy blocks broken servers, a "use" verdict with a
// success rate at or above the policy minimum is allowed, and anything else (no
// report, caution, too little data) returns an empty decision so the next tier runs.
func evaluateTestedTier(ctx context.Context, c client.Reader, policy *kubemootv1alpha1.MCPQualityPolicy, serverName string) PolicyDecision {
	report := &kubemootv1alpha1.MCPServerReport{}
	if err := c.Get(ctx, client.ObjectKey{
		Namespace: policy.Namespace,
		Name:      sanitizeK8sName(serverName),
	}, report); err != nil {
		return PolicyDecision{}
	}

	tested := policy.Spec.Tested
	if tested.BlockBrokenEnabled() && report.Status.Verdict == string(kubemootv1alpha1.VerdictAvoid) {
		logf.FromContext(ctx).Info("Blocking server with avoid verdict", "server", serverName)
		return PolicyDecision{
			Action:     policyActionDeny,
			Confidence: 1.0,
			Reason:     fmt.Sprintf("tested: verdict=avoid, %d failures recorded", report.Status.FailureCount),
		}
	}

	if report.Status.Verdict != string(kubemootv1alpha1.VerdictUse) {
		return PolicyDecision{}
	}
	if parseSuccessRate(report.Status.SuccessRate) < testedMinSuccessRate(tested.MinSuccessRate) {
		return PolicyDecision{}
	}
	return PolicyDecision{
		Action:     policyActionAllow,
		Confidence: 0.95,
		Reason:     fmt.Sprintf("tested: verdict=use, success rate %s", report.Status.SuccessRate),
	}
}

// testedMinSuccessRate parses the policy's minimum success rate (a fraction such as
// "0.9"), falling back to defaultTestedMinSuccessRate when unset or unparsable.
func testedMinSuccessRate(raw string) float64 {
	if raw == "" {
		return defaultTestedMinSuccessRate
	}
	parsed, err := parseFloat(raw)
	if err != nil {
		return defaultTestedMinSuccessRate
	}
	return parsed
}

// parseSuccessRate converts a report success rate such as "85%" into a fraction
// (0.85). "N/A" and any other value that is not a number read as 0.
func parseSuccessRate(raw string) float64 {
	pct, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%")), 64)
	if err != nil {
		return 0
	}
	return pct / 100
}
