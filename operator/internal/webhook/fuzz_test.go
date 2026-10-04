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

package webhook

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// FuzzValidateCrewName checks the crew-name rule against Kubernetes' own
// validation: a name is accepted exactly when it is a DNS-1123 label of at most
// 58 characters, and every object name the operator derives from an accepted
// name is itself valid.
func FuzzValidateCrewName(f *testing.F) {
	for _, s := range []string{
		validCrewName, "homelab-pilot", "Hello-World", "hello_world", "-pilot", "pilot-", "a",
		"this-crew-name-is-way-too-long-and-exceeds-the-fifty-eight-char", "pilot\n", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		_, err := validateCrew(&aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: name}})
		want := len(name) <= 58 && len(validation.IsDNS1123Label(name)) == 0
		if (err == nil) != want {
			t.Fatalf("validateCrew(%q) error = %v, Kubernetes label rule says accept=%v", name, err, want)
		}
		if err != nil {
			return
		}
		if errs := validation.IsDNS1123Label("crew-" + name); len(errs) > 0 {
			t.Fatalf("crew-%s is not a DNS-1123 label: %v", name, errs)
		}
		for _, derived := range []string{"crew-" + name + "-skills", "crew-" + name + "-resumes", "crew-" + name + "-discussion"} {
			if errs := validation.IsDNS1123Subdomain(derived); len(errs) > 0 {
				t.Fatalf("derived name %q is invalid: %v", derived, errs)
			}
		}
	})
}

// FuzzValidateCrewFitness checks the CrewFitness rules agree with themselves:
// an accepted spec has its required references and exactly one test source, and
// once the run completes an unchanged spec stays valid while a changed one is
// rejected.
func FuzzValidateCrewFitness(f *testing.F) {
	f.Add("pilot", "k8s-pod-status", "", "DESCRIPTION pods\nASSERT(synthesis is non-empty)", int64(time.Hour), "pilot2")
	f.Add("pilot", "smoke", "fitness-tests", "", int64(0), "pilot")
	f.Add("", "smoke", "cm", "x", int64(-1), "")
	f.Fuzz(func(t *testing.T, crewRef, testRef, cmRef, content string, ttl int64, otherCrew string) {
		cf := &aiv1alpha1.CrewFitness{Spec: aiv1alpha1.CrewFitnessSpec{
			CrewRef: crewRef, TestRef: testRef, ConfigMapRef: cmRef, TestContent: content,
		}}
		if ttl != 0 {
			cf.Spec.TTL = &metav1.Duration{Duration: time.Duration(ttl)}
		}
		if _, err := validateCrewFitness(cf); err != nil {
			return
		}
		checkAcceptedFitness(t, cf)
		checkCompletedImmutable(t, cf, otherCrew)
	})
}

// checkAcceptedFitness asserts the invariants of an accepted CrewFitness spec.
func checkAcceptedFitness(t *testing.T, cf *aiv1alpha1.CrewFitness) {
	t.Helper()
	s := cf.Spec
	if s.CrewRef == "" || s.TestRef == "" || (s.ConfigMapRef == "") == (s.TestContent == "") {
		t.Fatalf("accepted spec breaks the reference rules: %+v", s)
	}
	if s.TTL != nil && s.TTL.Duration <= 0 {
		t.Fatalf("accepted non-positive ttl %v", s.TTL.Duration)
	}
}

// checkCompletedImmutable asserts a completed run keeps its spec: the same spec
// updates cleanly, a different crewRef does not.
func checkCompletedImmutable(t *testing.T, cf *aiv1alpha1.CrewFitness, otherCrew string) {
	t.Helper()
	v := &CrewFitnessValidator{}
	old := cf.DeepCopy()
	old.Status.Phase = aiv1alpha1.CrewFitnessPhasePassed
	if _, err := v.ValidateUpdate(context.Background(), old, cf.DeepCopy()); err != nil {
		t.Fatalf("unchanged spec rejected after completion: %v", err)
	}
	changed := cf.DeepCopy()
	changed.Spec.CrewRef = otherCrew
	if _, err := v.ValidateUpdate(context.Background(), old, changed); otherCrew != cf.Spec.CrewRef && err == nil {
		t.Fatalf("crewRef change %q -> %q accepted after completion", cf.Spec.CrewRef, otherCrew)
	}
}

// FuzzValidateRAGSourceGitPaths checks the git-paths rule: a source is accepted
// exactly when no path carries a glob character.
func FuzzValidateRAGSourceGitPaths(f *testing.F) {
	f.Add("docs", "kubemoot/docs")
	f.Add("docs/**/*.md", "README.md")
	f.Add("a[b]", "{x,y}")
	f.Fuzz(func(t *testing.T, p1, p2 string) {
		rs := &aiv1alpha1.RAGSource{Spec: aiv1alpha1.RAGSourceSpec{Source: aiv1alpha1.SourceConfig{
			Type: aiv1alpha1.RAGSourceTypeGit,
			Git:  &aiv1alpha1.GitSource{Paths: []string{p1, p2}},
		}}}
		_, err := validateRAGSource(rs)
		globbed := strings.ContainsAny(p1, globChars) || strings.ContainsAny(p2, globChars)
		if (err != nil) != globbed {
			t.Fatalf("paths %q %q: error = %v, glob present = %v", p1, p2, err, globbed)
		}
	})
}
