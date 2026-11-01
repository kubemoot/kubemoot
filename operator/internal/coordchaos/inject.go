/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"context"
	"fmt"
	"time"
)

// defaultDeathQuiet is how long the runner lets a killed or frozen coordinator's last writes
// reach the streams before it reads the state to decide where the fault landed. It is an
// observation window: a process that is gone writes nothing more.
const defaultDeathQuiet = 2 * time.Second

func (r *Runner) deathQuiet() time.Duration {
	if r.Cfg.DeathQuiet > 0 {
		return r.Cfg.DeathQuiet
	}
	return defaultDeathQuiet
}

const (
	sigStop = "STOP"
	sigCont = "CONT"
)

// target picks the pod the fault goes to: the only coordinator pod, or with several the
// replica that holds the thread's lease, read from the lease bucket.
func (r *Runner) target(ctx context.Context, thread string) (Pod, error) {
	pods, err := r.Cluster.CoordinatorPods(ctx)
	if err != nil {
		return Pod{}, err
	}
	if len(pods) == 1 {
		return pods[0], nil
	}
	holder, err := r.Bus.LeaseHolder(ctx, thread)
	if err != nil {
		return Pod{}, fmt.Errorf("finding the lease holder of %s: %w", thread, err)
	}
	for _, p := range pods {
		if p.Name == holder {
			return p, nil
		}
	}
	return Pod{}, fmt.Errorf("lease holder %q of %s is not a live coordinator pod", holder, thread)
}

// inject puts the case's fault into the coordinator and records when and where.
func (r *Runner) inject(ctx context.Context, c Case, f *fault) error {
	pod, err := r.target(ctx, f.thread)
	if err != nil {
		return err
	}
	switch c.Action {
	case ActionKill, ActionKillHolder:
		err = r.Cluster.Kill(ctx, pod)
	case ActionStop:
		err = r.Cluster.Signal(ctx, pod, sigStop)
	default:
		err = fmt.Errorf("unknown action %q", c.Action)
	}
	if err != nil {
		return fmt.Errorf("%s on %s: %w", c.Action, pod.Name, err)
	}
	f.at, f.injected, f.pod = r.clock(), true, pod
	f.limit = time.Now().Add(r.Cfg.SettleTimeout) // real time: it is the deadline of a context
	r.logf("  %s on %s at %s", c.Action, pod.Name, c.Point)
	return nil
}

// postFault returns a context that ends SettleTimeout after the fault. Every wait after
// the fault runs under it, in both modes, so a thread that never answers (the expected
// result in control mode) ends the trial at the limit instead of waiting on a state that
// will not come.
func (r *Runner) postFault(ctx context.Context, f fault) (context.Context, context.CancelFunc) {
	if f.injected {
		return context.WithDeadline(ctx, f.limit)
	}
	return context.WithTimeout(ctx, r.Cfg.SettleTimeout)
}

// catchUpWait is the safety net for reading the streams to their end.
const catchUpWait = 30 * time.Second

// contTimeout bounds the SIGCONT that lets a frozen coordinator go; it outlives a cancelled run.
const contTimeout = 30 * time.Second

// judgeLanding records whether the fault landed on the point and a note when it did not.
func (r *Runner) judgeLanding(ctx context.Context, c Case, f fault, t *Trial) {
	lctx, cancel := r.postFault(ctx, f)
	defer cancel()
	landed, why := r.landed(lctx, c, f)
	t.Landed = landed
	if !landed {
		t.Notes = append(t.Notes, why)
	}
}

// landed reports whether the fault met the thread at the point. With a coordinator log
// that is the log's frontier read once the coordinator could write no more. Without one
// (control mode) the log cannot say, so the fault landed when the thread was open at the
// point when the kill went in and had not finished once the coordinator was gone.
func (r *Runner) landed(ctx context.Context, c Case, f fault) (bool, string) {
	passed := "the coordinator passed " + string(c.Point) + " before it stopped; the fault landed later"
	if c.Action != ActionStop {
		if err := r.awaitPodGone(ctx, f.pod); err != nil {
			return false, "the killed pod did not leave the pod list: " + err.Error()
		}
	}
	select {
	case <-ctx.Done():
		return false, "the post-fault limit ended before the coordinator could be shown stopped"
	case <-time.After(r.deathQuiet()):
	}
	cctx, cancel := context.WithTimeout(ctx, catchUpWait)
	defer cancel()
	if err := r.Bus.CatchUp(cctx); err != nil {
		return false, "the streams could not be read after the fault: " + err.Error()
	}
	th := r.Bus.Store().Thread(f.thread)
	if c.Mode == ModeControl {
		return !threadOver(th) && c.Point.HoldsMsgs(th), passed
	}
	return c.Point.HoldsIn(th, c.Mode), passed
}

