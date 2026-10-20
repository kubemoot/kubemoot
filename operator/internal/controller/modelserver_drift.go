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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/scheduler/record"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/perf"
)

// Once a server is calibrated, the operator compares the crew calls since then
// with the calibration (package perf). When a model's calls running alone drift
// well away from its calibrated speed, it records the drift on the server and
// asks the scheduling crew, once per drift, to judge whether to recalibrate: the
// cause may be lasting (an engine or hardware change) or passing (other load on
// the accelerator). The drift is cleared once the speeds agree again, or a new
// calibration replaces the one they drifted from.

// SchedulingCrewEnv names the crew the operator asks to judge drift, as
// namespace/name. Unset means no scheduling crew: drift is recorded, not judged.
const SchedulingCrewEnv = "KUBEMOOT_SCHEDULING_CREW"

const (
	calibrationPhaseCalibrated = "Calibrated"
	driftScheduledBy           = "kubemoot-operator/calibration-drift"
)

// CrewRef names a crew.
type CrewRef struct {
	Namespace string
	Name      string
}

// String is the crew as namespace/name.
func (c CrewRef) String() string { return c.Namespace + "/" + c.Name }

// ParseCrewRef reads namespace/name; anything else is no crew.
func ParseCrewRef(s string) (CrewRef, bool) {
	ns, name, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
		return CrewRef{}, false
	}
	return CrewRef{Namespace: ns, Name: name}, true
}

// DriftWatch reads crew-call history and asks the scheduling crew to judge drift.
type DriftWatch interface {
	// History is every agent's performance history for the server.
	History(server string) ([]bus.SchedulerPerformance, error)
	// Ready reports whether the scheduling crew can be asked now, and why not.
	Ready(ctx context.Context) (bool, string)
	// Ask posts the drift to the scheduling crew.
	Ask(ctx context.Context, server string, drift *aiv1alpha1.CalibrationDrift) error
}

// reviewDrift records, updates, or clears the server's calibration drift and
// asks the scheduling crew about a drift it has not been asked about. It writes the
// drift with its own status patches; failures are logged and retried on the next
// reconcile.
func (r *ModelServerReconciler) reviewDrift(ctx context.Context, p *aiv1alpha1.ModelServer) {
	cal := p.Status.Calibration
	if r.Drift == nil || cal == nil || cal.Phase != calibrationPhaseCalibrated || cal.CompletedAt == nil {
		return
	}
	log := logf.FromContext(ctx).WithName("calibration-drift")
	entries, err := r.Drift.History(p.Name)
	if err != nil {
		log.Info("could not read the performance history", "server", p.Name, "err", err)
		return
	}
	speeds, since := perf.CalibratedSpeeds(cal)
	next := nextDrift(p.Status.CalibrationDrift, perf.Drifted(speeds, perf.Observe(entries, p.Name, since)),
		*cal.CompletedAt, metav1.Now())
	if !driftUnchanged(p.Status.CalibrationDrift, next) &&
		!r.patchStatus(ctx, p, func(s *aiv1alpha1.ModelServerStatus) { s.CalibrationDrift = next.DeepCopy() }) {
		return
	}
	if next != nil && next.AskedAt == nil {
		r.askAboutDrift(ctx, p, next)
	}
}

// askAboutDrift asks the scheduling crew about the drift once: it records the ask
// first, so a failed status write cannot lead to a second ask, and takes it back
// when the ask itself fails.
func (r *ModelServerReconciler) askAboutDrift(ctx context.Context, p *aiv1alpha1.ModelServer, drift *aiv1alpha1.CalibrationDrift) {
	log := logf.FromContext(ctx).WithName("calibration-drift")
	if ready, why := r.Drift.Ready(ctx); !ready {
		log.V(1).Info("the scheduling crew is not ready; the drift waits", "server", p.Name, "why", why)
		return
	}
	now := metav1.Now()
	if !r.patchStatus(ctx, p, func(s *aiv1alpha1.ModelServerStatus) { setAskedAt(s, &now) }) {
		return
	}
	if err := r.Drift.Ask(ctx, p.Name, drift); err != nil {
		log.Info("could not ask the scheduling crew about the drift", "server", p.Name, "err", err)
		r.patchStatus(ctx, p, func(s *aiv1alpha1.ModelServerStatus) { setAskedAt(s, nil) })
		return
	}
	log.Info("asked the scheduling crew to judge a calibration drift", "server", p.Name, "models", len(drift.Models))
}

func setAskedAt(s *aiv1alpha1.ModelServerStatus, at *metav1.Time) {
	if s.CalibrationDrift != nil {
		s.CalibrationDrift.AskedAt = at
	}
}

// nextDrift is the drift to record for models: none when no model drifted, the
// current drift when it is the same one (same models, same calibration), else a
// new one detected now. A drift keeps the numbers it was detected with.
func nextDrift(current *aiv1alpha1.CalibrationDrift, models []perf.ModelDrift,
	calibrated, now metav1.Time) *aiv1alpha1.CalibrationDrift {
	if len(models) == 0 {
		return nil
	}
	names := make([]string, 0, len(models))
	drifts := make([]aiv1alpha1.ModelDrift, 0, len(models))
	for _, m := range models {
		names = append(names, m.Model)
		drifts = append(drifts, aiv1alpha1.ModelDrift{Model: m.Model, CalibratedTokensPerSecond: m.CalibratedTokensPerSecond,
			ObservedTokensPerSecond: m.ObservedTokensPerSecond, Samples: m.Samples})
	}
	key := digest(append(names, calibrated.UTC().Format(time.RFC3339)))
	if current != nil && current.Key == key {
		return current.DeepCopy()
	}
	return &aiv1alpha1.CalibrationDrift{Key: key, DetectedAt: now, Models: drifts}
}

