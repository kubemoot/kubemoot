/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/javajon/kubemoot/operator/internal/nats"
)

// DiscussionSubjectWildcard is the NATS subject the dispatcher subscribes
// to. Discussion subjects follow `kubemoot.discuss.<crew>.<channel>.<thread>`.
const DiscussionSubjectWildcard = "kubemoot.discuss.>"

// DiscussStreamName is the JetStream stream holding discussion messages.
// Used for the dispatcher's durable consumer binding.
const DiscussStreamName = "KUBEMOOT_DISCUSS"

// DispatcherDurableName is the JetStream consumer name. MUST be stable
// across operator pod restarts so the consumer's delivery position
// resumes; renaming it strands the existing consumer + loses position.
const DispatcherDurableName = "kubemoot-notification-dispatcher"

// CrewNamespaceLabel is the label that maps a Kubernetes namespace to a
// crew. Existing convention across the operator (see Crew controller).
const CrewNamespaceLabel = "kubemoot.ai/crew"

// Dispatcher is a leader-elected runnable that drives the
// NotificationSink → webhook path. Lookups are namespace-scoped; HTTP
// client is injectable for tests.
type Dispatcher struct {
	Client        client.Client
	Publisher     *kubemootnats.Publisher
	HTTPClient    *http.Client
	DashboardBase string // optional, e.g. "https://dashboard.example.com"
	Now           func() time.Time

	sub *nats.Subscription
}

// NeedLeaderElection ensures only the leader pod dispatches — otherwise
// each running operator pod fires duplicate webhooks.
func (d *Dispatcher) NeedLeaderElection() bool { return true }

// Start subscribes to discussion messages and runs until ctx is cancelled.
// Satisfies controller-runtime's manager.Runnable.
func (d *Dispatcher) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("notifications")
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if d.Now == nil {
		d.Now = time.Now
	}

	if d.Publisher == nil {
		log.Info("publisher not configured — notifications dispatcher idle")
		<-ctx.Done()
		return nil
	}

	// Durable JetStream consumer survives operator restarts: concerns
	// published while the dispatcher is offline are redelivered when it
	// reconnects. Without this, every operator crash drops in-flight
	// concerns on the floor.
	sub, err := d.Publisher.SubscribeDurable(
		DiscussStreamName,
		DiscussionSubjectWildcard,
		DispatcherDurableName,
		func(m *nats.Msg) {
			err := d.ProcessMessage(ctx, m.Subject, m.Data)
			if err != nil {
				log.V(1).Info("dispatch error", "subject", m.Subject, "err", err)
			}
			// Ack regardless of ProcessMessage outcome. Real failure modes:
			//   - decode error → permanent; Nak'ing would just retry forever
			//   - k8s API error → may be transient, but the next message in
			//     the stream will retry the same flow; redelivery storm is
			//     a worse failure than dropping one message
			//   - dispatch error → already recorded per-sink via recordFailure;
			//     redelivery would re-dispatch to ALL sinks, double-firing
			//     the ones that already succeeded
			// AckWait+MaxDeliver in SubscribeDurable still cover the
			// "operator died mid-handler" case automatically.
			if ackErr := m.Ack(); ackErr != nil {
				log.V(1).Info("ack failed", "subject", m.Subject, "err", ackErr)
			}
		},
	)
	if err != nil {
		log.Error(err, "failed to subscribe; dispatcher idle")
		<-ctx.Done()
		return nil
	}
	d.sub = sub
	if sub != nil {
		log.Info("dispatcher subscribed (JetStream durable)",
			"stream", DiscussStreamName,
			"subject", DiscussionSubjectWildcard,
			"durable", DispatcherDurableName)
	} else {
		log.Info("NATS not configured — dispatcher idle")
	}

	<-ctx.Done()
	if d.sub != nil {
		_ = d.sub.Drain()
	}
	return nil
}