// awaitPodGone waits until no live pod has the killed pod's UID.
func (r *Runner) awaitPodGone(ctx context.Context, gone Pod) error {
	return r.poll(ctx, "the killed pod to leave the pod list", func() (bool, error) {
		pods, err := r.Cluster.CoordinatorPods(ctx)
		if err != nil {
			return false, err
		}
		for _, p := range pods {
			if p.UID == gone.UID {
				return false, nil
			}
		}
		return true, nil
	})
}

// settle waits for the thread to close and the asker to have its answer. A zombie is
// let go once another replica has taken the thread over. A wait that runs out is a
// result, not an error: the checks then report the missing answer.
func (r *Runner) settle(ctx context.Context, c Case, asked Asked, f fault, t *Trial) error {
	sctx, cancel := r.postFault(ctx, f)
	defer cancel()
	if c.Action == ActionStop && f.injected {
		err := r.awaitTakeover(sctx, f)
		cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), contTimeout)
		cerr := r.Cluster.Signal(cctx, f.pod, sigCont)
		ccancel()
		if cerr != nil {
			return fmt.Errorf("resuming the frozen coordinator %s: %w", f.pod.Name, cerr)
		}
		if err != nil {
			t.Notes = append(t.Notes, "no replica took the thread over while the holder was frozen: "+err.Error())
		}
	}
	err := r.poll(sctx, "the thread to close and the answer to arrive", func() (bool, error) {
		if err := r.streamHealthy(sctx, asked); err != nil {
			return false, err
		}
		return r.answered(asked), nil
	})
	if err != nil && ctx.Err() == nil && sctx.Err() != nil {
		t.Notes = append(t.Notes, "the thread did not settle within "+r.Cfg.SettleTimeout.String())
		return nil
	}
	return err
}

func (r *Runner) awaitTakeover(ctx context.Context, f fault) error {
	return r.poll(ctx, "another replica to resume the thread", func() (bool, error) {
		return r.Bus.Store().Thread(f.thread).Has(EvCoordinatorResumed), nil
	})
}

// answered reports whether some thread of the question is over and the asker has an answer.
func (r *Runner) answered(asked Asked) bool {
	if asked.Syntheses() == 0 {
		return false
	}
	for _, id := range r.Bus.Store().ThreadsFor(asked.ConversationID()) {
		if threadSettled(r.Bus.Store().Thread(id)) {
			return true
		}
	}
	return false
}

// threadSettled is the end of a thread as the available state shows it: the log's
// thread_closed when there is a log, the thread_close message when there is not.
func threadSettled(t Thread) bool {
	if len(t.Events) > 0 {
		return t.Closed()
	}
	return t.MsgCount(MsgThreadClose) > 0
}

// finish watches for late duplicates, reads the final state, and runs the checks.
func (r *Runner) finish(ctx context.Context, c Case, asked Asked, f fault, before map[string]Counters, t Trial) Trial {
	select {
	case <-ctx.Done():
	case <-time.After(r.Cfg.Quiet):
	}
	cctx, cancelCatchUp := context.WithTimeout(ctx, catchUpWait)
	defer cancelCatchUp()
	if err := r.Bus.CatchUp(cctx); err != nil {
		t.Error = fmt.Sprintf("reading the streams: %v", err)
		return t
	}
	obs := r.observe(asked, c.Mode)
	unreadable := r.readCounters(ctx, c.Mode, before, &obs, &t)
	t.Violations = append(Evaluate(obs, expectation(c, f, t.Landed)), unreadable...)
	if c.Mode == ModeControl {
		t.Notes = append(t.Notes, lossEvidence(obs)...)
	}
	t.Threads = threadIDs(obs)
	if len(obs.Threads) > 0 {
		t.LogTypes = obs.Threads[0].Types()
	}
	if f.injected {
		t.AnswerSeconds, t.ClosedSeconds = r.recovery(obs, f.at)
	}
	return t
}

