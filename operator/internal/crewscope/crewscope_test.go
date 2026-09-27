/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crewscope

import "testing"

const (
	nsA       = "team-a"
	nsB       = "team-b"
	crewPilot = "pilot"
)

func mustScope(t *testing.T, ns, crew string) Scope {
	t.Helper()
	s, err := New(ns, crew)
	if err != nil {
		t.Fatalf("New(%q, %q): %v", ns, crew, err)
	}
	return s
}

func TestFormats(t *testing.T) {
	s := mustScope(t, nsA, "homelab-pilot")
	cases := []struct{ name, got, want string }{
		{"discuss", s.DiscussSubject("general", "t-1"), "kubemoot.discuss.team-a.homelab-pilot.general.t-1"},
		{"resume key", s.ResumeKey(), "team-a.homelab-pilot"},
		{"resume collection", s.ResumeCollection(), "crew_team_a_homelab_pilot_resumes"},
		{"memory prefix", s.MemoryPrefix(), "team-a.homelab-pilot."},
		{"cluster rbac", s.ClusterRBACName(), "crew-team-a-homelab-pilot-discussion"},
		{"route", s.RoutePathPrefix(), "/api/v1/namespaces/team-a/discussions/homelab-pilot"},
		{"gateway path", s.GatewayAPIPath(), "/api/v1/discussions/homelab-pilot"},
		{"legacy resume key", LegacyResumeKey("homelab-pilot"), "homelab-pilot"},
		{"legacy rbac", LegacyClusterRBACName("homelab-pilot"), "crew-homelab-pilot-discussion"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestSameCrewNameInTwoNamespacesDiffers(t *testing.T) {
	a := mustScope(t, nsA, crewPilot)
	b := mustScope(t, nsB, crewPilot)
	pairs := []struct{ name, a, b string }{
		{"discuss", a.DiscussSubject("general", "t"), b.DiscussSubject("general", "t")},
		{"resume key", a.ResumeKey(), b.ResumeKey()},
		{"resume collection", a.ResumeCollection(), b.ResumeCollection()},
		{"memory prefix", a.MemoryPrefix(), b.MemoryPrefix()},
		{"cluster rbac", a.ClusterRBACName(), b.ClusterRBACName()},
		{"route", a.RoutePathPrefix(), b.RoutePathPrefix()},
	}
	for _, p := range pairs {
		if p.a == p.b {
			t.Errorf("%s collides across namespaces: %q", p.name, p.a)
		}
	}
}

func TestNewRejectsMissingOrInvalidTokens(t *testing.T) {
	for _, c := range []struct{ ns, crew string }{
		{"", crewPilot},
		{nsA, ""},
		{"team.a", crewPilot},
		{nsA, "pi.lot"},
		{nsA, "pilot>"},
		{nsA, "*"},
		{"team a", crewPilot},
	} {
		if _, err := New(c.ns, c.crew); err == nil {
			t.Errorf("New(%q, %q) accepted an invalid token", c.ns, c.crew)
		}
	}
}

func TestParseDiscussSubjectRoundTrip(t *testing.T) {
	s := mustScope(t, nsA, crewPilot)
	for _, thread := range []string{"abc-123", "thread.with.dots"} {
		got, err := ParseDiscussSubject(s.DiscussSubject("general", thread))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Scope != s || got.Channel != "general" || got.Thread != thread {
			t.Errorf("round trip = %+v, want %+v general %s", got, s, thread)
		}
	}
}

func TestParseDiscussSubjectRejectsMalformed(t *testing.T) {
	for _, subject := range []string{
		"",
		"kubemoot.request.team-a.pilot",
		"kubemoot.discuss.pilot.general.t",
		"kubemoot.discuss.team-a.pilot.general",
		"kubemoot.discuss..pilot.general.t",
		"kubemoot.discuss.team-a..general.t",
		"kubemoot.discuss.team-a.pilot..t",
		"kubemoot.discuss.team-a.pilot.general.",
	} {
		if _, err := ParseDiscussSubject(subject); err == nil {
			t.Errorf("ParseDiscussSubject(%q) accepted a malformed subject", subject)
		}
	}
}

func TestMemoryKeyRoundTrip(t *testing.T) {
	k, err := ParseMemoryKey("team-a.pilot.infra.disk.size")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if k.Namespace != nsA || k.Crew != crewPilot || k.Rest != "infra.disk.size" {
		t.Errorf("parsed %+v", k)
	}
	if k.Key() != "team-a.pilot.infra.disk.size" {
		t.Errorf("Key() = %q", k.Key())
	}
	for _, bad := range []string{"", nsA, "team-a.pilot", "team-a.pilot.", ".pilot.topic"} {
		if _, err := ParseMemoryKey(bad); err == nil {
			t.Errorf("ParseMemoryKey(%q) accepted a malformed key", bad)
		}
	}
}

func TestSplitLegacyMemoryKey(t *testing.T) {
	crew, rest, ok := SplitLegacyMemoryKey("pilot.infra.disk")
	if !ok || crew != crewPilot || rest != "infra.disk" {
		t.Errorf("got %q %q %v", crew, rest, ok)
	}
	for _, bad := range []string{"", crewPilot, ".infra", "pilot."} {
		if _, _, ok := SplitLegacyMemoryKey(bad); ok {
			t.Errorf("SplitLegacyMemoryKey(%q) accepted a malformed key", bad)
		}
	}
}
