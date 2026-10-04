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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	revTestHash       = "9aae3d6"
	revTestDeployedAt = "2026-09-27T10:00:00Z"
	revTestSource     = "github.com/acme/crews//pilot"
	revTestOwner      = "dev@example.com"
	revTestVersion    = "0.40.3_9aae3d688d39"
)

func revTestCrew(annotations, labels map[string]string) *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{
		Name: testCrewPilot, Namespace: testNamespaceA, Annotations: annotations, Labels: labels,
	}}
}

func crewForgeAnnotations() map[string]string {
	return map[string]string{
		annoCrewForgeSource:     revTestSource,
		annoCrewForgeOwner:      revTestOwner,
		annoCrewForgeRevision:   revTestHash,
		annoCrewForgeChannel:    testBundle,
		annoCrewForgeDeployedAt: revTestDeployedAt,
	}
}

func TestCrewProvenanceReadsAnnotationsAndLabel(t *testing.T) {
	crew := revTestCrew(crewForgeAnnotations(), map[string]string{crewVersionLabel: revTestVersion})
	got := crewProvenance(crew)
	want := namespaceCrewEntry{
		Source: revTestSource, Owner: revTestOwner, Revision: revTestHash,
		Channel: testBundle, CrewVersion: revTestVersion, DeployedAt: revTestDeployedAt,
	}
	if got != want {
		t.Fatalf("crewProvenance = %+v, want %+v", got, want)
	}
	if !crewProvenance(revTestCrew(nil, nil)).isEmpty() {
		t.Fatal("a crew with no annotations or labels must yield an empty entry")
	}
}

func TestRevisionFromProvenance(t *testing.T) {
	now := metav1.Now()
	if _, ok := revisionFromProvenance(namespaceCrewEntry{}, now); ok {
		t.Fatal("no markers must record nothing")
	}
	if _, ok := revisionFromProvenance(namespaceCrewEntry{Source: revTestSource, Owner: revTestOwner, Channel: "helm"}, now); ok {
		t.Fatal("source/owner/channel alone are not revision markers")
	}
	for name, p := range map[string]namespaceCrewEntry{
		"revision":     {Revision: revTestHash},
		"deployed-at":  {DeployedAt: revTestDeployedAt},
		"crew-version": {CrewVersion: revTestVersion},
	} {
		rev, ok := revisionFromProvenance(p, now)
		if !ok || !rev.ObservedAt.Equal(&now) {
			t.Fatalf("%s alone must record a revision observed now, got %+v ok=%v", name, rev, ok)
		}
	}
}

func TestAppendRevisionDedupesAgainstNewest(t *testing.T) {
	first := kubemootv1alpha1.CrewRevision{Revision: "a", DeployedAt: "t1"}
	history, changed := appendRevision(nil, first)
	if !changed || len(history) != 1 {
		t.Fatalf("first append must add, got %v changed=%v", history, changed)
	}
	dup := first
	dup.Owner = testSomeoneElse
	if _, changed := appendRevision(history, dup); changed {
		t.Fatal("same (revision, deployed-at, crew-version) must not append")
	}
	redeploy := kubemootv1alpha1.CrewRevision{Revision: "a", DeployedAt: "t2"}
	history, changed = appendRevision(history, redeploy)
	if !changed || len(history) != 2 || history[0].DeployedAt != "t2" {
		t.Fatalf("a new deployed-at must prepend, got %v", history)
	}
	rollback := kubemootv1alpha1.CrewRevision{Revision: "a", DeployedAt: "t1"}
	if history, _ = appendRevision(history, rollback); len(history) != 3 {
		t.Fatalf("a value matching an older, non-newest entry must still append, got %d", len(history))
	}
}

func TestAppendRevisionCapsNewestFirst(t *testing.T) {
	var history []kubemootv1alpha1.CrewRevision
	for i := range maxCrewRevisions + 5 {
		history, _ = appendRevision(history, kubemootv1alpha1.CrewRevision{Revision: fmt.Sprintf("r%d", i)})
	}
	if len(history) != maxCrewRevisions {
		t.Fatalf("len = %d, want %d", len(history), maxCrewRevisions)
	}
	if history[0].Revision != "r14" || history[maxCrewRevisions-1].Revision != "r5" {
		t.Fatalf("want newest r14 first and r5 last, got %s .. %s", history[0].Revision, history[maxCrewRevisions-1].Revision)
	}
}

func TestRecordCrewRevision(t *testing.T) {
	crew := revTestCrew(nil, nil)
	recordCrewRevision(crew, crewProvenance(crew), metav1.Now())
	if len(crew.Status.Revisions) != 0 {
		t.Fatal("a crew with no markers must record nothing")
	}
	crew.Labels = map[string]string{crewVersionLabel: revTestVersion}
	recordCrewRevision(crew, crewProvenance(crew), metav1.Now())
	recordCrewRevision(crew, crewProvenance(crew), metav1.NewTime(time.Now().Add(time.Minute)))
	if len(crew.Status.Revisions) != 1 || crew.Status.Revisions[0].CrewVersion != revTestVersion {
		t.Fatalf("want one crew-version revision, got %+v", crew.Status.Revisions)
	}
}

