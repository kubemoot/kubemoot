/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package fitnessrealm decides whether two fitness runs ran on the same realm.
// It is pure: it reads the RealmSnapshot a suite records in status and returns
// digests, differences, and resource notes, so the operator, kmctl, and any other
// client that reads the status agree on what "comparable" means.
package fitnessrealm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Part names, as they appear in a difference.
const (
	PartServers   = "servers"
	PartModels    = "models"
	PartVersions  = "versions"
	PartResources = "resources"
	PartIsolation = "isolation"
)

// DigestsOf hashes the comparable parts of a snapshot. Time, residency, memory in
// use, and concurrent activity are left out: they change from run to run on an
// unchanged system, so they are recorded but never make runs incomparable.
func DigestsOf(s *kubemootv1alpha1.RealmSnapshot) *kubemootv1alpha1.RealmDigests {
	if s == nil {
		return nil
	}
	return &kubemootv1alpha1.RealmDigests{
		Servers:   hashLines(serverLines(s)),
		Models:    hashLines(modelLines(s)),
		Versions:  hashLines(versionLines(s)),
		Resources: hashLines(resourceLines(s)),
		Isolation: hashLines([]string{"trialIsolation=" + s.TrialIsolation}),
	}
}

// DifferingParts names the parts whose digests differ, in a fixed order; empty
// when the realms match. A missing realm on either side is reported as "unknown".
func DifferingParts(a, b *kubemootv1alpha1.RealmDigests) []string {
	if a == nil || b == nil {
		return []string{"unknown"}
	}
	var out []string
	for _, p := range []struct{ name, a, b string }{
		{PartServers, a.Servers, b.Servers},
		{PartModels, a.Models, b.Models},
		{PartVersions, a.Versions, b.Versions},
		{PartResources, a.Resources, b.Resources},
		{PartIsolation, a.Isolation, b.Isolation},
	} {
		if p.a != p.b {
			out = append(out, p.name)
		}
	}
	return out
}

// Differences lists, one line each, what differs between two snapshots: the
// comparable parts, plus residency and concurrent activity, which are noted
// without making the realms incomparable. The list is cut at limit.
func Differences(a, b *kubemootv1alpha1.RealmSnapshot, limit int) []string {
	return differences(a, b, limit, true)
}

// Drift lists what changed between the start and the end of one run. Models
// loading during the run is expected, so residency is not drift.
func Drift(start, end *kubemootv1alpha1.RealmSnapshot, limit int) []string {
	return differences(start, end, limit, false)
}

func differences(a, b *kubemootv1alpha1.RealmSnapshot, limit int, withResidency bool) []string {
	if a == nil || b == nil {
		return nil
	}
	var out []string
	out = append(out, diffLines("server", serverLines(a), serverLines(b))...)
	out = append(out, diffLines("model", modelLines(a), modelLines(b))...)
	out = append(out, diffLines("version", versionLines(a), versionLines(b))...)
	out = append(out, diffLines("resource", resourceLines(a), resourceLines(b))...)
	if a.TrialIsolation != b.TrialIsolation {
		out = append(out, fmt.Sprintf("trial isolation: %s -> %s", a.TrialIsolation, b.TrialIsolation))
	}
	if withResidency {
		out = append(out, diffLines("residency", residencyLines(a), residencyLines(b))...)
	}
	if x, y := strings.Join(a.ConcurrentSuites, ","), strings.Join(b.ConcurrentSuites, ","); x != y {
		out = append(out, fmt.Sprintf("concurrent suites: [%s] -> [%s]", x, y))
	}
	if a.ConcurrentRuns != b.ConcurrentRuns {
		out = append(out, fmt.Sprintf("concurrent standalone runs: %d -> %d", a.ConcurrentRuns, b.ConcurrentRuns))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// diffLines reports the lines only in a (gone) and only in b (new), under a label.
func diffLines(label string, a, b []string) []string {
	inA, inB := toSet(a), toSet(b)
	var out []string
	for _, l := range a {
		if !inB[l] {
			out = append(out, fmt.Sprintf("%s changed or removed: %s", label, l))
		}
	}
	for _, l := range b {
		if !inA[l] {
			out = append(out, fmt.Sprintf("%s new or changed: %s", label, l))
		}
	}
	return out
}

func toSet(lines []string) map[string]bool {
	m := make(map[string]bool, len(lines))
	for _, l := range lines {
		m[l] = true
	}
	return m
}

func hashLines(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])[:kubemootv1alpha1.RealmDigestChars]
}

