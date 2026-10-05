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
	"errors"
	"sort"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	orphanA = "smoke-1"
	orphanB = "smoke-2"
)

type fakeStateStore struct {
	keys    []string
	listErr error
	deleted []string
}

func (f *fakeStateStore) ListKVKeys(string) ([]string, error) { return f.keys, f.listErr }
func (f *fakeStateStore) DeleteKVKey(_, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func reconcileRequest(obj interface {
	GetName() string
	GetNamespace() string
}) ctrl.Request {
	r := ctrl.Request{}
	r.Name, r.Namespace = obj.GetName(), obj.GetNamespace()
	return r
}

func TestProviderStateSweep_DeletesOnlyOrphans(t *testing.T) {
	keep := deletionProvider("http://x")
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(keep).Build()
	store := &fakeStateStore{keys: []string{delTestProvider, orphanA, orphanB}}
	s := &ProviderStateSweeper{Reader: cl, Store: store}
	if err := s.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	sort.Strings(store.deleted)
	if len(store.deleted) != 2 || store.deleted[0] != orphanA || store.deleted[1] != orphanB {
		t.Errorf("deleted = %v; want [orphanA orphanB]", store.deleted)
	}
}

func TestProviderStateSweep_ListErrorDeletesNothing(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).Build()
	store := &fakeStateStore{keys: []string{orphanA}, listErr: errors.New("nats down")}
	s := &ProviderStateSweeper{Reader: cl, Store: store}
	if err := s.Sweep(context.Background()); err == nil {
		t.Error("expected the list error to be returned")
	}
	if len(store.deleted) != 0 {
		t.Errorf("nothing should be deleted; got %v", store.deleted)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Errorf("Start must not fail the manager: %v", err)
	}
	if !s.NeedLeaderElection() {
		t.Error("sweep should run on the leader")
	}
}

func TestDeleteOrphanedProviderState(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(deletionProvider("http://x")).Build()
	store := &fakeStateStore{}
	deleteOrphanedProviderState(context.Background(), cl, store, delTestProvider)
	if len(store.deleted) != 0 {
		t.Errorf("a provider that still exists keeps its state; deleted %v", store.deleted)
	}
	deleteOrphanedProviderState(context.Background(), cl, store, "gone")
	if len(store.deleted) != 1 || store.deleted[0] != "gone" {
		t.Errorf("deleted = %v; want [gone]", store.deleted)
	}
}

func TestModelProviderReconcile_NotFoundWithoutNATS(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).Build()
	r := &ModelProviderReconciler{Client: cl}
	req := ctrl.Request{}
	req.Name, req.Namespace = "gone", "kubemoot-system"
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Errorf("a missing provider is not an error: %v", err)
	}
}
