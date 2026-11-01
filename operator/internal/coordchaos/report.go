/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Outcome is the verdict on a case.
type Outcome string

const (
	// OutcomePass: the fault landed on the point and no check failed.
	OutcomePass Outcome = "pass"
	// OutcomeFail: a check failed.
	OutcomeFail Outcome = "fail"
	// OutcomeNotReached: no attempt landed the fault on the point, so nothing was proven.
	OutcomeNotReached Outcome = "not_reached"
	// OutcomeError: the harness or the cluster failed (a pod in CrashLoopBackOff, the bus
	// unreachable); the case says nothing about the coordinator.
	OutcomeError Outcome = "error"
	// OutcomeExpectedLoss: control mode, the fault landed and the thread lost what the
	// protections would have kept. This is the documented result, not a pass.
	OutcomeExpectedLoss Outcome = "expected_loss"
	// OutcomeControlNoLoss: control mode, the fault landed and nothing was lost. Reported
	// so that a control that proves nothing is visible.
	OutcomeControlNoLoss Outcome = "control_no_loss"
)

// Trial is one attempt of a case.
type Trial struct {
	Number     int         `json:"trial"`
	Landed     bool        `json:"landed"`
	Threads    []string    `json:"threads,omitempty"`
	LogTypes   []string    `json:"log,omitempty"`
	Violations []Violation `json:"violations,omitempty"`
	Error      string      `json:"error,omitempty"`
	// AnswerSeconds is the time from the fault to the answer reaching the discussion stream.
	AnswerSeconds float64 `json:"answerSeconds,omitempty"`
	// ClosedSeconds is the time from the fault to thread_closed in the log.
	ClosedSeconds float64            `json:"closedSeconds,omitempty"`
	Metrics       map[string]float64 `json:"metrics,omitempty"`
	Notes         []string           `json:"notes,omitempty"`
}

// CaseResult is a case and what its trials showed.
type CaseResult struct {
	Case     Case    `json:"case"`
	Outcome  Outcome `json:"outcome"`
	Attempts int     `json:"attempts"`
	Trials   []Trial `json:"trials"`
}

// Classify turns the trials of a case into its outcome.
func Classify(c Case, trials []Trial) Outcome {
	landed, violated, errored := false, false, false
	for _, t := range trials {
		landed = landed || t.Landed
		violated = violated || len(t.Violations) > 0
		errored = errored || t.Error != ""
	}
	if c.Mode == ModeControl {
		return classifyControl(landed, violated, errored)
	}
	switch {
	case violated:
		return OutcomeFail
	case errored:
		return OutcomeError
	case landed:
		return OutcomePass
	}
	return OutcomeNotReached
}

func classifyControl(landed, violated, errored bool) Outcome {
	switch {
	case landed && violated:
		return OutcomeExpectedLoss
	case landed:
		return OutcomeControlNoLoss
	case errored:
		return OutcomeError
	}
	return OutcomeNotReached
}

// Report is the result of one run.
type Report struct {
	Started  time.Time    `json:"started"`
	Finished time.Time    `json:"finished"`
	Context  string       `json:"context"`
	Crew     string       `json:"crew"`
	Mode     Mode         `json:"mode"`
	Flags    Flags        `json:"effectiveFlags"`
	Cases    []CaseResult `json:"cases"`
}

// Exit codes of the driver.
const (
	ExitOK         = 0
	ExitFail       = 1
	ExitRefused    = 2
	ExitNotReached = 3
)

// ExitCode returns the driver's exit status: 1 when a case failed or errored, 3 when
// every case passed but one never landed, else 0. Control outcomes never fail a run.
func (r Report) ExitCode() int {
	code := ExitOK
	for _, c := range r.Cases {
		switch c.Outcome {
		case OutcomeFail, OutcomeError:
			return ExitFail
		case OutcomeNotReached:
			code = ExitNotReached
		}
	}
	return code
}

// WriteJSON writes the report as indented JSON.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Markdown renders the short table, then the evidence for each violation.
func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Coordinator chaos run\n\nCrew %s, mode %s, context %s.\n", r.Crew, r.Mode, r.Context)
	fmt.Fprintf(&b, "Effective flags: eventLog=%t resume=%t lease=%t.\n\n", r.Flags.EventLog, r.Flags.Resume, r.Flags.Lease)
	b.WriteString("| Case | Replicas | Flags | Outcome | Trials | Answer s | Closed s | Violations |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, c := range r.Cases {
		lt := landedTrial(c.Trials)
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %d | %s | %s | %d |\n",
			c.Case.Name, c.Case.Replicas, flagList(c.Case.Need), c.Outcome, c.Attempts,
			seconds(lt.AnswerSeconds), seconds(lt.ClosedSeconds), countViolations(c.Trials))
	}
	b.WriteString(r.evidence())
	return b.String()
}

func (r Report) evidence() string {
	var b strings.Builder
	for _, c := range r.Cases {
		for _, t := range c.Trials {
			if t.Error != "" {
				fmt.Fprintf(&b, "\n- %s trial %d: error: %s\n", c.Case.Name, t.Number, t.Error)
			}
			for _, v := range t.Violations {
				fmt.Fprintf(&b, "\n- %s trial %d: %s: %s\n", c.Case.Name, t.Number, v.Check, v.Detail)
				if len(v.Evidence) > 0 {
					fmt.Fprintf(&b, "  evidence: %s\n", strings.Join(v.Evidence, ", "))
				}
			}
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n## Evidence\n" + b.String()
}

func landedTrial(ts []Trial) Trial {
	for _, t := range ts {
		if t.Landed {
			return t
		}
	}
	if len(ts) > 0 {
		return ts[len(ts)-1]
	}
	return Trial{}
}

func countViolations(ts []Trial) int {
	n := 0
	for _, t := range ts {
		n += len(t.Violations)
	}
	return n
}

func seconds(s float64) string {
	if s <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", s)
}

func flagList(f Flags) string {
	var on []string
	if f.EventLog {
		on = append(on, "log")
	}
	if f.Resume {
		on = append(on, "resume")
	}
	if f.Lease {
		on = append(on, "lease")
	}
	if len(on) == 0 {
		return "off"
	}
	return strings.Join(on, "+")
}