// ProcessMessage decodes, filters, routes, and dispatches one message.
// Returns nil for routine "skip" cases (wrong messageType, no matching sinks);
// returns an error only when a real failure should be visible to callers.
func (d *Dispatcher) ProcessMessage(ctx context.Context, subject string, body []byte) error {
	var msg DiscussionMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if msg.MessageType != ConcernMessageType {
		return nil
	}
	crew, ok := parseCrewFromSubject(subject)
	if !ok {
		return fmt.Errorf("could not parse crew from subject %q", subject)
	}
	namespace, ok, err := d.findCrewNamespace(ctx, crew)
	if err != nil {
		return fmt.Errorf("lookup crew namespace: %w", err)
	}
	if !ok {
		return nil // no namespace labeled for this crew → nobody listening
	}

	sinks := &kubemootv1alpha1.NotificationSinkList{}
	if err := d.Client.List(ctx, sinks, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("list sinks: %w", err)
	}
	payload := BuildPayload(&msg, crew, d.DashboardBase)
	body2, err := MarshalPayload(&payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	for i := range sinks.Items {
		sink := &sinks.Items[i]
		if !matchesFilters(sink, &msg) {
			continue
		}
		if !sink.Status.Ready {
			continue // reconciler said the spec is invalid
		}
		if err := d.dispatch(ctx, sink, body2); err != nil {
			d.recordFailure(ctx, sink, err)
			continue
		}
		d.recordSuccess(ctx, sink, msg.AgentName)
	}
	return nil
}

// matchesFilters returns true when the message should fire this sink.
// Empty filter slices mean "match anything".
func matchesFilters(sink *kubemootv1alpha1.NotificationSink, msg *DiscussionMessage) bool {
	if len(sink.Spec.Channels) > 0 && !contains(sink.Spec.Channels, msg.Channel) {
		return false
	}
	if len(sink.Spec.Agents) > 0 && !contains(sink.Spec.Agents, msg.AgentName) {
		return false
	}
	return true
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// parseCrewFromSubject extracts <crew> from kubemoot.discuss.<crew>.<channel>.<thread>.
func parseCrewFromSubject(subject string) (string, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) < 5 || parts[0] != "kubemoot" || parts[1] != "discuss" {
		return "", false
	}
	if parts[2] == "" {
		return "", false
	}
	return parts[2], true
}

// findCrewNamespace returns the namespace labeled with kubemoot.ai/crew=<crew>.
// First match wins (the convention is one-namespace-per-crew).
func (d *Dispatcher) findCrewNamespace(ctx context.Context, crew string) (string, bool, error) {
	nsList := &corev1.NamespaceList{}
	if err := d.Client.List(ctx, nsList, client.MatchingLabels{CrewNamespaceLabel: crew}); err != nil {
		return "", false, err
	}
	if len(nsList.Items) == 0 {
		return "", false, nil
	}
	return nsList.Items[0].Name, true, nil
}

// dispatch performs the HTTP request. Returns a non-nil error on transport
// failure OR non-2xx response so the caller can record it on the sink status.
func (d *Dispatcher) dispatch(ctx context.Context, sink *kubemootv1alpha1.NotificationSink, body []byte) error {
	method := sink.Spec.Webhook.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, sink.Spec.Webhook.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	title := sink.Spec.Title
	if title == "" {
		title = fmt.Sprintf("Kubemoot · %s raised a concern", agentFromBody(body))
	}
	req.Header.Set("X-Title", title)
	if sink.Spec.Priority != "" {
		req.Header.Set("X-Priority", sink.Spec.Priority)
	}
	if err := d.applyCustomHeaders(ctx, sink, req); err != nil {
		return fmt.Errorf("apply headers: %w", err)
	}

	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

// agentFromBody re-parses the payload body to read the agent name back out
// for the default title. Cheap; payload is small.
func agentFromBody(body []byte) string {
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return "agent"
	}
	if p.Agent == "" {
		return "agent"
	}
	return p.Agent
}

// applyCustomHeaders resolves Value vs ValueFrom for each header entry.
// ValueFrom currently supports secretKeyRef and configMapKeyRef (k8s
// EnvVarSource shape) but only secretKeyRef is exercised in the first
// release; configMapKeyRef is implemented since the API already accepts it.
func (d *Dispatcher) applyCustomHeaders(ctx context.Context, sink *kubemootv1alpha1.NotificationSink, req *http.Request) error {
	for _, h := range sink.Spec.Webhook.Headers {
		val, err := d.resolveHeader(ctx, sink.Namespace, &h)
		if err != nil {
			return err
		}
		req.Header.Set(h.Name, val)
	}
	return nil
}

func (d *Dispatcher) resolveHeader(ctx context.Context, namespace string, h *kubemootv1alpha1.HeaderEntry) (string, error) {
	if h.Value != "" {
		return h.Value, nil
	}
	if h.ValueFrom == nil {
		return "", errors.New("header has neither value nor valueFrom")
	}
	if ref := h.ValueFrom.SecretKeyRef; ref != nil {
		secret := &corev1.Secret{}
		if err := d.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, secret); err != nil {
			return "", fmt.Errorf("get secret %s/%s: %w", namespace, ref.Name, err)
		}
		raw, ok := secret.Data[ref.Key]
		if !ok {
			return "", fmt.Errorf("secret %s missing key %s", ref.Name, ref.Key)
		}
		return string(raw), nil
	}
	if ref := h.ValueFrom.ConfigMapKeyRef; ref != nil {
		cm := &corev1.ConfigMap{}
		if err := d.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, cm); err != nil {
			return "", fmt.Errorf("get configmap %s/%s: %w", namespace, ref.Name, err)
		}
		val, ok := cm.Data[ref.Key]
		if !ok {
			return "", fmt.Errorf("configmap %s missing key %s", ref.Name, ref.Key)
		}
		return val, nil
	}
	return "", errors.New("valueFrom must specify secretKeyRef or configMapKeyRef")
}

// now returns the current time, falling back to time.Now when no clock
// was injected. Centralizes the nil-safety so each call site doesn't
// have to.
func (d *Dispatcher) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// recordSuccess increments TotalDispatched + updates lastFired fields.
// Best-effort: status update failures are logged but not returned.
func (d *Dispatcher) recordSuccess(ctx context.Context, sink *kubemootv1alpha1.NotificationSink, agent string) {
	log := logf.FromContext(ctx).WithName("notifications")
	patched := sink.DeepCopy()
	patched.Status.TotalDispatched++
	patched.Status.LastError = ""
	stamp := metav1.NewTime(d.now())
	patched.Status.LastFiredAt = &stamp
	patched.Status.LastFiredAgent = agent
	if err := d.Client.Status().Update(ctx, patched); err != nil && !apierrors.IsConflict(err) {
		log.V(1).Info("status update after success failed", "sink", sink.Name, "err", err)
	}
}

func (d *Dispatcher) recordFailure(ctx context.Context, sink *kubemootv1alpha1.NotificationSink, dispatchErr error) {
	log := logf.FromContext(ctx).WithName("notifications")
	patched := sink.DeepCopy()
	patched.Status.TotalFailed++
	patched.Status.LastError = dispatchErr.Error()
	if err := d.Client.Status().Update(ctx, patched); err != nil && !apierrors.IsConflict(err) {
		log.V(1).Info("status update after failure failed", "sink", sink.Name, "err", err)
	}
}
