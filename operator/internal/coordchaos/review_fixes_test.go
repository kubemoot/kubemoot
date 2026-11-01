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

func TestProtectedModeIgnoresMessagesUntilTheEventIsLogged(t *testing.T) {
	b := newBuilder("a")
	b.msg(MsgThreadStart)
	th := b.thread()
	if PointThreadOpened.HoldsIn(th, ModeProtected) {
		t.Fatal("thread_opened must not hold before the event is in the log")
	}
	if !PointThreadOpened.HoldsIn(th, ModeControl) {
		t.Fatal("control mode reads messages")
	}
	b.ev(EvThreadOpened, EventData{})
	if !PointThreadOpened.HoldsIn(b.thread(), ModeProtected) {
		t.Fatal("thread_opened should hold once logged")
	}
}

func TestSelectTrimsAndSkipsEmptyFilters(t *testing.T) {
	m := Matrix(ModeProtected)
	got, err := Select(m, []string{"baseline", " stop@evaluation_started ", ""})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	if all, _ := Select(m, []string{"", " "}); len(all) != len(m) {
		t.Fatal("only empty filters keep all")
	}
}

func TestSignalerRefusesANamespaceItDidNotCreate(t *testing.T) {
	r := &recorded{out: map[string]string{
		nsGet: nsOut,
	}}
	c := newKube(r)
	pod := Pod{Name: "p1", Node: "n", ContainerID: tContainerID}
	err := c.Signal(context.Background(), pod, sigStop)
	if err == nil || !strings.Contains(err.Error(), "not created by the chaos test") {
		t.Fatalf("got %v", err)
	}
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), " apply ") {
			t.Fatal("applied a manifest into a foreign namespace")
		}
	}
}

func TestUnreadableCountersFailAProtectedTrial(t *testing.T) {
	h := newHarness(t, 1)
	h.hold = holdSynthesis
	h.noCounters = true
	r := h.runner()
	r.Cfg.MetricsWait = 50 * time.Millisecond
	res := r.Run(context.Background(), []Case{kill(PointSynthesisStarted)})[0]
	if res.Outcome != OutcomeFail || !strings.Contains(names(res.Trials[0].Violations), "metrics_unreadable") {
		t.Fatalf("outcome %s violations %s", res.Outcome, names(res.Trials[0].Violations))
	}
}
