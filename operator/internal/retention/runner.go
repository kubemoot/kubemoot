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
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// CheckInterval is how often the runner looks for a crew whose schedule has come due.
const CheckInterval = time.Minute

// Runner runs each crew's cleanup on the crew's schedule and records the result in the
// Crew's status. It runs on the elected leader only.
type Runner struct {
	Client  client.Client
	Cleaner *Cleaner
	// Interval overrides CheckInterval.
	Interval time.Duration
	// attempts remembers when each crew was last cleaned, so a run whose result could not
	// be recorded in the Crew status is not repeated before the next scheduled run.
	attempts sync.Map
	// Active reports whether there is a store to clean; nil means there is. With none, a
	// pass does nothing, so no crew is reported as cleaned when nothing could be.
	Active func() bool
}

// NeedLeaderElection runs the cleanup on the leader only.
func (r *Runner) NeedLeaderElection() bool { return true }

// Start checks for due crews until the context ends.
func (r *Runner) Start(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = CheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		r.Pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Pass runs the cleanup of every crew whose schedule is due.
func (r *Runner) Pass(ctx context.Context) {
	if r.Active != nil && !r.Active() {
		return
	}
	log := logf.FromContext(ctx).WithName("retention")
	crews := &kubemootv1alpha1.CrewList{}
	if err := r.Client.List(ctx, crews); err != nil {
		log.Error(err, "Listing Crews for the retention cleanup failed")
		return
	}
	now := r.Cleaner.Now()
	for i := range crews.Items {
		crew := &crews.Items[i]
		if !crew.DeletionTimestamp.IsZero() {
			continue
		}
		r.runCrew(ctx, crew, now)
	}
}

// runCrew cleans one crew if its schedule is due and its policy is valid. A crew with an
// invalid policy is skipped: nothing is removed on a value that could not be read.
func (r *Runner) runCrew(ctx context.Context, crew *kubemootv1alpha1.Crew, now time.Time) {
	log := logf.FromContext(ctx).WithName("retention").WithValues("crew", crew.Namespace+"/"+crew.Name)
	policy, err := Resolve(crew)
	last := r.lastRun(crew)
	if err != nil || !policy.Enforced() || !due(last, policy, now) {
		return
	}
	scope, err := crewscope.New(crew.Namespace, crew.Name)
	if err != nil {
		return
	}
	r.attempts.Store(client.ObjectKeyFromObject(crew), now)
	removed, runErr := r.Cleaner.Run(scope, policy)
	status := &kubemootv1alpha1.RetentionStatus{
		LastRunTime: &metav1.Time{Time: now},
		Removed: kubemootv1alpha1.RetentionRemoved{
			Conversations: int32(removed.Conversations), //nolint:gosec // bounded by the bucket size
			Memory:        int32(removed.Memory),        //nolint:gosec // bounded by the bucket size
			Other:         int32(removed.Other),         //nolint:gosec // bounded by the stream size
		},
	}
	if runErr != nil {
		status.Error = runErr.Error()
		log.Error(runErr, "Retention cleanup did not finish")
	} else {
		log.Info("Retention cleanup done", "conversations", removed.Conversations, "memory", removed.Memory, "other", removed.Other)
	}
	orig := crew.DeepCopy()
	crew.Status.Retention = status
	if err := r.Client.Status().Patch(ctx, crew, client.MergeFrom(orig)); err != nil {
		log.Error(err, "Recording the retention cleanup in the Crew status failed")
	}
}

// lastRun is when the crew was last cleaned: the later of the time its status records and
// this runner's own last attempt, else the Crew's creation.
func (r *Runner) lastRun(crew *kubemootv1alpha1.Crew) time.Time {
	last := crew.CreationTimestamp.Time
	if s := crew.Status.Retention; s != nil && s.LastRunTime != nil {
		last = s.LastRunTime.Time
	}
	if v, ok := r.attempts.Load(client.ObjectKeyFromObject(crew)); ok {
		if t := v.(time.Time); t.After(last) {
			last = t
		}
	}
	return last
}

// due reports whether the schedule has a run between the last one and now. The schedule
// is read in UTC, whatever zone the operator runs in.
func due(last time.Time, p Policy, now time.Time) bool {
	return !p.Schedule.Next(last.UTC()).After(now)
}
