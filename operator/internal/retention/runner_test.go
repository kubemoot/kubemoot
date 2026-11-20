/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package retention

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func runnerFor(t *testing.T, store Store, now time.Time, crews ...*kubemootv1alpha1.Crew) (*Runner, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kubemootv1alpha1.Crew{})
	for _, c := range crews {
		b = b.WithObjects(c)
	}
	c := b.Build()
	return &Runner{Client: c, Cleaner: &Cleaner{Store: store, Now: func() time.Time { return now }}}, c
}

func crewCreated(name string, created time.Time, r *kubemootv1alpha1.CrewRetention) *kubemootv1alpha1.Crew {
	c := crewWith(r)
	c.Name = name
	c.CreationTimestamp = metav1.NewTime(created)
	return c
}

func statusOf(t *testing.T, c client.Client, name string) *kubemootv1alpha1.RetentionStatus {
	t.Helper()
	got := &kubemootv1alpha1.Crew{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: name}, got); err != nil {
		t.Fatal(err)
	}
	return got.Status.Retention
}

func TestPassRunsDueCrewAndRecordsWhatItRemoved(t *testing.T) {
	s := newFakeStore()
	s.kv[ConversationsBucket] = []KVEntry{{keyConvOld, ago(40 * day)}, {keyConvNew, ago(day)}}
	s.kv[MemoryBucket] = []KVEntry{{"ns.crew.t.stale", ago(100 * day)}}
	r, c := runnerFor(t, s, epoch, crewCreated("crew", ago(200*day), defaulted()))
	r.Pass(context.Background())

	st := statusOf(t, c, "crew")
	if st == nil || st.LastRunTime == nil || !st.LastRunTime.Time.Equal(epoch) {
		t.Fatalf("last run not recorded: %+v", st)
	}
	if st.Removed.Conversations != 1 || st.Removed.Memory != 1 || st.Removed.Other != 0 || st.Error != "" {
		t.Errorf("removed counts: %+v", st)
	}
}

func TestPassWaitsForTheSchedule(t *testing.T) {
	s := newFakeStore()
	s.kv[ConversationsBucket] = []KVEntry{{keyConvOld, ago(40 * day)}}
	crew := crewCreated("crew", ago(200*day), defaulted())
	crew.Status.Retention = &kubemootv1alpha1.RetentionStatus{LastRunTime: &metav1.Time{Time: epoch.Add(-time.Hour)}}
	// epoch is 12:00; the last run was at 11:00 the same day, so the next @daily is tomorrow.
	r, c := runnerFor(t, s, epoch, crew)
	r.Pass(context.Background())
	if len(s.deleted) != 0 {
		t.Fatal("ran before the schedule was due")
	}
	if st := statusOf(t, c, "crew"); !st.LastRunTime.Time.Equal(epoch.Add(-time.Hour)) {
		t.Errorf("last run moved: %v", st.LastRunTime)
	}

	r.Cleaner.Now = func() time.Time { return epoch.Add(13 * time.Hour) }
	r.Pass(context.Background())
	if len(s.deleted[ConversationsBucket]) != 1 {
		t.Errorf("did not run once due: %v", s.deleted)
	}
}

func TestPassSkipsInvalidUnsetAndDeletingCrews(t *testing.T) {
	s := newFakeStore()
	s.kv[ConversationsBucket] = []KVEntry{{"conv.ns.bad.old", ago(900 * day)}, {"conv.ns.none.old", ago(900 * day)}, {"conv.ns.gone.old", ago(900 * day)}}
	bad := defaulted()
	bad.Conversations.Window = "30 days"
	gone := crewCreated("gone", ago(200*day), defaulted())
	now := metav1.NewTime(epoch)
	gone.DeletionTimestamp = &now
	gone.Finalizers = []string{"kubemoot.ai/crew-finalizer"}
	r, c := runnerFor(t, s, epoch, crewCreated("bad", ago(200*day), bad), crewCreated("none", ago(200*day), nil), gone)
	r.Pass(context.Background())
	if len(s.deleted) != 0 {
		t.Errorf("removed data for an invalid, unset or deleting crew: %v", s.deleted)
	}
	if statusOf(t, c, "bad") != nil || statusOf(t, c, "none") != nil {
		t.Error("a skipped crew has no cleanup status")
	}
}

func TestPassRecordsAFailureAndLeavesOtherCrewsRunning(t *testing.T) {
	s := newFakeStore()
	s.failKV[MemoryBucket] = context.DeadlineExceeded
	s.kv[ConversationsBucket] = []KVEntry{{"conv.ns.a.old", ago(40 * day)}, {"conv.ns.b.old", ago(40 * day)}}
	r, c := runnerFor(t, s, epoch, crewCreated("a", ago(200*day), defaulted()), crewCreated("b", ago(200*day), defaulted()))
	r.Pass(context.Background())
	for _, n := range []string{"a", "b"} {
		st := statusOf(t, c, n)
		if st == nil || st.Error == "" || st.Removed.Conversations != 1 {
			t.Errorf("%s: want a recorded error and its conversation removed, got %+v", n, st)
		}
	}
}

func TestStartStopsWithTheContext(t *testing.T) {
	r, _ := runnerFor(t, newFakeStore(), epoch)
	r.Interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- r.Start(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not stop")
	}
	if !r.NeedLeaderElection() {
		t.Error("the cleanup runs on the leader only")
	}
}

func TestPassDoesNothingWithoutAStore(t *testing.T) {
	s := newFakeStore()
	s.kv[ConversationsBucket] = []KVEntry{{keyConvOld, ago(40 * day)}}
	r, c := runnerFor(t, s, epoch, crewCreated("crew", ago(200*day), defaulted()))
	r.Active = func() bool { return false }
	r.Pass(context.Background())
	if len(s.deleted) != 0 || statusOf(t, c, "crew") != nil {
		t.Error("a pass without a store must not record a cleanup")
	}
}

func TestPassDoesNotRepeatARunWhoseStatusCouldNotBeRecorded(t *testing.T) {
	s := newFakeStore()
	s.kv[ConversationsBucket] = []KVEntry{{keyConvOld, ago(40 * day)}}
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kubemootv1alpha1.Crew{}).
		WithObjects(crewCreated("crew", ago(200*day), defaulted())).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			return errors.New("conflict")
		}}).Build()
	r := &Runner{Client: c, Cleaner: &Cleaner{Store: s, Now: func() time.Time { return epoch }}}
	r.Pass(context.Background())
	r.Pass(context.Background())
	if len(s.deleted[ConversationsBucket]) != 1 || len(s.purged) != 0 {
		t.Errorf("the cleanup ran again before its schedule: %v", s.deleted)
	}
	r.Cleaner.Now = func() time.Time { return epoch.Add(13 * time.Hour) }
	s.kv[ConversationsBucket] = []KVEntry{{"conv.ns.crew.old2", ago(40 * day)}}
	r.Pass(context.Background())
	if len(s.deleted[ConversationsBucket]) != 2 {
		t.Errorf("the next scheduled run did not happen: %v", s.deleted)
	}
}