func decodeCrews(t *testing.T, raw string) map[string]namespaceCrewEntry {
	t.Helper()
	out := map[string]namespaceCrewEntry{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("annotation is not valid JSON: %q: %v", raw, err)
	}
	return out
}

func TestMergeNamespaceCrewsAddAndUpdate(t *testing.T) {
	e1 := namespaceCrewEntry{Revision: "a", Owner: revTestOwner}
	raw, changed := mergeNamespaceCrews("", "pilot", e1)
	if !changed || decodeCrews(t, raw)["pilot"] != e1 {
		t.Fatalf("add: got %q changed=%v", raw, changed)
	}
	raw, _ = mergeNamespaceCrews(raw, testOtherCrew, namespaceCrewEntry{CrewVersion: revTestVersion})
	if _, changed = mergeNamespaceCrews(raw, "pilot", e1); changed {
		t.Fatal("an identical entry must not report a change")
	}
	e2 := namespaceCrewEntry{Revision: "b", Owner: revTestOwner}
	raw, changed = mergeNamespaceCrews(raw, "pilot", e2)
	crews := decodeCrews(t, raw)
	if !changed || crews["pilot"] != e2 || crews[testOtherCrew].CrewVersion != revTestVersion {
		t.Fatalf("update must replace pilot and keep other, got %q", raw)
	}
}

func TestMergeNamespaceCrewsRemove(t *testing.T) {
	raw := `{"other":{"crewVersion":"v1"},"pilot":{"revision":"b"}}`
	raw, changed := mergeNamespaceCrews(raw, "pilot", namespaceCrewEntry{})
	crews := decodeCrews(t, raw)
	if _, present := crews["pilot"]; !changed || present || len(crews) != 1 {
		t.Fatalf("remove must drop only pilot, got %q", raw)
	}
	raw, changed = mergeNamespaceCrews(raw, testOtherCrew, namespaceCrewEntry{})
	if !changed || raw != "" {
		t.Fatalf("removing the last entry must yield an empty value, got %q", raw)
	}
	if _, changed = mergeNamespaceCrews("", "ghost", namespaceCrewEntry{}); changed {
		t.Fatal("removing an absent entry must not report a change")
	}
}

func TestMergeNamespaceCrewsToleratesMalformedJSON(t *testing.T) {
	for _, bad := range []string{"{not json", "null", "[1,2]", `"str"`} {
		raw, changed := mergeNamespaceCrews(bad, "pilot", namespaceCrewEntry{Revision: "a"})
		if !changed || decodeCrews(t, raw)["pilot"].Revision != "a" {
			t.Fatalf("malformed %q must be replaced, got %q changed=%v", bad, raw, changed)
		}
		raw, changed = mergeNamespaceCrews(bad, "pilot", namespaceCrewEntry{})
		if !changed || raw != "" {
			t.Fatalf("removing from malformed %q must clear it, got %q changed=%v", bad, raw, changed)
		}
	}
}

func TestNamespaceCrewsPatch(t *testing.T) {
	if got := string(namespaceCrewsPatch("")); got != `{"metadata":{"annotations":{"kubemoot.ai/crews":null}}}` {
		t.Fatalf("empty value must null the annotation, got %s", got)
	}
	var p map[string]map[string]map[string]string
	if err := json.Unmarshal(namespaceCrewsPatch(`{"a":{}}`), &p); err != nil || p["metadata"]["annotations"][annoNamespaceCrews] != `{"a":{}}` {
		t.Fatalf("value must be set verbatim, got %v err=%v", p, err)
	}
}

func TestPatchNamespaceCrewsKeepsOtherAnnotations(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespaceA, Annotations: map[string]string{"keep": "me"}}}
	cli := fake.NewClientBuilder().WithScheme(scopeTestScheme(t)).WithObjects(ns).Build()
	r := &CrewReconciler{Client: cli}
	crew := revTestCrew(crewForgeAnnotations(), nil)

	r.reconcileNamespaceCrews(context.Background(), crew, crewProvenance(crew))
	got := &corev1.Namespace{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: testNamespaceA}, got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations["keep"] != "me" || decodeCrews(t, got.Annotations[annoNamespaceCrews])[testCrewPilot].Revision != revTestHash {
		t.Fatalf("annotations after set = %v", got.Annotations)
	}

	r.removeNamespaceCrew(context.Background(), crew)
	got = &corev1.Namespace{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: testNamespaceA}, got); err != nil {
		t.Fatal(err)
	}
	if _, present := got.Annotations[annoNamespaceCrews]; present || got.Annotations["keep"] != "me" {
		t.Fatalf("annotations after remove = %v", got.Annotations)
	}
}

func TestPatchNamespaceCrewsSwallowsErrors(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespaceA}}
	cli := fake.NewClientBuilder().WithScheme(scopeTestScheme(t)).WithObjects(ns).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
				return fmt.Errorf("forbidden")
			},
		}).Build()
	r := &CrewReconciler{Client: cli}
	crew := revTestCrew(crewForgeAnnotations(), nil)
	r.reconcileNamespaceCrews(context.Background(), crew, crewProvenance(crew))

	missing := &CrewReconciler{Client: fake.NewClientBuilder().WithScheme(scopeTestScheme(t)).Build()}
	missing.removeNamespaceCrew(context.Background(), revTestCrew(nil, nil))
}
