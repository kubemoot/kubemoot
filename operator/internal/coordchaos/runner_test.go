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
	"sync"
	"testing"
	"time"
)

// item is one event or message a simulated coordinator writes, in stream order.
type item struct {
	ev  *Event
	msg *Msg
}

// sim replays a healthy thread into a store the way a coordinator would, holds after
// chosen events (the windows the harness aims at), and can be halted by a fault. After
// a halt it behaves as the coordinator that took over: it writes coordinator_resumed,
// optionally a duplicate answer, then finishes the thread.
type sim struct {
	store     *Store
	items     []item
	hold      map[string]time.Duration
	base      uint64
	mu        sync.Mutex
	halted    bool
	resumeIn  time.Duration
	duplicate bool
	resumed   chan struct{}
}

func newSim(store *Store, id string, base uint64, hold map[string]time.Duration) *sim {
	b := healthyThread(id)
	items := make([]item, 0, len(b.evs)+len(b.msgs))
	for i := range b.evs {
		items = append(items, item{ev: &b.evs[i]})
	}
	for i := range b.msgs {
		items = append(items, item{msg: &b.msgs[i]})
	}
	sortItems(items)
	return &sim{store: store, items: items, hold: hold, base: base, resumed: make(chan struct{}, 1)}
}

func sortItems(items []item) {
	seq := func(it item) uint64 {
		if it.ev != nil {
			return it.ev.Seq
		}
		return it.msg.Seq
	}
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && seq(items[j]) < seq(items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func (s *sim) emit(it item) {
	if it.ev != nil {
		e := *it.ev
		e.Seq += s.base
		e.Seen = time.Now()
		s.store.AddEvent(e)
		return
	}
	m := *it.msg
	m.Seq += s.base
	m.Seen = time.Now()
	s.store.AddMsg(m)
}

func (s *sim) halt(resumeIn time.Duration, duplicate bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.halted, s.resumeIn, s.duplicate = true, resumeIn, duplicate
}

func (s *sim) isHalted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.halted
}

// runAll writes everything at once, as a coordinator that is too fast to aim at.
func (s *sim) runAll() {
	for _, it := range s.items {
		s.emit(it)
	}
}

func (s *sim) run(id string) {
	for _, it := range s.items {
		for s.isHalted() {
			s.takeOver(id)
		}
		s.emit(it)
		if d := s.hold[holdKey(it)]; d > 0 {
			s.sleepUnlessHalted(d)
		}
	}
}

func holdKey(it item) string {
	if it.ev != nil {
		return it.ev.Type
	}
	if it.msg != nil {
		return "msg:" + it.msg.MessageType
	}
	return ""
}

func (s *sim) sleepUnlessHalted(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) && !s.isHalted() {
		time.Sleep(2 * time.Millisecond)
	}
}

func (s *sim) takeOver(id string) {
	time.Sleep(s.resumeIn)
	s.store.AddEvent(Event{Seq: s.base + 500, EventID: id + "/resumed", Type: EvCoordinatorResumed, ThreadID: id, Seen: time.Now()})
	if s.duplicate {
		s.store.AddMsg(Msg{Seq: s.base + 501, MessageID: id + "/second-answer", ThreadID: id, MessageType: MsgSynthesis, Seen: time.Now()})
	}
	s.mu.Lock()
	s.halted = false
	s.mu.Unlock()
	select {
	case s.resumed <- struct{}{}:
	default:
	}
}

type fakeAsked struct {
	conv  string
	store *Store
}

func (a *fakeAsked) ConversationID() string { return a.conv }
func (a *fakeAsked) Done() <-chan struct{}  { return nil }
func (a *fakeAsked) Err() error             { return nil }
func (a *fakeAsked) Close()                 {}
func (a *fakeAsked) Syntheses() int {
	n := 0
	for _, id := range a.store.ThreadsFor(a.conv) {
		n += a.store.Thread(id).MsgCount(MsgSynthesis)
	}
	return n
}

type harness struct {
	t          *testing.T
	store      *Store
	mu         sync.Mutex
	sims       []*sim
	hold       map[string]time.Duration
	instant    bool
	pods       []Pod
	signals    []string
	kills      []string
	trials     int
	duplicate  bool
	noCounters bool
	podsStick  bool
	resumeIn   time.Duration
}

