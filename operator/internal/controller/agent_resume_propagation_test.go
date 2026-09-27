/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// TestSpecialistChangeEnqueuesItsCoordinator: a specialist's change re-reconciles
// the coordinator of its own crew only; coordinators, crewless agents, and other
// objects enqueue nothing.
func TestSpecialistChangeEnqueuesItsCoordinator(t *testing.T) {
	coord := mkResumeAgent("team-prop", "ops-coordinator", "platform-ops", roleCoordinator, nil)
	other := mkResumeAgent("team-prop", "other-coordinator", "pedagogy", roleCoordinator, nil)
	reader := mkResumeAgent("team-prop", "docs-reader", "platform-ops", "analyst", nil)
	crewless := mkResumeAgent("team-prop", "loner", "", "tooler", func(a *kubemootv1alpha1.Agent) { a.Labels = nil })
	cli := fake.NewClientBuilder().WithScheme(resumeScheme(t)).WithObjects(coord, other, reader, crewless).Build()
	ctx := context.Background()

	got := mapSpecialistToCoordinatorRequests(ctx, cli, reader)
	if len(got) != 1 || got[0].Name != "ops-coordinator" || got[0].Namespace != "team-prop" {
		t.Fatalf("want [team-prop/ops-coordinator], got %v", got)
	}
	if reqs := mapSpecialistToCoordinatorRequests(ctx, cli, coord); reqs != nil {
		t.Errorf("a coordinator should enqueue nothing, got %v", reqs)
	}
	if reqs := mapSpecialistToCoordinatorRequests(ctx, cli, crewless); reqs != nil {
		t.Errorf("an agent without a crew label should enqueue nothing, got %v", reqs)
	}
	if reqs := mapSpecialistToCoordinatorRequests(ctx, cli, &kubemootv1alpha1.PromptModule{}); reqs != nil {
		t.Errorf("a non-Agent should enqueue nothing, got %v", reqs)
	}
}

// TestSpecialistResumePredicate: spec and annotation changes pass, status-only updates
// do not, creates and deletes do.
func TestSpecialistResumePredicate(t *testing.T) {
	p := specialistResumeChanged()
	base := mkResumeAgent("team-prop", "docs-reader", "platform-ops", "analyst", nil)
	base.Generation = 1

	statusOnly := base.DeepCopy()
	statusOnly.Status.Phase = "Running"
	if p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: statusOnly}) {
		t.Error("a status-only update should not pass")
	}
	specChange := base.DeepCopy()
	specChange.Generation = 2
	if !p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: specChange}) {
		t.Error("a spec change (generation bump) should pass")
	}
	annotated := base.DeepCopy()
	annotated.Annotations = map[string]string{triageSummaryAnno: "reads the docs"}
	if !p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: annotated}) {
		t.Error("an annotation change should pass")
	}
	if !p.Create(event.CreateEvent{Object: base}) || !p.Delete(event.DeleteEvent{Object: base}) {
		t.Error("creates and deletes should pass")
	}
}
