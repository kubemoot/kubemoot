package a2a

import "testing"

const day = "2026-11-20"

func TestContextLine(t *testing.T) {
	cases := map[string]struct{ date, zone, ns, crew, want string }{
		"all": {day, "America/New_York", "crew-homelab-pilot", "homelab-pilot",
			"Today is " + day + " (America/New_York). You are in namespace crew-homelab-pilot, crew homelab-pilot."},
		"no zone":      {day, "", "ns", "c", "Today is " + day + ". You are in namespace ns, crew c."},
		"no date":      {"", "", "ns", "c", "You are in namespace ns, crew c."},
		"namespace":    {"", "", "ns", "", "You are in namespace ns."},
		"crew":         {"", "", "", "c", "You are in crew c."},
		"only the day": {day, "UTC", "", "", "Today is " + day + " (UTC)."},
		"nothing":      {"", "", "", "", ""},
	}
	for name, c := range cases {
		if got := ContextLine(c.date, c.zone, c.ns, c.crew); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}
