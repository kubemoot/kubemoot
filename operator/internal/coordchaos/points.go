/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import "fmt"

// Point is a place in a thread's life where the harness acts. A point is a predicate on
// what the harness has read about the thread, never a delay: it holds while the thread
// sits at the point and stops holding when the coordinator takes the next step.
type Point string

// The kill points. Each is the frontier of the coordinator log, so the same predicate
// that says "reached" also says, after the coordinator is dead, "the kill landed here".
const (
	// PointThreadOpened: thread_opened is logged and the advisory step has not logged a subcommittee.
	PointThreadOpened Point = "thread_opened"
	// PointSubcommitteeConvened: the brief is logged, evaluation has not started.
	PointSubcommitteeConvened Point = "subcommittee_convened"
	// PointEvaluationStarted: the roster is logged and no phase has settled.
	PointEvaluationStarted Point = "evaluation_started"
	// PointReviewStarted: the review roster is logged and synthesis has not started.
	PointReviewStarted Point = "review_started"
	// PointSynthesisStarted: the synthesis call began and the answer is not logged.
	PointSynthesisStarted Point = "synthesis_started"
	// PointSynthesisDone: the answer is logged and not yet published.
	PointSynthesisDone Point = "synthesis_done"
	// PointThreadClose: thread_close is published and thread_closed is not yet logged.
	PointThreadClose Point = "thread_close"
)

// AllPoints lists the points in the order a thread passes them.
var AllPoints = []Point{
	PointThreadOpened, PointSubcommitteeConvened, PointEvaluationStarted,
	PointReviewStarted, PointSynthesisStarted, PointSynthesisDone, PointThreadClose,
}

// narrowPoints are the points whose window is the gap between a write and the
// next write, milliseconds rather than a model call. The harness reaches them by
// watching, so a kill can land after the next step; the runner then tries again.
var narrowPoints = map[Point]bool{
	PointSubcommitteeConvened: true,
	PointSynthesisDone:        true,
	PointThreadClose:          true,
}

// Narrow reports whether the point's window is a few milliseconds wide.
func (p Point) Narrow() bool { return narrowPoints[p] }

var logFrontiers = map[Point]func(Thread) bool{
	PointThreadOpened: func(t Thread) bool {
		return t.Has(EvThreadOpened) && !t.Has(EvSubcommitteeConvened) && !t.Closed()
	},
	PointSubcommitteeConvened: func(t Thread) bool {
		return t.Has(EvSubcommitteeConvened) && !t.Has(EvEvaluationStarted) &&
			!t.Has(EvSynthesisStarted) && !t.Closed()
	},
	PointEvaluationStarted: func(t Thread) bool {
		return t.Has(EvEvaluationStarted) && !t.Has(EvSettled) &&
			!t.Has(EvSynthesisStarted) && !t.Closed()
	},
	PointReviewStarted: func(t Thread) bool {
		return t.Has(EvReviewStarted) && !t.Has(EvSynthesisStarted) && !t.Closed()
	},
	PointSynthesisStarted: func(t Thread) bool {
		return t.Has(EvSynthesisStarted) && !t.Has(EvSynthesisDone) && !t.Closed()
	},
	PointSynthesisDone: func(t Thread) bool {
		return t.Has(EvSynthesisDone) && t.MsgCount(MsgSynthesis) == 0 && !t.Closed()
	},
	PointThreadClose: func(t Thread) bool {
		return t.MsgCount(MsgThreadClose) > 0 && !t.Closed()
	},
}

// msgFrontiers read the discussion stream only. They serve the control mode, where the
// coordinator log is off, and cover the points a message can show.
var msgFrontiers = map[Point]func(Thread) bool{
	PointThreadOpened: func(t Thread) bool {
		return t.MsgCount(MsgThreadStart) > 0 && t.MsgCount(MsgAdvisoryReady) == 0 &&
			t.MsgCount(MsgSynthesis) == 0
	},
	PointEvaluationStarted: func(t Thread) bool {
		return t.MsgCount(MsgAdvisoryReady) > 0 && t.terminalSignals() == 0 &&
			t.MsgCount(MsgSynthesis) == 0
	},
	PointSynthesisStarted: func(t Thread) bool {
		return t.terminalSignals() > 0 && t.MsgCount(MsgSynthesis) == 0
	},
}

// Holds reports whether the thread sits at the point. A thread with a coordinator log
// is judged by the log; one without (control mode) by its messages.
func (p Point) Holds(t Thread) bool {
	if len(t.Events) > 0 {
		f, ok := logFrontiers[p]
		return ok && f(t)
	}
	f, ok := msgFrontiers[p]
	return ok && f(t)
}

// ParsePoint returns the point named s.
func ParsePoint(s string) (Point, error) {
	for _, p := range AllPoints {
		if string(p) == s {
			return p, nil
		}
	}
	return "", fmt.Errorf("unknown kill point %q", s)
}