func newHarness(t *testing.T, replicas int) *harness {
	h := &harness{t: t, store: NewStore(), resumeIn: 30 * time.Millisecond}
	h.setPods(replicas)
	return h
}

func (h *harness) setPods(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pods = nil
	for i := 0; i < n; i++ {
		h.pods = append(h.pods, Pod{Name: fmt.Sprintf("c-%d-%d", h.trials, i), UID: fmt.Sprintf("u%d-%d", h.trials, i), Ready: true})
	}
}

func (h *harness) Store() *Store                 { return h.store }
func (h *harness) CatchUp(context.Context) error { return nil }
func (h *harness) SetReplicas(_ context.Context, n int) error {
	h.mu.Lock()
	cur := len(h.pods)
	h.mu.Unlock()
	if cur != n {
		h.setPods(n)
	}
	return nil
}

func (h *harness) LeaseHolder(context.Context, string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pods[0].Name, nil
}

func (h *harness) CoordinatorPods(context.Context) ([]Pod, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Pod(nil), h.pods...), nil
}

func (h *harness) Kill(_ context.Context, p Pod) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kills = append(h.kills, p.Name)
	var left []Pod
	for _, q := range h.pods {
		if q.UID != p.UID {
			left = append(left, q)
		}
	}
	if !h.podsStick {
		h.pods = left
	}
	for _, s := range h.sims {
		s.halt(h.resumeIn, h.duplicate)
	}
	// The replacement pod appears once the new coordinator has taken over.
	go func() {
		time.Sleep(h.resumeIn)
		h.mu.Lock()
		h.pods = append(h.pods, Pod{Name: p.Name + "-r", UID: p.UID + "-r", Ready: true})
		h.mu.Unlock()
	}()
	return nil
}

func (h *harness) Signal(_ context.Context, _ Pod, sig string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.signals = append(h.signals, sig)
	if sig == sigStop {
		for _, s := range h.sims {
			s.halt(h.resumeIn, h.duplicate)
		}
	}
	return nil
}

func (h *harness) Counters(context.Context, Pod) (Counters, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.noCounters && len(h.kills)+len(h.signals) > 0 {
		return nil, context.DeadlineExceeded
	}
	return Counters{}, nil
}

func (h *harness) Ask(_ context.Context, _ string) (Asked, error) {
	h.mu.Lock()
	h.trials++
	n := h.trials
	h.mu.Unlock()
	id := fmt.Sprintf("t%d", n)
	s := newSim(h.store, id, uint64(n)*1000, h.hold)
	h.mu.Lock()
	h.sims = append(h.sims, s)
	h.mu.Unlock()
	if h.instant {
		s.runAll()
	} else {
		go s.run(id)
	}
	return &fakeAsked{conv: "conv-" + id, store: h.store}, nil
}

func (h *harness) runner() *Runner {
	return &Runner{
		Cfg: Config{
			PointTimeout: 5 * time.Second, SettleTimeout: 5 * time.Second, Quiet: 20 * time.Millisecond,
			Poll: 2 * time.Millisecond, DeathQuiet: 20 * time.Millisecond, Question: "q",
		},
		Cluster: h, Asker: h, Bus: h,
	}
}

var holdSynthesis = map[string]time.Duration{EvSynthesisStarted: 300 * time.Millisecond}

func kill(p Point) Case {
	c := newCase(ActionKill, p, 1, ModeProtected, protectedFlags(false))
	c.Attempts = 2
	return c
}

func TestKillAtSynthesisStartedLandsAndPasses(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = holdSynthesis
	res := h.runner().Run(context.Background(), []Case{kill(PointSynthesisStarted)})[0]
	if res.Outcome != OutcomePass {
		t.Fatalf("outcome %s: %+v", res.Outcome, res.Trials)
	}
	tr := res.Trials[0]
	if !tr.Landed || len(h.kills) != 1 || tr.AnswerSeconds <= 0 || tr.ClosedSeconds <= 0 {
		t.Fatalf("trial %+v kills %v", tr, h.kills)
	}
	if tr.ClosedSeconds < tr.AnswerSeconds {
		t.Errorf("closed (%v) before the answer (%v)", tr.ClosedSeconds, tr.AnswerSeconds)
	}
}

