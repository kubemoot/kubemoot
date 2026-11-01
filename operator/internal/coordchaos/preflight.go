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
	"regexp"
	"strconv"
	"strings"
)

// FlagsFromEnv reads the protections a coordinator container runs with from its
// environment. The container's environment is what the process reads, so it is the
// effective setting: an Agent that sets a variable itself wins over the chart value.
func FlagsFromEnv(env map[string]string) Flags {
	return Flags{
		EventLog: env[envEventLog] == flagTrue,
		Resume:   env[envResume] == flagTrue,
		Lease:    env[envLease] == flagTrue,
	}
}

// EffectiveFlags returns the flags every coordinator pod runs with, or an error when
// the pods disagree (a rollout in progress) or there are none.
func EffectiveFlags(pods []Pod) (Flags, error) {
	if len(pods) == 0 {
		return Flags{}, fmt.Errorf("no coordinator pod found")
	}
	first := FlagsFromEnv(pods[0].Env)
	for _, p := range pods[1:] {
		if got := FlagsFromEnv(p.Env); got != first {
			return Flags{}, fmt.Errorf("coordinator pods disagree: %s has %+v, %s has %+v", pods[0].Name, first, p.Name, got)
		}
	}
	return first, nil
}

// Union returns the flags any of the cases need.
func Union(cases []Case) Flags {
	var u Flags
	for _, c := range cases {
		u.EventLog = u.EventLog || c.Need.EventLog
		u.Resume = u.Resume || c.Need.Resume
		u.Lease = u.Lease || c.Need.Lease
	}
	return u
}

// CheckFlags refuses a run the coordinator cannot support. A protected run needs every
// flag its cases need; a control run needs all of them off, so that its loss is the loss
// of an unprotected coordinator. configured is the KubemootConfig value, compared with the
// effective one only to say when an Agent overrides the chart.
func CheckFlags(mode Mode, need, effective Flags, configured *Flags) (notes []string, err error) {
	if mode == ModeControl {
		if effective != (Flags{}) {
			return nil, fmt.Errorf("control mode needs the coordinator flags off, but they are %s; "+
				"turn them off (chart values coordinator.eventLog, resume, lease) and let the coordinator roll", flagList(effective))
		}
		return flagNotes(effective, configured), nil
	}
	var missing []string
	if need.EventLog && !effective.EventLog {
		missing = append(missing, envEventLog)
	}
	if need.Resume && !effective.Resume {
		missing = append(missing, envResume)
	}
	if need.Lease && !effective.Lease {
		missing = append(missing, envLease)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("refusing to run against an unprotected coordinator: %s not true on the coordinator pod; "+
			"a pass would prove nothing (use --mode control to document the loss without the flags)", strings.Join(missing, ", "))
	}
	return flagNotes(effective, configured), nil
}

func flagNotes(effective Flags, configured *Flags) []string {
	if configured == nil || *configured == effective {
		return nil
	}
	return []string{fmt.Sprintf("KubemootConfig says %s but the coordinator pod runs with %s; "+
		"the Agent's own env wins, and the pod is what was tested", flagList(*configured), flagList(effective))}
}

// Counters are the coordinator counters of one pod, by short name: checks.match,
// checks.mismatch, checks.unlogged, fenced, append_failures, threads_unlogged and
// resumes.<action>.
type Counters map[string]float64

var labelPattern = regexp.MustCompile(`(\w+)="([^"]*)"`)

// ParseCounters reads the coordinator counters out of Prometheus text.
func ParseCounters(text string) Counters {
	out := Counters{}
	for _, line := range strings.Split(text, "\n") {
		name, labels, value, ok := splitSample(line)
		if !ok {
			continue
		}
		if key := counterKey(name, labels); key != "" {
			out[key] += value
		}
	}
	return out
}

func splitSample(line string) (name string, labels map[string]string, value float64, ok bool) {
	if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "kubemoot_coordinator_") {
		return "", nil, 0, false
	}
	end := strings.LastIndex(line, " ")
	if end < 0 {
		return "", nil, 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(line[end+1:]), 64)
	if err != nil {
		return "", nil, 0, false
	}
	head := line[:end]
	name = head
	labels = map[string]string{}
	if i := strings.Index(head, "{"); i >= 0 {
		name = head[:i]
		for _, m := range labelPattern.FindAllStringSubmatch(head[i:], -1) {
			labels[m[1]] = m[2]
		}
	}
	return name, labels, v, true
}

func counterKey(name string, labels map[string]string) string {
	name = strings.TrimSuffix(name, "_total")
	switch name + "_total" {
	case metricChecks:
		return "checks." + labels["result"]
	case metricResumes:
		return "resumes." + labels["action"]
	case metricFenced:
		return "fenced"
	case metricAppendFailures:
		return "append_failures"
	case metricThreadsUnlogged:
		return "threads_unlogged"
	}
	return ""
}

// Delta sums, over the pods present after, how much each counter grew since before.
// A pod absent from before is new and counts from zero. Counters of a pod that is gone
// went with it and are not counted.
func Delta(before, after map[string]Counters) map[string]float64 {
	out := map[string]float64{}
	for uid, now := range after {
		was := before[uid]
		for k, v := range now {
			out[k] += v - was[k]
		}
	}
	return out
}