func driftUnchanged(a, b *aiv1alpha1.CalibrationDrift) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Key == b.Key && (a.AskedAt == nil) == (b.AskedAt == nil)
}

// patchStatus changes the server's status with a merge patch that touches only
// what mutate changes, and reports whether it was stored. It patches a copy: the
// response is the stored object, which must not replace what this reconcile
// discovered and has yet to write. On success p takes the stored resource version
// and the fields other writers own (the calibration, the operator's own requests,
// the conditions), so the reconcile's status update that follows neither conflicts
// nor writes them back stale.
func (r *ModelServerReconciler) patchStatus(ctx context.Context, p *aiv1alpha1.ModelServer,
	mutate func(*aiv1alpha1.ModelServerStatus)) bool {
	stored := p.DeepCopy()
	before := stored.DeepCopy()
	mutate(&stored.Status)
	if err := r.Status().Patch(ctx, stored, client.MergeFrom(before)); err != nil {
		logf.FromContext(ctx).Info("could not patch the server's status", "server", p.Name, "err", err)
		return false
	}
	p.ResourceVersion = stored.ResourceVersion
	p.Status.Calibration = stored.Status.Calibration
	p.Status.CalibrationRequest = stored.Status.CalibrationRequest
	p.Status.CalibrationDrift = stored.Status.CalibrationDrift
	p.Status.Conditions = stored.Status.Conditions
	return true
}

// DriftKV is the part of the operator's NATS publisher the drift watch uses.
type DriftKV interface {
	perf.KV
	PutKVValue(bucket, key string, value []byte) error
	Configured() bool
}

// NATSDriftWatch reads the performance history from NATS KV and asks the
// scheduling crew through the operator's schedule poller: it writes a follow-up
// record due now, and the poller starts the conversation in the crew.
type NATSDriftWatch struct {
	Client client.Reader
	KV     DriftKV
	Crew   CrewRef
	Now    func() time.Time
}

func (w NATSDriftWatch) configured() bool { return w.KV != nil && w.KV.Configured() }

// History reads the server's entries in the performance bucket.
func (w NATSDriftWatch) History(server string) ([]bus.SchedulerPerformance, error) {
	if !w.configured() {
		return nil, errNATSUnavailable
	}
	return perf.ReadHistory(w.KV, server)
}

// Ready reports whether the crew exists and every agent in it is ready, so a
// request is not posted before its coordinator listens.
func (w NATSDriftWatch) Ready(ctx context.Context) (bool, string) {
	var crew aiv1alpha1.Crew
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: w.Crew.Namespace, Name: w.Crew.Name}, &crew); err != nil {
		return false, "crew: " + err.Error()
	}
	var agents aiv1alpha1.AgentList
	if err := w.Client.List(ctx, &agents, client.InNamespace(w.Crew.Namespace),
		client.MatchingLabels{labelCrew: w.Crew.Name}); err != nil {
		return false, "agents: " + err.Error()
	}
	if len(agents.Items) == 0 {
		return false, "the crew has no agents"
	}
	for _, a := range agents.Items {
		if !a.Status.Ready {
			return false, "agent " + a.Name + " is not ready"
		}
	}
	return true, ""
}

// errNotStored is a request the KV read-back did not find.
var errNotStored = errors.New("the request was not stored")

// Ask writes the request and reads it back, since the publisher reports some write
// failures as success. Its ID is stable per server and drift, so a retry
// overwrites rather than adds.
func (w NATSDriftWatch) Ask(_ context.Context, server string, drift *aiv1alpha1.CalibrationDrift) error {
	if !w.configured() {
		return errNATSUnavailable
	}
	rec := driftRecord(w.Crew, server, drift, w.Now())
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := w.KV.PutKVValue(record.Bucket, rec.ScheduleID, raw); err != nil {
		return err
	}
	stored, err := w.KV.GetKVValue(record.Bucket, rec.ScheduleID)
	if err != nil {
		return err
	}
	if stored == nil {
		return errNotStored
	}
	return nil
}

// driftRecord is the follow-up that asks the crew to judge the drift.
func driftRecord(crew CrewRef, server string, drift *aiv1alpha1.CalibrationDrift, now time.Time) record.Record {
	return record.Record{
		ScheduleID:  "calibration-drift-" + server + "-" + drift.Key,
		Kind:        record.KindFollowup,
		TriggerAt:   now.UTC(),
		ScheduledBy: driftScheduledBy,
		Namespace:   crew.Namespace,
		Crew:        crew.Name,
		Query:       driftQuery(server, drift),
		Reason:      "calibration drift " + drift.Key,
	}
}

// driftQuery states the drift with its numbers and asks for a judgment.
func driftQuery(server string, drift *aiv1alpha1.CalibrationDrift) string {
	parts := make([]string, 0, len(drift.Models))
	for _, m := range drift.Models {
		parts = append(parts, fmt.Sprintf("%s generated %d tokens per second at the median over %d calls running alone, "+
			"against %d calibrated", m.Model, m.ObservedTokensPerSecond, m.Samples, m.CalibratedTokensPerSecond))
	}
	return fmt.Sprintf("Crew calls on model server %s have drifted from its calibration: %s. "+
		"Judge whether to recalibrate now or wait.", server, strings.Join(parts, "; "))
}
