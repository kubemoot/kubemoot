/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var (
	protectedCase = Case{Name: "kill@x", Mode: ModeProtected}
	controlCase   = Case{Name: "kill@x (control)", Mode: ModeControl}
	bad           = []Violation{{Check: vAnswer, Detail: "0 synthesis messages"}}
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		c      Case
		trials []Trial
		want   Outcome
	}{
		{"landed and clean", protectedCase, []Trial{{Landed: true}}, OutcomePass},
		{"landed with a violation", protectedCase, []Trial{{Landed: true, Violations: bad}}, OutcomeFail},
		{"a missed trial with a violation still fails", protectedCase, []Trial{{Violations: bad}, {Landed: true}}, OutcomeFail},
		{"never landed", protectedCase, []Trial{{}, {}}, OutcomeNotReached},
		{"error without violation", protectedCase, []Trial{{Error: tBoom}}, OutcomeError},
		{"control loss", controlCase, []Trial{{Landed: true, Violations: bad}}, OutcomeExpectedLoss},
		{"control without loss", controlCase, []Trial{{Landed: true}}, OutcomeControlNoLoss},
		{"control never landed", controlCase, []Trial{{}}, OutcomeNotReached},
		{"control error", controlCase, []Trial{{Error: tBoom}}, OutcomeError},
	}
	for _, c := range cases {
		if got := Classify(c.c, c.trials); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestExitCode(t *testing.T) {
	mk := func(os ...Outcome) Report {
		var r Report
		for _, o := range os {
			r.Cases = append(r.Cases, CaseResult{Outcome: o})
		}
		return r
	}
	cases := []struct {
		r    Report
		want int
	}{
		{mk(OutcomePass, OutcomePass), ExitOK},
		{mk(OutcomePass, OutcomeExpectedLoss, OutcomeControlNoLoss), ExitOK},
		{mk(OutcomePass, OutcomeNotReached), ExitNotReached},
		{mk(OutcomeNotReached, OutcomeFail), ExitFail},
		{mk(OutcomeError), ExitFail},
	}
	for i, c := range cases {
		if got := c.r.ExitCode(); got != c.want {
			t.Errorf("case %d: got %d, want %d", i, got, c.want)
		}
	}
}

func TestReportRendering(t *testing.T) {
	r := Report{
		Crew: tHello, Mode: ModeProtected, Context: "kind-chaos", Flags: Flags{EventLog: true, Resume: true},
		Cases: []CaseResult{{
			Case:     Case{Name: "kill@synthesis_started", Replicas: 1, Need: Flags{EventLog: true, Resume: true}},
			Outcome:  OutcomeFail,
			Attempts: 1,
			Trials: []Trial{{
				Number: 1, Landed: true, AnswerSeconds: 41.25,
				Violations: []Violation{{Check: vAnswer, Detail: "2 synthesis messages", Evidence: []string{"m1", "m2"}}},
			}},
		}},
	}
	md := r.Markdown()
	for _, want := range []string{
		"| kill@synthesis_started | 1 | log+resume | fail | 1 | 41.2 | - | 1 |",
		"one_answer_on_bus: 2 synthesis messages", "evidence: m1, m2", "mode protected",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || back.Cases[0].Trials[0].Violations[0].Check != vAnswer {
		t.Fatalf("json round trip: %v %+v", err, back)
	}
}

func TestMatrixControl(t *testing.T) {
	for _, c := range Matrix(ModeControl) {
		if c.Need != (Flags{}) || c.Action != ActionKill || c.Mode != ModeControl {
			t.Errorf("control case %+v", c)
		}
	}
}

func checkProtectedCase(t *testing.T, c Case) {
	t.Helper()
	if !c.Need.EventLog || !c.Need.Resume {
		t.Errorf("%s does not need the log and resume", c.Name)
	}
	if (c.Action == ActionStop || c.Action == ActionKillHolder) && (!c.Need.Lease || c.Replicas != 2) {
		t.Errorf("%s needs leases and two replicas", c.Name)
	}
}

func TestMatrix(t *testing.T) {
	p := Matrix(ModeProtected)
	if len(p) != 1+len(AllPoints)+2+3 {
		t.Fatalf("protected matrix has %d cases", len(p))
	}
	for _, c := range p {
		checkProtectedCase(t, c)
	}
	if Union(p) != (Flags{EventLog: true, Resume: true, Lease: true}) {
		t.Errorf("union %+v", Union(p))
	}
	if Matrix(ModeProtected)[0].Expect().ResumedMax != 0 || p[1].Expect().ResumedMin != 1 {
		t.Error("expected resumes: baseline none, kill one")
	}
}

func TestSelect(t *testing.T) {
	m := Matrix(ModeProtected)
	got, err := Select(m, []string{"stop@", "baseline"})
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	if _, err := Select(m, []string{"no such case"}); err == nil {
		t.Fatal("an unmatched filter must be an error")
	}
	if all, _ := Select(m, nil); len(all) != len(m) {
		t.Fatal("no filter keeps all")
	}
}
