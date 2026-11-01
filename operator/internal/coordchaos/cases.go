/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"fmt"
	"strings"
)

// Action is the fault injected at a point.
type Action string

const (
	// ActionNone injects nothing: the baseline that proves the checks pass on a healthy run.
	ActionNone Action = "none"
	// ActionKill force-deletes the coordinator pod (grace period 0), the crash case.
	ActionKill Action = "kill"
	// ActionStop freezes the lease holder with SIGSTOP until another replica has taken
	// the thread over, then resumes it with SIGCONT: the zombie case. Needs two replicas.
	ActionStop Action = "stop"
	// ActionKillHolder force-deletes the replica that holds the thread's lease while
	// another replica runs. Needs two replicas.
	ActionKillHolder Action = "kill_holder"
)

// Flags are the coordinator protections a case needs.
type Flags struct {
	EventLog bool `json:"eventLog"`
	Resume   bool `json:"resume"`
	Lease    bool `json:"lease"`
}

// Case is one row of the matrix.
type Case struct {
	Name     string `json:"name"`
	Point    Point  `json:"point,omitempty"`
	Action   Action `json:"action"`
	Replicas int    `json:"replicas"`
	Mode     Mode   `json:"mode"`
	// Need are the flags the coordinator must have for this case to mean anything.
	Need Flags `json:"flags"`
	// Attempts bounds the trials the runner makes to land the fault on the point.
	Attempts int `json:"attempts"`
}

// Expect returns what a trial of the case should show in the log.
func (c Case) Expect() Expect {
	e := Expect{Mode: c.Mode}
	if c.Action != ActionNone {
		e.ResumedMin, e.ResumedMax = 1, 1
	}
	return e
}

func protectedFlags(lease bool) Flags { return Flags{EventLog: true, Resume: true, Lease: lease} }

func caseName(a Action, p Point, m Mode) string {
	n := string(a)
	if p != "" {
		n += "@" + string(p)
	}
	if m == ModeControl {
		n += " (control)"
	}
	return n
}

func newCase(a Action, p Point, replicas int, m Mode, need Flags) Case {
	attempts := 2
	if p.Narrow() {
		attempts = 5
	}
	return Case{Name: caseName(a, p, m), Point: p, Action: a, Replicas: replicas, Mode: m, Need: need, Attempts: attempts}
}

// Matrix returns the cases of the mode. The protected matrix kills a single coordinator
// at every point, then freezes and kills the lease holder of a pair. The control matrix
// kills a coordinator that has none of the protections, at the points a message shows.
func Matrix(m Mode) []Case {
	if m == ModeControl {
		cs := make([]Case, 0, 3)
		for _, p := range []Point{PointThreadOpened, PointEvaluationStarted, PointSynthesisStarted} {
			cs = append(cs, newCase(ActionKill, p, 1, ModeControl, Flags{}))
		}
		return cs
	}
	cs := []Case{{Name: "baseline", Action: ActionNone, Replicas: 1, Mode: m, Need: protectedFlags(false), Attempts: 1}}
	for _, p := range AllPoints {
		cs = append(cs, newCase(ActionKill, p, 1, m, protectedFlags(false)))
	}
	for _, p := range []Point{PointEvaluationStarted, PointSynthesisStarted} {
		cs = append(cs, newCase(ActionStop, p, 2, m, protectedFlags(true)))
	}
	for _, p := range []Point{PointEvaluationStarted, PointSynthesisStarted, PointSynthesisDone} {
		cs = append(cs, newCase(ActionKillHolder, p, 2, m, protectedFlags(true)))
	}
	return cs
}

// Select keeps the cases whose name contains one of the filters (all when empty).
func Select(cases []Case, filters []string) ([]Case, error) {
	if len(filters) == 0 {
		return cases, nil
	}
	var out []Case
	for _, c := range cases {
		for _, f := range filters {
			if strings.Contains(c.Name, f) {
				out = append(out, c)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no case matches %v", filters)
	}
	return out, nil
}