func serverLines(s *kubemootv1alpha1.RealmSnapshot) []string {
	out := make([]string, 0, len(s.ModelServers))
	for _, m := range s.ModelServers {
		line := fmt.Sprintf("%s/%s engine=%s version=%s", m.Namespace, m.Name, m.Engine, m.EngineVersion)
		if a := m.Accelerator; a != nil {
			line += fmt.Sprintf(" memoryMiB=%d maxParallel=%d context=%d", a.MemoryMiB, a.MaxParallel, a.ContextLength)
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

func modelLines(s *kubemootv1alpha1.RealmSnapshot) []string {
	var out []string
	for _, m := range s.ModelServers {
		for _, mod := range m.Models {
			out = append(out, fmt.Sprintf("%s/%s %s digest=%s quant=%s context=%d",
				m.Namespace, m.Name, mod.Name, mod.Digest, mod.Quantization, mod.ContextLength))
		}
	}
	sort.Strings(out)
	return out
}

func residencyLines(s *kubemootv1alpha1.RealmSnapshot) []string {
	var out []string
	for _, m := range s.ModelServers {
		for _, mod := range m.Models {
			if mod.Resident {
				out = append(out, fmt.Sprintf("%s/%s %s resident", m.Namespace, m.Name, mod.Name))
			}
		}
	}
	sort.Strings(out)
	return out
}

func versionLines(s *kubemootv1alpha1.RealmSnapshot) []string {
	out := make([]string, 0, 2+len(s.Agents))
	out = append(out, "operator="+s.OperatorVersion, "crew="+s.CrewVersion)
	for _, a := range s.Agents {
		out = append(out, fmt.Sprintf("image %s=%s", a.Container, a.Image))
	}
	sort.Strings(out)
	return out
}

func resourceLines(s *kubemootv1alpha1.RealmSnapshot) []string {
	out := make([]string, 0, len(s.Agents))
	for _, a := range s.Agents {
		out = append(out, fmt.Sprintf("%s cpu=%s/%s memory=%s/%s",
			a.Container, a.CPURequest, a.CPULimit, a.MemoryRequest, a.MemoryLimit))
	}
	sort.Strings(out)
	return out
}

// ResourceNotes flags container resources that make a throttled agent likely. The
// guidance is a guaranteed allocation (a request) plus a separate, higher hard
// limit: an unset request has no guaranteed allocation, and a limit equal to the
// request leaves no headroom, so a burst is throttled or killed at the allocation.
func ResourceNotes(agents []kubemootv1alpha1.RealmAgentContainer) []string {
	out := make([]string, 0, 2*len(agents))
	for _, a := range agents {
		out = append(out, resourceNote(a.Container, "cpu", a.CPURequest, a.CPULimit)...)
		out = append(out, resourceNote(a.Container, "memory", a.MemoryRequest, a.MemoryLimit)...)
	}
	sort.Strings(out)
	return out
}

func resourceNote(container, resource, request, limit string) []string {
	switch {
	case request == "":
		return []string{fmt.Sprintf("%s: %s request unset, so no guaranteed allocation", container, resource)}
	case limit == request:
		return []string{fmt.Sprintf("%s: %s limit equals the request %s, so no headroom above the guaranteed allocation",
			container, resource, request)}
	}
	return nil
}
