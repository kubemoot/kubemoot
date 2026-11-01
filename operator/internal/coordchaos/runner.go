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
	"errors"
	"fmt"
	"time"
)

// Pod is a coordinator pod as the harness needs to see it.
type Pod struct {
	Name        string
	UID         string
	Node        string
	ContainerID string
	Ready       bool
	// WaitingReason is the container's waiting reason, such as CrashLoopBackOff.
	WaitingReason string
	// Env is the agent container's environment.
	Env map[string]string
}

// Cluster is what the runner needs from Kubernetes.
type Cluster interface {
	// CoordinatorPods lists the live (not terminating) pods of the coordinator Agent.
	CoordinatorPods(ctx context.Context) ([]Pod, error)
	// SetReplicas sets the coordinator Agent's replicas and returns once it is set.
	SetReplicas(ctx context.Context, n int) error
	// Kill force-deletes the pod with a grace period of 0.
	Kill(ctx context.Context, pod Pod) error
	// Signal sends STOP or CONT to every process of the pod's agent container.
	Signal(ctx context.Context, pod Pod, sig string) error
	// Counters reads the coordinator counters of the pod.
	Counters(ctx context.Context, pod Pod) (Counters, error)
}

// Asked is a question in flight. Its answers are counted from the asker's own stream.
type Asked interface {
	ConversationID() string
	// Syntheses is how many answers the stream has delivered so far.
	Syntheses() int
	// Done is closed when the stream ended.
	Done() <-chan struct{}
	// Err is a stream failure, nil while the stream is healthy or ended normally.
	Err() error
	Close()
}

// Asker asks a crew a question through the path a person's client uses.
type Asker interface {
	Ask(ctx context.Context, question string) (Asked, error)
}

// Bus reads the streams and the lease bucket.
type Bus interface {
	Store() *Store
	// CatchUp returns once the store holds everything the streams hold for the crew.
	CatchUp(ctx context.Context) error
	// LeaseHolder returns the pod that holds the thread's lease.
	LeaseHolder(ctx context.Context, threadID string) (string, error)
}

// Config tunes the runner. Every duration is a safety net or an observation window,
// none of them paces the flow: the runner moves on when the state says so.
type Config struct {
	Namespace string
	Crew      string
	// PointTimeout bounds the wait for a thread to reach the kill point.
	PointTimeout time.Duration
	// SettleTimeout bounds the wait, after the fault, for the thread to close.
	SettleTimeout time.Duration
	// Quiet is how long the runner watches after the thread settled for a late duplicate.
	Quiet time.Duration
	// DeathQuiet is how long a killed or frozen coordinator's last writes get to reach the streams
	// before the runner reads where the fault landed. Zero means 2 seconds.
	DeathQuiet time.Duration
	// MetricsWait is a safety net for the replacement coordinator to serve its counters. Zero means 2 minutes.
	MetricsWait time.Duration
	// Poll is how often state is read.
	Poll time.Duration
	// Question is the text asked; the runner appends a marker that names the trial.
	Question string
}

// Runner runs the matrix.
type Runner struct {
	Cfg     Config
	Cluster Cluster
	Asker   Asker
	Bus     Bus
	Log     func(format string, args ...any)
	now     func() time.Time
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r *Runner) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// poll reads state every Poll until cond reports done, cond fails, or ctx ends.
func (r *Runner) poll(ctx context.Context, what string, cond func() (bool, error)) error {
	interval := r.Cfg.Poll
	if interval <= 0 {
		interval = defaultPollInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for %s: %w", what, err)
		}
		done, err := cond()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
		case <-tick.C:
		}
	}
}

var failingWaits = map[string]bool{
	"CrashLoopBackOff": true, "ImagePullBackOff": true, "ErrImagePull": true,
	"CreateContainerConfigError": true, "InvalidImageName": true,
}

// healthy fails on a state that will not recover by waiting: a coordinator pod that
// cannot start.
func (r *Runner) healthy(ctx context.Context) error {
	pods, err := r.Cluster.CoordinatorPods(ctx)
	if err != nil {
		return err
	}
	for _, p := range pods {
		if failingWaits[p.WaitingReason] {
			return fmt.Errorf("coordinator pod %s is %s", p.Name, p.WaitingReason)
		}
	}
	return nil
}

// Run runs the cases in order and returns the report. It restores the coordinator's
// replica count when it ends.
func (r *Runner) Run(ctx context.Context, cases []Case) []CaseResult {
	results := make([]CaseResult, 0, len(cases))
	for _, c := range cases {
		r.logf("case %s", c.Name)
		res := r.runCase(ctx, c)
		r.logf("case %s: %s after %d trial(s)", c.Name, res.Outcome, res.Attempts)
		results = append(results, res)
		if ctx.Err() != nil {
			break
		}
	}
	return results
}

func (r *Runner) runCase(ctx context.Context, c Case) CaseResult {
	var trials []Trial
	for n := 1; n <= c.Attempts; n++ {
		t := r.runTrial(ctx, c, n)
		trials = append(trials, t)
		r.logf("  trial %d: landed=%t violations=%d error=%q", n, t.Landed, len(t.Violations), t.Error)
		if t.Landed || t.Error != "" || ctx.Err() != nil {
			break
		}
	}
	return CaseResult{Case: c, Outcome: Classify(c, trials), Attempts: len(trials), Trials: trials}
}