// expectation is what the log should show for the trial: one resume after a fault that
// landed, none when no fault went in, and zero or one when the coordinator got past the
// point before it stopped (if it had already closed the thread there is nothing to resume).
func expectation(c Case, f fault, landed bool) Expect {
	exp := c.Expect()
	switch {
	case c.Action != ActionNone && !f.injected:
		exp.ResumedMin, exp.ResumedMax = 0, 0
	case c.Action != ActionNone && !landed:
		exp.ResumedMin, exp.ResumedMax = 0, 1
	}
	return exp
}

// defaultMetricsWait is the safety net for the replacement coordinator to serve its counters.
const defaultMetricsWait = 2 * time.Minute

func (r *Runner) metricsWait() time.Duration {
	wait := defaultMetricsWait
	if r.Cfg.MetricsWait > 0 {
		wait = r.Cfg.MetricsWait
	}
	if limit := r.Cfg.SettleTimeout; limit > 0 && limit < wait {
		wait = limit
	}
	return wait
}

// readCounters reads the counters after the trial, waiting for a replacement pod to
// serve them. In protected mode counters that cannot be read are a violation, because
// the metrics check could not fail without them.
func (r *Runner) readCounters(ctx context.Context, mode Mode, before map[string]Counters, obs *Observation,
	t *Trial) []Violation {
	mctx, cancel := context.WithTimeout(ctx, r.metricsWait())
	defer cancel()
	var after map[string]Counters
	err := r.poll(mctx, "the coordinator counters", func() (bool, error) {
		var rerr error
		after, rerr = r.snapshotCounters(mctx)
		return rerr == nil, nil
	})
	if err == nil {
		obs.Metrics = Delta(before, after)
		t.Metrics = obs.Metrics
		return nil
	}
	t.Notes = append(t.Notes, "counters not read after the trial: "+err.Error())
	if mode == ModeProtected && ctx.Err() == nil {
		return []Violation{{Check: "metrics_unreadable", Detail: "the coordinator counters could not be read after the trial"}}
	}
	return nil
}

// lossEvidence states what a control trial shows of the loss: whether each thread is
// still open and how many answers the thread and the asker got.
func lossEvidence(obs Observation) []string {
	out := make([]string, 0, len(obs.Threads)+1)
	for _, th := range obs.Threads {
		out = append(out, fmt.Sprintf("thread %s: open=%t, %d messages, %d answers, %d thread_close",
			th.ID, !threadSettled(th), len(th.Msgs), th.MsgCount(MsgSynthesis), th.MsgCount(MsgThreadClose)))
	}
	return append(out, fmt.Sprintf("the asker received %d answers", obs.AskerSyntheses))
}

// observe collects the state the checks read.
func (r *Runner) observe(asked Asked, mode Mode) Observation {
	store := r.Bus.Store()
	ids := store.ThreadsFor(asked.ConversationID())
	obs := Observation{AskerSyntheses: asked.Syntheses(), Metrics: map[string]float64{}}
	for _, id := range ids {
		obs.Threads = append(obs.Threads, store.Thread(id))
	}
	if mode == ModeProtected {
		keep := map[string]bool{}
		for _, id := range ids {
			keep[id] = true
		}
		obs.OpenAtEnd = store.OpenThreads(func(id string) bool { return keep[id] })
	}
	return obs
}

// recovery measures, on the harness clock, how long after the fault the answer reached
// the discussion stream and the thread closed in the log. Zero means it did not.
func (r *Runner) recovery(obs Observation, faultAt time.Time) (answer, closed float64) {
	for _, th := range obs.Threads {
		if m := th.MsgsOf(MsgSynthesis); len(m) > 0 && answer == 0 {
			answer = positive(m[0].Seen.Sub(faultAt))
		}
		if ev, ok := lastEvent(th, EvThreadClosed); ok && closed == 0 {
			closed = positive(ev.Seen.Sub(faultAt))
		}
	}
	return answer, closed
}

func positive(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return d.Seconds()
}
