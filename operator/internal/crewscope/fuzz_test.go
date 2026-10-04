/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package crewscope

import (
	"strings"
	"testing"
)

// FuzzParseDiscussSubject feeds arbitrary NATS subjects (the dispatcher parses
// every subject it receives). An accepted subject names a valid scope and renders
// back to exactly itself.
func FuzzParseDiscussSubject(f *testing.F) {
	for _, s := range []string{
		"kubemoot.discuss.team-a.pilot.general.abc-123",
		"kubemoot.discuss.team-a.pilot.general.thread.with.dots",
		"kubemoot.discuss.team-a.pilot.general.",
		"kubemoot.discuss..pilot.general.t",
		"kubemoot.discuss.team a.pilot.general.t",
		"kubemoot.request.team-a.pilot",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, subject string) {
		d, err := ParseDiscussSubject(subject)
		if err != nil {
			return
		}
		if _, err := New(d.Namespace, d.Crew); err != nil {
			t.Fatalf("accepted %q with an invalid scope: %v", subject, err)
		}
		if got := d.DiscussSubject(d.Channel, d.Thread); got != subject {
			t.Fatalf("ParseDiscussSubject(%q) renders back as %q", subject, got)
		}
	})
}

// FuzzDiscussSubjectRoundTrip builds a subject from parts and parses it back:
// any valid scope with a dot-free channel and a non-empty thread survives intact.
func FuzzDiscussSubjectRoundTrip(f *testing.F) {
	f.Add("team-a", "pilot", "general", "abc-123")
	f.Add("kubemoot", "homelab-pilot", "general", "thread.with.dots")
	f.Fuzz(func(t *testing.T, ns, crew, channel, thread string) {
		s, err := New(ns, crew)
		if err != nil || channel == "" || strings.Contains(channel, ".") || thread == "" {
			return
		}
		d, err := ParseDiscussSubject(s.DiscussSubject(channel, thread))
		if err != nil {
			t.Fatalf("subject for %+v %q %q does not parse: %v", s, channel, thread, err)
		}
		if d.Scope != s || d.Channel != channel || d.Thread != thread {
			t.Fatalf("round trip = %+v, want %+v %q %q", d, s, channel, thread)
		}
	})
}

// FuzzParseMemoryKey feeds arbitrary crew-memory KV keys. An accepted key
// renders back to itself, and a key built from a valid scope parses to it.
func FuzzParseMemoryKey(f *testing.F) {
	f.Add("team-a.pilot.infra.disk.size")
	f.Add("pilot.infra")
	f.Add("..x")
	f.Fuzz(func(t *testing.T, key string) {
		k, err := ParseMemoryKey(key)
		if err != nil {
			return
		}
		if k.Key() != key {
			t.Fatalf("ParseMemoryKey(%q).Key() = %q", key, k.Key())
		}
		again, err := ParseMemoryKey(k.MemoryPrefix() + k.Rest)
		if err != nil || again != k {
			t.Fatalf("re-parse of %q = (%+v, %v), want %+v", key, again, err, k)
		}
	})
}