// errTrial marks a failure of the harness or the cluster, as opposed to a result.
func errTrial(n int, err error) Trial { return Trial{Number: n, Error: err.Error()} }

func (r *Runner) runTrial(ctx context.Context, c Case, n int) Trial {
	if err := r.ensureReplicas(ctx, c.Replicas); err != nil {
		return errTrial(n, err)
	}
	before, err := r.snapshotCounters(ctx)
	if err != nil {
		return errTrial(n, err)
	}
	question := fmt.Sprintf("%s [chaos %s trial %d %d]", r.Cfg.Question, c.Name, n, r.clock().UnixNano())
	asked, err := r.Asker.Ask(ctx, question)
	if err != nil {
		return errTrial(n, fmt.Errorf("asking: %w", err))
	}
	defer asked.Close()
	t := Trial{Number: n}
	fault, err := r.driveToFault(ctx, c, asked, &t)
	if err != nil {
		t.Error = err.Error()
		return t
	}
	if fault.injected {
		r.judgeLanding(ctx, c, fault, &t)
	}
	if err := r.settle(ctx, c, asked, fault, &t); err != nil {
		t.Error = err.Error()
		return t
	}
	return r.finish(ctx, c, asked, fault, before, t)
}

// fault records when and where the fault went in.
type fault struct {
	at time.Time
	// limit is the real-time end of every wait after the fault.
	limit    time.Time
	injected bool
	pod      Pod
	thread   string
}

// driveToFault waits for the kill point, injects the fault, and records whether it
// landed on the point. A baseline injects nothing.
func (r *Runner) driveToFault(ctx context.Context, c Case, asked Asked, t *Trial) (fault, error) {
	if c.Action == ActionNone {
		t.Landed = true // nothing to aim: the baseline is a complete run
		return fault{}, nil
	}
	f := fault{}
	pctx, cancel := context.WithTimeout(ctx, r.Cfg.PointTimeout)
	defer cancel()
	thread, reached, err := r.awaitPoint(pctx, c.Point, c.Mode, asked)
	if err != nil {
		return f, err
	}
	f.thread = thread
	if !reached {
		t.Notes = append(t.Notes, "the thread passed "+string(c.Point)+" before the harness acted; no fault injected")
		return f, nil
	}
	if err := r.inject(ctx, c, &f); err != nil {
		return f, err
	}
	return f, nil
}

var errMissed = errors.New("thread passed the point")

// awaitPoint waits for the question's thread to sit at the point. It returns reached=false
// when the thread closed without the harness seeing it there.
func (r *Runner) awaitPoint(ctx context.Context, p Point, m Mode, asked Asked) (string, bool, error) {
	var thread string
	reached := false
	err := r.poll(ctx, "the thread to reach "+string(p), func() (bool, error) {
		if err := r.streamHealthy(ctx, asked); err != nil {
			return false, err
		}
		ids := r.Bus.Store().ThreadsFor(asked.ConversationID())
		if len(ids) == 0 {
			return false, nil
		}
		th := r.Bus.Store().Thread(ids[0])
		thread = ids[0]
		if p.HoldsIn(th, m) {
			reached = true
			return true, nil
		}
		if threadOver(th) {
			return false, errMissed
		}
		return false, nil
	})
	if errors.Is(err, errMissed) {
		return thread, false, nil
	}
	return thread, reached, err
}

func (r *Runner) streamHealthy(ctx context.Context, asked Asked) error {
	if err := asked.Err(); err != nil {
		return fmt.Errorf("the asker's stream failed: %w", err)
	}
	return r.healthy(ctx)
}

// threadOver reports whether the thread ended: closed in the log, or thread_close seen.
func threadOver(t Thread) bool { return t.Closed() || t.MsgCount(MsgThreadClose) > 0 }

func (r *Runner) ensureReplicas(ctx context.Context, want int) error {
	wctx, cancel := context.WithTimeout(ctx, r.Cfg.PointTimeout)
	defer cancel()
	if err := r.Cluster.SetReplicas(wctx, want); err != nil {
		return fmt.Errorf("setting replicas to %d: %w", want, err)
	}
	return r.poll(wctx, fmt.Sprintf("%d ready coordinator pod(s)", want), func() (bool, error) {
		if err := r.healthy(wctx); err != nil {
			return false, err
		}
		pods, err := r.Cluster.CoordinatorPods(wctx)
		if err != nil {
			return false, err
		}
		ready := 0
		for _, p := range pods {
			if p.Ready {
				ready++
			}
		}
		return ready == want && len(pods) == want, nil
	})
}

func (r *Runner) snapshotCounters(ctx context.Context) (map[string]Counters, error) {
	pods, err := r.Cluster.CoordinatorPods(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Counters{}
	for _, p := range pods {
		c, err := r.Cluster.Counters(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("reading counters of %s: %w", p.Name, err)
		}
		out[p.UID] = c
	}
	return out, nil
}
