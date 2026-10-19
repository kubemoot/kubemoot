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

package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// nodeOperators maps the NodeSelectorRequirement operators a scheduling rule may
// use to label selection operators. Gt and Lt are strict integer comparisons, as
// in node affinity.
var nodeOperators = map[corev1.NodeSelectorOperator]selection.Operator{
	corev1.NodeSelectorOpIn:           selection.In,
	corev1.NodeSelectorOpNotIn:        selection.NotIn,
	corev1.NodeSelectorOpExists:       selection.Exists,
	corev1.NodeSelectorOpDoesNotExist: selection.DoesNotExist,
	corev1.NodeSelectorOpGt:           selection.GreaterThan,
	corev1.NodeSelectorOpLt:           selection.LessThan,
}

// RequirementOf turns a scheduling rule's NodeSelectorRequirement into a label
// requirement that can be matched against a model's labels. It fails on an unknown
// operator, on In or NotIn without values, on Exists or DoesNotExist with values,
// and on Gt or Lt without exactly one integer value. The admission webhook and the
// scheduler share it, so what is admitted is what is matched.
func RequirementOf(r corev1.NodeSelectorRequirement) (labels.Requirement, error) {
	op, ok := nodeOperators[r.Operator]
	if !ok {
		return labels.Requirement{}, fmt.Errorf("key %s: operator %q is not one of In, NotIn, Exists, DoesNotExist, Gt, Lt", r.Key, r.Operator)
	}
	req, err := labels.NewRequirement(r.Key, op, r.Values)
	if err != nil {
		return labels.Requirement{}, fmt.Errorf("key %s: %w", r.Key, err)
	}
	return *req, nil
}