func TestDuplicateAnswerAfterTakeoverFails(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = holdSynthesis
	h.duplicate = true
	res := h.runner().Run(context.Background(), []Case{kill(PointSynthesisStarted)})[0]
	if res.Outcome != OutcomeFail {
		t.Fatalf("outcome %s", res.Outcome)
	}
	if got := names(res.Trials[0].Violations); got != "one_answer_on_bus,one_answer_to_asker,duplicate_message" {
		t.Fatalf("violations %s", got)
	}
}

func TestAThreadTooFastToAimAtIsNotReachedNotAPass(t *testing.T) {
	h := newHarness(t, 1)
	h.instant = true
	res := h.runner().Run(context.Background(), []Case{kill(PointEvaluationStarted)})[0]
	if res.Outcome != OutcomeNotReached || res.Attempts != 2 {
		t.Fatalf("outcome %s after %d attempts", res.Outcome, res.Attempts)
	}
	if len(h.kills) != 0 {
		t.Fatalf("a kill was sent although the thread had passed the point: %v", h.kills)
	}
}

func TestBaselineInjectsNothingAndPasses(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = map[string]time.Duration{EvSynthesisStarted: 10 * time.Millisecond}
	c := Matrix(ModeProtected)[0]
	res := h.runner().Run(context.Background(), []Case{c})[0]
	if res.Outcome != OutcomePass || len(h.kills)+len(h.signals) != 0 {
		t.Fatalf("outcome %s kills %v signals %v trials %+v", res.Outcome, h.kills, h.signals, res.Trials)
	}
}

func TestZombieIsFrozenUntilAnotherReplicaTakesOverThenContinued(t *testing.T) {
	h := newHarness(t, 2)
	h.hold = map[string]time.Duration{EvEvaluationStarted: 300 * time.Millisecond}
	c := newCase(ActionStop, PointEvaluationStarted, 2, ModeProtected, protectedFlags(true))
	res := h.runner().Run(context.Background(), []Case{c})[0]
	if res.Outcome != OutcomePass {
		t.Fatalf("outcome %s: %+v", res.Outcome, res.Trials)
	}
	if fmt.Sprint(h.signals) != "[STOP CONT]" {
		t.Fatalf("signals %v", h.signals)
	}
	if !res.Trials[0].Landed {
		t.Fatal("the freeze did not land on the point")
	}
}

func TestKillHolderTargetsTheLeaseHolder(t *testing.T) {
	h := newHarness(t, 2)
	h.hold = holdSynthesis
	c := newCase(ActionKillHolder, PointSynthesisStarted, 2, ModeProtected, protectedFlags(true))
	res := h.runner().Run(context.Background(), []Case{c})[0]
	if res.Outcome != OutcomePass || len(h.kills) != 1 || h.kills[0] != "c-0-0" {
		t.Fatalf("outcome %s kills %v", res.Outcome, h.kills)
	}
}

func TestControlRunReportsTheLossAsExpected(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = holdSynthesis
	// A coordinator without the protections never comes back for the thread: nothing
	// resumes it, so the answer never arrives. The sim stays halted.
	h.resumeIn = time.Hour
	r := h.runner()
	r.Cfg.SettleTimeout = 150 * time.Millisecond
	c := newCase(ActionKill, PointSynthesisStarted, 1, ModeControl, Flags{})
	res := r.Run(context.Background(), []Case{c})[0]
	if res.Outcome != OutcomeExpectedLoss {
		t.Fatalf("outcome %s: %+v", res.Outcome, res.Trials)
	}
}

func TestPollGivesUpWhenTheContextEnds(t *testing.T) {
	r := &Runner{Cfg: Config{Poll: time.Millisecond}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.poll(ctx, "never", func() (bool, error) { return false, nil }); err == nil {
		t.Fatal("poll returned nil")
	}
	want := fmt.Errorf("boom")
	if err := r.poll(context.Background(), "x", func() (bool, error) { return false, want }); err != want {
		t.Fatalf("got %v", err)
	}
}

func TestACrashLoopingCoordinatorIsAnErrorNotAWait(t *testing.T) {
	h := newHarness(t, 1)
	h.pods[0].WaitingReason = "CrashLoopBackOff"
	r := h.runner()
	r.Cfg.PointTimeout = time.Second
	res := r.Run(context.Background(), []Case{kill(PointSynthesisStarted)})[0]
	if res.Outcome != OutcomeError || res.Trials[0].Error == "" {
		t.Fatalf("outcome %s: %+v", res.Outcome, res.Trials)
	}
}
