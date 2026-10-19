package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func TestRequirementOfAcceptsEveryOperator(t *testing.T) {
	for _, r := range []corev1.NodeSelectorRequirement{
		{Key: "k", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}},
		{Key: "k", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"a"}},
		{Key: "k", Operator: corev1.NodeSelectorOpExists},
		{Key: "k", Operator: corev1.NodeSelectorOpDoesNotExist},
		{Key: "k", Operator: corev1.NodeSelectorOpGt, Values: []string{"5"}},
		{Key: "k", Operator: corev1.NodeSelectorOpLt, Values: []string{"5"}},
	} {
		if _, err := RequirementOf(r); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
}

func TestRequirementOfRejectsMalformedRequirements(t *testing.T) {
	for name, r := range map[string]corev1.NodeSelectorRequirement{
		"unknown operator":        {Key: "k", Operator: "Between", Values: []string{"1"}},
		"empty operator":          {Key: "k"},
		"Gt with text":            {Key: "k", Operator: corev1.NodeSelectorOpGt, Values: []string{"many"}},
		"Gt with a decimal":       {Key: "k", Operator: corev1.NodeSelectorOpGt, Values: []string{"1.5"}},
		"Lt with two values":      {Key: "k", Operator: corev1.NodeSelectorOpLt, Values: []string{"1", "2"}},
		"In without values":       {Key: "k", Operator: corev1.NodeSelectorOpIn},
		"Exists with values":      {Key: "k", Operator: corev1.NodeSelectorOpExists, Values: []string{"x"}},
		"an invalid label key":    {Key: "not a key!", Operator: corev1.NodeSelectorOpExists},
		"an invalid label value":  {Key: "k", Operator: corev1.NodeSelectorOpIn, Values: []string{"bad value!"}},
		"DoesNotExist and values": {Key: "k", Operator: corev1.NodeSelectorOpDoesNotExist, Values: []string{"x"}},
	} {
		if _, err := RequirementOf(r); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestRequirementOfMatchesStrictly(t *testing.T) {
	gt, err := RequirementOf(corev1.NodeSelectorRequirement{Key: "ctx", Operator: corev1.NodeSelectorOpGt, Values: []string{"16383"}})
	if err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]bool{"16384": true, "32768": true, "16383": false, "100": false, "not-a-number": false} {
		if got := gt.Matches(labels.Set{"ctx": value}); got != want {
			t.Errorf("ctx=%s: %v, want %v", value, got, want)
		}
	}
	if gt.Matches(labels.Set{}) {
		t.Error("a missing label must fail Gt")
	}
}
