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
	"strings"
	"testing"
	"time"
)

const (
	nsGet   = "get namespace kubemoot-chaos --ignore-not-found"
	jsonKey = "jsonpath"
	tNS     = "kubemoot-chaos"
	nsOut   = "namespace/kubemoot-chaos"
)

func joined(r *recorded) []string {
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func anyContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestSignalProceedsInANamespaceItCreated(t *testing.T) {
	r := &recorded{out: map[string]string{nsGet: nsOut, jsonKey: "coordinator-chaos"}}
	c := newKube(r)
	pod := Pod{Name: "p1", Node: "n", ContainerID: tContainerID}
	if err := c.Signal(context.Background(), pod, sigStop); err != nil {
		t.Fatal(err)
	}
	if !anyContains(joined(r), " apply -f -") {
		t.Fatal("did not apply into its own namespace")
	}
}

func TestCleanupDeletesTheNamespaceOnlyWhenTheTestMadeIt(t *testing.T) {
	pod := Pod{Name: "p1", Node: "n", ContainerID: tContainerID}
	for _, c := range []struct {
		label      string
		wantDelete bool
	}{{"coordinator-chaos", true}, {"someone-else", false}} {
		r := &recorded{}
		kc := newKube(r)
		if err := kc.Signal(context.Background(), pod, sigStop); err != nil { // creates the pod
			t.Fatal(err)
		}
		r.out = map[string]string{nsGet: nsOut, jsonKey: c.label}
		if err := kc.Signaler.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := anyContains(joined(r), "delete namespace kubemoot-chaos"); got != c.wantDelete {
			t.Errorf("label %q: namespace deleted=%t, want %t", c.label, got, c.wantDelete)
		}
		if !anyContains(joined(r), "delete pod -n kubemoot-chaos -l app.kubernetes.io/managed-by=coordinator-chaos") {
			t.Errorf("label %q: signaling pods not deleted", c.label)
		}
	}
}

func TestCleanupDoesNothingWhenNoSignalingPodWasMade(t *testing.T) {
	r := &recorded{}
	if err := newKube(r).Signaler.Cleanup(context.Background()); err != nil || len(r.calls) != 0 {
		t.Fatalf("err %v calls %d", err, len(r.calls))
	}
}

func TestContinueAllReturnsTheFailures(t *testing.T) {
	failing := false
	k := Kubectl{Context: tContext, Exec: func(_ context.Context, _ []byte, args []string) ([]byte, error) {
		if failing && strings.Contains(strings.Join(args, " "), " exec ") {
			return nil, errors.New("exit 3")
		}
		return nil, nil
	}}
	s := &NodeSignaler{K: k, Namespace: tNS, Image: "img"}
	pod := Pod{Name: "p1", Node: "n", ContainerID: tContainerID}
	if err := s.Signal(context.Background(), pod, sigStop); err != nil {
		t.Fatal(err)
	}
	failing = true
	errs := s.ContinueAll(context.Background(), []Pod{pod})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "CONT to p1") {
		t.Fatalf("got %v", errs)
	}
}

func TestUnreadableCountersDoNotProveLossInControlMode(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = holdSynthesis
	h.noCounters = true
	r := h.runner()
	r.Cfg.MetricsWait = 50 * time.Millisecond
	c := newCase(ActionKill, PointSynthesisStarted, 1, ModeControl, Flags{})
	res := r.Run(context.Background(), []Case{c})[0]
	if res.Outcome == OutcomeExpectedLoss || res.Outcome == OutcomeFail {
		t.Fatalf("a counter read failure became %s: %+v", res.Outcome, res.Trials[0].Violations)
	}
}

func TestThreadsForOrdersByWhenTheHarnessSawThem(t *testing.T) {
	s := NewStore()
	t0 := time.Unix(1000, 0)
	// The first thread has the higher stream sequence but was seen first.
	s.AddMsg(Msg{Seq: 90, ThreadID: "first", MessageType: MsgThreadStart, ConversationID: "c", Seen: t0})
	s.AddEvent(Event{Seq: 5, EventID: "x", Type: EvThreadOpened, ThreadID: "second", Data: EventData{ConversationID: "c"}, Seen: t0.Add(time.Second)})
	if got := s.ThreadsFor("c"); len(got) != 2 || got[0] != "first" {
		t.Fatalf("got %v", got)
	}
}
