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

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
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

func TestParseCrewFromSubject(t *testing.T) {
	cases := []struct {
		subject string
		want    string
		ok      bool
	}{
		{"kubemoot.discuss.homelab-pilot.general.abc-123", "homelab-pilot", true},
		{"kubemoot.discuss.crew.channel.thread.extra", "crew", true},
		{"kubemoot.discuss.crew.channel", "", false},
		{"other.subject.path.here.x", "", false},
		{"kubemoot.discuss..channel.thread", "", false}, // empty crew rejected
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := parseCrewFromSubject(tc.subject)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseCrewFromSubject(%q): got (%q, %v); want (%q, %v)", tc.subject, got, ok, tc.want, tc.ok)
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
	msg := &DiscussionMessage{Channel: "general", AgentName: "k8s-storage"}

	cases := []struct {
		name string
		sink *kubemootv1alpha1.NotificationSink
		want bool
	}{
		{"empty filters match anything", mkSink(nil, nil), true},
		{"channel match", mkSink([]string{"general"}, nil), true},
		{"channel mismatch", mkSink([]string{"alerts"}, nil), false},
		{"agent match", mkSink(nil, []string{"k8s-storage", "k8s-workloads"}), true},
		{"agent mismatch", mkSink(nil, []string{"k8s-workloads"}), false},
		{"both match", mkSink([]string{"general"}, []string{"k8s-storage"}), true},
		{"channel match, agent mismatch", mkSink([]string{"general"}, []string{"other"}), false},
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
		if err := d.ProcessMessage(context.Background(), "kubemoot.discuss.c.general.t", body); err != nil {
			t.Errorf("messageType=%q: expected nil error; got %v", mt, err)
		}
	}
}

// TestProcessMessage_DispatchesConcernToWebhook is the happy-path:
// a concern message hits a sink whose namespace is labeled for the crew,
// the webhook captures the payload, and the sink status is updated.
func TestProcessMessage_DispatchesConcernToWebhook(t *testing.T) {
	scheme := newScheme(t)

	// Capture webhook calls.
	var (
		mu        sync.Mutex
		captured  [][]byte
		headers   []http.Header
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

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "crew-homelab-pilot",
			Labels: map[string]string{CrewNamespaceLabel: "homelab-pilot"},
		},
	}
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
		WithObjects(ns, sink).
		WithStatusSubresource(sink).
		Build()

	d := &Dispatcher{Client: cli, HTTPClient: srv.Client(), DashboardBase: "https://dash.example.com"}

	msg := DiscussionMessage{
		MessageType: ConcernMessageType,
		AgentName:   "k8s-storage",
		Content:     "Partition /var on rig0 is 96% full",
		ThreadID:    "thread-1",
		Channel:     "general",
		Timestamp:   "2026-05-17T04:00:00Z",
	}
	body, _ := json.Marshal(msg)
	if err := d.ProcessMessage(context.Background(), "kubemoot.discuss.homelab-pilot.general.thread-1", body); err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("expected 1 webhook hit; got %d", len(captured))
	}
	if !strings.Contains(string(captured[0]), "Partition /var") {
		t.Errorf("payload should contain the concern; got %s", captured[0])
	}
	if h := headers[0].Get("X-Priority"); h != "high" {
		t.Errorf("X-Priority: got %q, want high", h)
	}
	if h := headers[0].Get("X-Title"); !strings.Contains(h, "k8s-storage") {
		t.Errorf("X-Title should include agent; got %q", h)
	}
	if h := headers[0].Get("Content-Type"); h != "application/json" {
		t.Errorf("Content-Type: got %q", h)
	}
}

// TestProcessMessage_SkipsWhenNamespaceMissing — no crew namespace label →
// no sinks dispatched, no error returned (silent skip).
func TestProcessMessage_SkipsWhenNamespaceMissing(t *testing.T) {
	scheme := newScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	d := &Dispatcher{Client: cli}
	body, _ := json.Marshal(DiscussionMessage{
		MessageType: ConcernMessageType, AgentName: "x", Content: "y", ThreadID: "t", Channel: "general",
	})
	if err := d.ProcessMessage(context.Background(), "kubemoot.discuss.ghost-crew.general.t", body); err != nil {
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

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "crew-x", Labels: map[string]string{CrewNamespaceLabel: "x"},
		},
	}
	sink := &kubemootv1alpha1.NotificationSink{
		ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "crew-x"},
		Spec:       kubemootv1alpha1.NotificationSinkSpec{Webhook: kubemootv1alpha1.WebhookTarget{URL: srv.URL}},
		Status:     kubemootv1alpha1.NotificationSinkStatus{Ready: false},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns, sink).Build()
	d := &Dispatcher{Client: cli, HTTPClient: srv.Client()}
	body, _ := json.Marshal(DiscussionMessage{
		MessageType: ConcernMessageType, AgentName: "a", Content: "c", ThreadID: "t",
	})
	_ = d.ProcessMessage(context.Background(), "kubemoot.discuss.x.general.t", body)
	if hit {
		t.Error("non-ready sink should not have been dispatched")
	}
}
