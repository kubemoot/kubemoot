/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add custom scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	return s
}

func TestProcessMessage_RejectsUnscopedSubject(t *testing.T) {
	d := &Dispatcher{}
	body, _ := json.Marshal(DiscussionMessage{MessageType: ConcernMessageType, AgentName: "x", Content: "y", ThreadID: "t"})
	for _, subject := range []string{
		"kubemoot.discuss.crew.channel",
		"kubemoot.discuss..crew.channel.thread",
		"other.subject.path.here.x.y",
		"",
	} {
		if err := d.ProcessMessage(context.Background(), subject, body); err == nil {
			t.Errorf("subject %q: expected a parse error", subject)
		}
	}
}

func TestMatchesFilters(t *testing.T) {
	mkSink := func(channels, agents []string) *kubemootv1alpha1.NotificationSink {
		return &kubemootv1alpha1.NotificationSink{
			Spec: kubemootv1alpha1.NotificationSinkSpec{
				Channels: channels,
				Agents:   agents,
			},
		}
	}
	msg := &DiscussionMessage{Channel: testChannelGeneral, AgentName: testAgentK8sStorage}

	cases := []struct {
		name string
		sink *kubemootv1alpha1.NotificationSink
		want bool
	}{
		{"empty filters match anything", mkSink(nil, nil), true},
		{"channel match", mkSink([]string{testChannelGeneral}, nil), true},
		{"channel mismatch", mkSink([]string{"alerts"}, nil), false},
		{"agent match", mkSink(nil, []string{testAgentK8sStorage, "k8s-workloads"}), true},
		{"agent mismatch", mkSink(nil, []string{"k8s-workloads"}), false},
		{"both match", mkSink([]string{testChannelGeneral}, []string{testAgentK8sStorage}), true},
		{"channel match, agent mismatch", mkSink([]string{testChannelGeneral}, []string{"other"}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesFilters(tc.sink, msg); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProcessMessage_SkipsNonConcernSignals — only the concern messageType
// dispatches in the first release.
func TestProcessMessage_SkipsNonConcernSignals(t *testing.T) {
	d := &Dispatcher{} // no client/HTTP needed for the skip path
	for _, mt := range []string{"agree", "stand_aside", "synthesis", "advisory", "evaluating"} {
		body, _ := json.Marshal(DiscussionMessage{MessageType: mt, AgentName: "x", Content: "y", ThreadID: "t"})
		if err := d.ProcessMessage(context.Background(), "kubemoot.discuss.ns.c.general.t", body); err != nil {
			t.Errorf("messageType=%q: expected nil error; got %v", mt, err)
		}
	}
}

// TestProcessMessage_DispatchesConcernToWebhook is the happy-path:
// a concern message hits a sink in the namespace the subject names,
// the webhook captures the payload, and the sink status is updated.
func TestProcessMessage_DispatchesConcernToWebhook(t *testing.T) {
	scheme := newScheme(t)

	// Capture webhook calls.
	var (
		mu       sync.Mutex
		captured [][]byte
		headers  []http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = append(captured, body)
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &kubemootv1alpha1.NotificationSink{
		ObjectMeta: metav1.ObjectMeta{Name: "ntfy", Namespace: "crew-homelab-pilot"},
		Spec: kubemootv1alpha1.NotificationSinkSpec{
			Webhook:  kubemootv1alpha1.WebhookTarget{URL: srv.URL, Method: "POST"},
			Priority: "high",
		},
		Status: kubemootv1alpha1.NotificationSinkStatus{Ready: true},
	}

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sink).
		WithStatusSubresource(sink).
		Build()

	d := &Dispatcher{Client: cli, HTTPClient: srv.Client(), DashboardBase: "https://dash.example.com"}

	msg := DiscussionMessage{
		MessageType: ConcernMessageType,
		AgentName:   testAgentK8sStorage,
		Content:     "Partition /var on rig0 is 96% full",
		ThreadID:    "thread-1",
		Channel:     testChannelGeneral,
		Timestamp:   "2026-05-17T04:00:00Z",
	}
	body, _ := json.Marshal(msg)
	if err := d.ProcessMessage(context.Background(), "kubemoot.discuss.crew-homelab-pilot.homelab-pilot.general.thread-1", body); err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("expected 1 webhook hit; got %d", len(captured))
	}
	if !strings.Contains(string(captured[0]), `"namespace":"crew-homelab-pilot"`) {
		t.Errorf("payload should name the namespace; got %s", captured[0])
	}
	if !strings.Contains(string(captured[0]), "Partition /var") {
		t.Errorf("payload should contain the concern; got %s", captured[0])
	}
	if h := headers[0].Get("X-Priority"); h != "high" {
		t.Errorf("X-Priority: got %q, want high", h)
	}
	if h := headers[0].Get("X-Title"); !strings.Contains(h, testAgentK8sStorage) {
		t.Errorf("X-Title should include agent; got %q", h)
	}
	if h := headers[0].Get("Content-Type"); h != "application/json" {
		t.Errorf("Content-Type: got %q", h)
	}
}

// TestProcessMessage_SkipsWhenNamespaceHasNoSinks — no sinks in the subject's
// namespace → nothing dispatched, no error returned (silent skip).
func TestProcessMessage_SkipsWhenNamespaceHasNoSinks(t *testing.T) {
	scheme := newScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	d := &Dispatcher{Client: cli}
	body, _ := json.Marshal(DiscussionMessage{
		MessageType: ConcernMessageType, AgentName: "x", Content: "y", ThreadID: "t", Channel: testChannelGeneral,
	})
	if err := d.ProcessMessage(context.Background(), "kubemoot.discuss.ghost-ns.ghost-crew.general.t", body); err != nil {
		t.Errorf("expected silent skip; got error %v", err)
	}
}

// TestProcessMessage_SkipsUnreadySinks — sinks with Ready=false (failed
// validation) should not be hit even if filters would match.
func TestProcessMessage_SkipsUnreadySinks(t *testing.T) {
	scheme := newScheme(t)
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &kubemootv1alpha1.NotificationSink{
		ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "crew-x"},
		Spec:       kubemootv1alpha1.NotificationSinkSpec{Webhook: kubemootv1alpha1.WebhookTarget{URL: srv.URL}},
		Status:     kubemootv1alpha1.NotificationSinkStatus{Ready: false},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sink).Build()
	d := &Dispatcher{Client: cli, HTTPClient: srv.Client()}
	body, _ := json.Marshal(DiscussionMessage{
		MessageType: ConcernMessageType, AgentName: "a", Content: "c", ThreadID: "t",
	})
	_ = d.ProcessMessage(context.Background(), "kubemoot.discuss.crew-x.x.general.t", body)
	if hit {
		t.Error("non-ready sink should not have been dispatched")
	}
}

// TestProcessMessage_SameCrewNameInTwoNamespaces — a concern from pilot in
// team-a fires only team-a's sink, never the sink of pilot in team-b.
func TestProcessMessage_SameCrewNameInTwoNamespaces(t *testing.T) {
	scheme := newScheme(t)
	var (
		mu   sync.Mutex
		hits []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mkSink := func(ns string) *kubemootv1alpha1.NotificationSink {
		return &kubemootv1alpha1.NotificationSink{
			ObjectMeta: metav1.ObjectMeta{Name: "ntfy", Namespace: ns},
			Spec:       kubemootv1alpha1.NotificationSinkSpec{Webhook: kubemootv1alpha1.WebhookTarget{URL: srv.URL + "/" + ns}},
			Status:     kubemootv1alpha1.NotificationSinkStatus{Ready: true},
		}
	}
	a, b := mkSink("team-a"), mkSink("team-b")
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(a, b).WithStatusSubresource(a, b).Build()
	d := &Dispatcher{Client: cli, HTTPClient: srv.Client()}
	body, _ := json.Marshal(DiscussionMessage{
		MessageType: ConcernMessageType, AgentName: "a", Content: "c", ThreadID: "t", Channel: testChannelGeneral,
	})
	if err := d.ProcessMessage(context.Background(), "kubemoot.discuss.team-a.pilot.general.t", body); err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 || hits[0] != "/team-a" {
		t.Fatalf("want exactly team-a's sink hit, got %v", hits)
	}
}
