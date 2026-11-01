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
	r.logf("  %s on %s at %s", c.Action, pod.Name, c.Point)
	return nil
}

// landed reports whether the thread still sat at the point once the coordinator could
// write no more. That is the state at the moment of death, read after the fact.
func (r *Runner) landed(ctx context.Context, c Case, f fault) bool {
	if c.Action != ActionStop {
		if err := r.poll(ctx, "the killed pod to leave the pod list", func() (bool, error) {
			pods, err := r.Cluster.CoordinatorPods(ctx)
			if err != nil {
				return false, err
			}
			for _, p := range pods {
				if p.UID == f.pod.UID {
					return false, nil
				}
			}
			return true, nil
		}); err != nil {
			return false
		}
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(r.deathQuiet()):
	}
	if err := r.Bus.CatchUp(ctx); err != nil {
		return false
	}
	return c.Point.Holds(r.Bus.Store().Thread(f.thread))
}

// settle waits for the thread to close and the asker to have its answer. A zombie is
// let go once another replica has taken the thread over. A wait that runs out is a
// result, not an error: the checks then report the missing answer.
func (r *Runner) settle(ctx context.Context, c Case, asked Asked, f fault, t *Trial) error {
	sctx, cancel := context.WithTimeout(ctx, r.Cfg.SettleTimeout)
	defer cancel()
	if c.Action == ActionStop && f.injected {
		err := r.awaitTakeover(sctx, f)
		if cerr := r.Cluster.Signal(context.WithoutCancel(ctx), f.pod, sigCont); cerr != nil {
			return fmt.Errorf("resuming the frozen coordinator %s: %w", f.pod.Name, cerr)
		}
		if err != nil {
			t.Notes = append(t.Notes, "no replica took the thread over while the holder was frozen: "+err.Error())
		}
	}
	err := r.poll(sctx, "the thread to close and the answer to arrive", func() (bool, error) {
		if err := r.streamHealthy(ctx, asked); err != nil {
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
	if err := r.Bus.CatchUp(ctx); err != nil {
		t.Error = fmt.Sprintf("reading the streams: %v", err)
		return t
	}
	obs := r.observe(asked, c.Mode)
	if after, err := r.snapshotCounters(ctx); err == nil {
		obs.Metrics = Delta(before, after)
		t.Metrics = obs.Metrics
	} else {
		t.Notes = append(t.Notes, "counters not read after the trial: "+err.Error())
	}
	exp := c.Expect()
	if c.Action != ActionNone && !f.injected {
		exp.ResumedMin, exp.ResumedMax = 0, 0
	}
	t.Violations = Evaluate(obs, exp)
	t.Threads = threadIDs(obs)
	if len(obs.Threads) > 0 {
		t.LogTypes = obs.Threads[0].Types()
	}
	if f.injected {
		t.AnswerSeconds, t.ClosedSeconds = r.recovery(obs, f.at)
	}
	return t
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
