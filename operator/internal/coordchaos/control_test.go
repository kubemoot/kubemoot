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
	"strings"
	"testing"
	"time"
)

// holdStart keeps the simulated coordinator at the thread_start message, the control
// mode's thread_opened point, long enough for the harness to aim.
var holdStart = map[string]time.Duration{"msg:" + MsgThreadStart: 400 * time.Millisecond}

func controlKill(p Point) Case {
	c := newCase(ActionKill, p, 1, ModeControl, Flags{})
	c.Attempts = 1
	return c
}

// A coordinator without the protections never comes back for its thread. The kill must
// end as expected_loss inside the settle timeout, with the evidence of the loss, and
// must not wait for an answer, a close or a later point.
func TestControlKillAtThreadOpenedEndsAsExpectedLossWithinTheSettleTimeout(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = holdStart
	h.resumeIn = time.Hour // nothing resumes the thread
	r := h.runner()
	r.Cfg.SettleTimeout = 300 * time.Millisecond
	r.Cfg.PointTimeout = 5 * time.Second
	r.Cfg.MetricsWait = 5 * time.Second // the bound must come from the settle timeout

	started := time.Now()
	done := make(chan CaseResult, 1)
	go func() { done <- r.Run(context.Background(), []Case{controlKill(PointThreadOpened)})[0] }()
	var res CaseResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the control trial did not end: a wait after the fault is not bounded")
	}
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("took %s with a settle timeout of 300ms", took)
	}
	checkExpectedLoss(t, res)
}

func checkExpectedLoss(t *testing.T, res CaseResult) {
	t.Helper()
	if res.Outcome != OutcomeExpectedLoss {
		t.Fatalf("outcome %s: %+v", res.Outcome, res.Trials)
	}
	tr := res.Trials[0]
	if !tr.Landed || tr.Error != "" {
		t.Fatalf("landed=%t error=%q", tr.Landed, tr.Error)
	}
	evidence := strings.Join(tr.Notes, "\n")
	for _, want := range []string{"open=true", "0 answers", "the asker received 0 answers"} {
		if !strings.Contains(evidence, want) {
			t.Errorf("evidence lacks %q:\n%s", want, evidence)
		}
	}
	md := Report{Cases: []CaseResult{res}}.Markdown()
	if !strings.Contains(md, "the asker received 0 answers") || !strings.Contains(md, "expected_loss") {
		t.Errorf("report lacks the evidence:\n%s", md)
	}
}

// A killed pod that stays in the pod list must not hold the trial past the limit.
func TestAPodThatNeverLeavesTheListEndsTheTrialAtTheLimit(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = holdStart
	h.resumeIn = time.Hour
	h.podsStick = true
	r := h.runner()
	r.Cfg.SettleTimeout = 300 * time.Millisecond
	done := make(chan CaseResult, 1)
	go func() { done <- r.Run(context.Background(), []Case{controlKill(PointThreadOpened)})[0] }()
	select {
	case res := <-done:
		if res.Trials[0].Landed || !strings.Contains(strings.Join(res.Trials[0].Notes, " "), "did not leave the pod list") {
			t.Fatalf("trial %+v", res.Trials[0])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the trial waited on a pod that never left the list")
	}
}

// The same bound holds in protected mode: a coordinator that never resumes ends the
// trial at the settle timeout with the missing answer reported as a violation.
func TestProtectedKillThatNeverResumesEndsAtTheSettleTimeout(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = holdSynthesis
	h.resumeIn = time.Hour
	r := h.runner()
	r.Cfg.SettleTimeout = 300 * time.Millisecond
	done := make(chan CaseResult, 1)
	go func() { done <- r.Run(context.Background(), []Case{kill(PointSynthesisStarted)})[0] }()
	select {
	case res := <-done:
		if res.Outcome != OutcomeFail || !strings.Contains(names(res.Trials[0].Violations), "one_answer_on_bus") {
			t.Fatalf("outcome %s: %s", res.Outcome, names(res.Trials[0].Violations))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the protected trial did not end")
	}
}

// The kill arrives after the coordinator already published the answer: the fault did not
// land at synthesis_started, and a control trial must not claim it did.
func TestControlFaultAfterThePointIsNotLanded(t *testing.T) {
	h := newHarness(t, 1)
	st := h.store
	st.AddMsg(Msg{Seq: 1, ThreadID: "x", MessageType: MsgThreadStart, ConversationID: "c", Seen: time.Now()})
	st.AddMsg(Msg{Seq: 2, ThreadID: "x", MessageType: signalAgree, Seen: time.Now()})
	r := h.runner()
	f := fault{thread: "x", injected: true, at: time.Now(), limit: time.Now().Add(5 * time.Second), pod: Pod{UID: "gone"}}
	c := controlKill(PointSynthesisStarted)
	if landed, _ := r.landed(context.Background(), c, f); !landed {
		t.Fatal("a thread with signals and no answer is at synthesis_started")
	}
	st.AddMsg(Msg{Seq: 3, ThreadID: "x", MessageType: MsgSynthesis, Seen: time.Now()})
	if landed, _ := r.landed(context.Background(), c, f); landed {
		t.Fatal("the answer was already published, so the kill did not land at synthesis_started")
	}
}

func TestAControlTrialWithAHarnessErrorIsNotAClaimOfNoLoss(t *testing.T) {
	c := controlKill(PointThreadOpened)
	got := Classify(c, []Trial{{Landed: true, Error: "the asker's stream failed"}})
	if got != OutcomeError {
		t.Fatalf("got %s", got)
	}
}

func TestPollChecksTheContextBeforeAskingAgain(t *testing.T) {
	r := &Runner{Cfg: Config{Poll: time.Millisecond}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := r.poll(ctx, "x", func() (bool, error) { called = true; return false, nil })
	if err == nil || called {
		t.Fatalf("err %v, condition called %t", err, called)
	}
}
