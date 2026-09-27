/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crewmemory

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

const (
	legacyKey    = "pilot.infra.disk"
	scopedKey    = "team-a.pilot.infra.disk"
	ambiguousKey = "shared.infra.disk"
	nsA          = "team-a"
	nsB          = "team-b"
)

func crew(ns, name string) kubemootv1alpha1.Crew {
	return kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func TestPlanClassifiesKeys(t *testing.T) {
	idx := NewIndex([]kubemootv1alpha1.Crew{
		crew(nsA, "pilot"),
		crew(nsA, "shared"),
		crew(nsB, "shared"),
	})
	p := Plan([]string{
		legacyKey,                  // one namespace -> rename
		scopedKey,                  // already scoped -> skip
		ambiguousKey,               // two namespaces -> ambiguous
		"ghost.infra.disk",         // no crew -> orphaned
		"nodots",                   // malformed -> orphaned
		"team-b.shared.topic.fact", // already scoped -> skip
	}, idx)
	wantRenames := []Rename{{From: legacyKey, To: scopedKey}}
	if !reflect.DeepEqual(p.Renames, wantRenames) {
		t.Errorf("renames = %+v, want %+v", p.Renames, wantRenames)
	}
	if !reflect.DeepEqual(p.Ambiguous, []string{ambiguousKey}) {
		t.Errorf("ambiguous = %v", p.Ambiguous)
	}
	sort.Strings(p.Orphaned)
	if !reflect.DeepEqual(p.Orphaned, []string{"ghost.infra.disk", "nodots"}) {
		t.Errorf("orphaned = %v", p.Orphaned)
	}
}

func TestPlanFlagsKeysThatReadBothWays(t *testing.T) {
	// Namespace "sales" holds Crew "orders", and a Crew named "sales" exists too:
	// sales.orders.line1 is either orders' fact or an unscoped fact of sales.
	idx := NewIndex([]kubemootv1alpha1.Crew{
		crew("sales", "orders"),
		crew(nsB, "sales"),
		crew("homelab-pilot", "homelab-pilot"),
	})
	p := Plan([]string{"sales.orders.line1", "homelab-pilot.homelab-pilot.topic.key"}, idx)
	if !reflect.DeepEqual(p.Ambiguous, []string{"sales.orders.line1"}) {
		t.Errorf("ambiguous = %v", p.Ambiguous)
	}
	if len(p.Renames)+len(p.Orphaned) != 0 {
		t.Errorf("unexpected plan %+v", p)
	}
}

func TestPlanIsIdempotent(t *testing.T) {
	idx := NewIndex([]kubemootv1alpha1.Crew{crew(nsA, "pilot")})
	first := Plan([]string{legacyKey}, idx)
	second := Plan([]string{first.Renames[0].To}, idx)
	if len(second.Renames)+len(second.Ambiguous)+len(second.Orphaned) != 0 {
		t.Errorf("a migrated key must be left alone, got %+v", second)
	}
}

type fakeKV struct {
	data    map[string][]byte
	failPut bool
}

func (f *fakeKV) ListKVKeys(string) ([]string, error) {
	keys := make([]string, 0, len(f.data))
	for k := range f.data {
		keys = append(keys, k)
	}
	return keys, nil
}
func (f *fakeKV) GetKVValue(_, key string) ([]byte, error) { return f.data[key], nil }
func (f *fakeKV) PutKVValue(_, key string, v []byte) error {
	if f.failPut {
		return errors.New("put failed")
	}
	f.data[key] = v
	return nil
}
func (f *fakeKV) DeleteKVKey(_, key string) error { delete(f.data, key); return nil }

func newReader(t *testing.T, crews ...kubemootv1alpha1.Crew) *Migrator {
	t.Helper()
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(s)
	for i := range crews {
		b = b.WithObjects(&crews[i])
	}
	return &Migrator{Reader: b.Build()}
}

func TestRunRenamesAndLeavesTheRest(t *testing.T) {
	m := newReader(t, crew(nsA, "pilot"), crew(nsA, "shared"), crew(nsB, "shared"))
	kv := &fakeKV{data: map[string][]byte{
		legacyKey:    []byte("old"),
		ambiguousKey: []byte("amb"),
		"ghost.x.y":  []byte("orphan"),
	}}
	m.KV = kv
	if err := m.Run(context.Background(), logr.Discard()); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		scopedKey:    []byte("old"),
		ambiguousKey: []byte("amb"),
		"ghost.x.y":  []byte("orphan"),
	}
	if !reflect.DeepEqual(kv.data, want) {
		t.Errorf("bucket = %v, want %v", kv.data, want)
	}
}

func TestRunKeepsExistingScopedValue(t *testing.T) {
	m := newReader(t, crew(nsA, "pilot"))
	kv := &fakeKV{data: map[string][]byte{
		legacyKey: []byte("old"),
		scopedKey: []byte("new"),
	}}
	m.KV = kv
	if err := m.Run(context.Background(), logr.Discard()); err != nil {
		t.Fatal(err)
	}
	if got := string(kv.data[scopedKey]); got != "new" {
		t.Errorf("scoped value overwritten: %q", got)
	}
	if _, ok := kv.data[legacyKey]; ok {
		t.Error("unscoped key should be deleted")
	}
}

func TestRunKeepsUnscopedKeyWhenPutFails(t *testing.T) {
	m := newReader(t, crew(nsA, "pilot"))
	kv := &fakeKV{data: map[string][]byte{legacyKey: []byte("old")}, failPut: true}
	m.KV = kv
	if err := m.Run(context.Background(), logr.Discard()); err != nil {
		t.Fatal(err)
	}
	if string(kv.data[legacyKey]) != "old" {
		t.Error("unscoped key must survive a failed copy")
	}
}

func TestRunNoopWithoutKV(t *testing.T) {
	m := &Migrator{}
	if err := m.Run(context.Background(), logr.Discard()); err != nil {
		t.Fatal(err)
	}
	if !m.NeedLeaderElection() {
		t.Error("migration must run on the leader only")
	}
}
