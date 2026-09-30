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
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestComputeVerdict_EmptyTrials_Untested(t *testing.T) {
	r := &MCPServerReportReconciler{}
	verdict := r.computeVerdict(nil)
	if verdict != string(kubemootv1alpha1.VerdictUntested) {
		t.Errorf("got %s, want untested", verdict)
	}
}

func TestComputeVerdict_AllRecentSuccess_Use(t *testing.T) {
	r := &MCPServerReportReconciler{}
	trials := []kubemootv1alpha1.TrialRecord{
		{Success: true},
		{Success: true},
		{Success: true},
	}
	verdict := r.computeVerdict(trials)
	if verdict != string(kubemootv1alpha1.VerdictUse) {
		t.Errorf("got %s, want use", verdict)
	}
}

func TestComputeVerdict_AllRecentFailure_Avoid(t *testing.T) {
	r := &MCPServerReportReconciler{}
	trials := []kubemootv1alpha1.TrialRecord{
		{Success: false},
		{Success: false},
		{Success: false},
	}
	verdict := r.computeVerdict(trials)
	if verdict != string(kubemootv1alpha1.VerdictAvoid) {
		t.Errorf("got %s, want avoid", verdict)
	}
}

func TestComputeVerdict_MixedRecent_Caution(t *testing.T) {
	r := &MCPServerReportReconciler{}
	trials := []kubemootv1alpha1.TrialRecord{
		{Success: true},
		{Success: false},
		{Success: true},
	}
	verdict := r.computeVerdict(trials)
	if verdict != string(kubemootv1alpha1.VerdictCaution) {
		t.Errorf("got %s, want caution", verdict)
	}
}

func TestComputeVerdict_LooksAtLastThreeOnly(t *testing.T) {
	r := &MCPServerReportReconciler{}
	// 5 old failures + 3 recent successes → should be "use"
	trials := []kubemootv1alpha1.TrialRecord{
		{Success: false},
		{Success: false},
		{Success: false},
		{Success: false},
		{Success: false},
		{Success: true},
		{Success: true},
		{Success: true},
	}
	verdict := r.computeVerdict(trials)
	if verdict != string(kubemootv1alpha1.VerdictUse) {
		t.Errorf("got %s, want use (should only look at last 3)", verdict)
	}
}

func TestComputeVerdict_OneTrial_Success(t *testing.T) {
	r := &MCPServerReportReconciler{}
	trials := []kubemootv1alpha1.TrialRecord{
		{Success: true},
	}
	verdict := r.computeVerdict(trials)
	if verdict != string(kubemootv1alpha1.VerdictUse) {
		t.Errorf("got %s, want use", verdict)
	}
}

func TestComputeVerdict_OneTrial_Failure(t *testing.T) {
	r := &MCPServerReportReconciler{}
	trials := []kubemootv1alpha1.TrialRecord{
		{Success: false},
	}
	verdict := r.computeVerdict(trials)
	if verdict != string(kubemootv1alpha1.VerdictAvoid) {
		t.Errorf("got %s, want avoid", verdict)
	}
}
