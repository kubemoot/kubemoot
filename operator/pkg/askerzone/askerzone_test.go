/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package askerzone

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"region zone", "America/New_York", "America/New_York", true},
		{"utc", "UTC", "UTC", true},
		{"offset zone", "Etc/GMT+5", "Etc/GMT+5", true},
		{"trimmed", "  Europe/Paris ", "Europe/Paris", true},
		{"empty", "", "", false},
		{"blank", "   ", "", false},
		{"local is not a zone", "Local", "", false},
		{"unknown", "Mars/Base", "", false},
		{"spaces inside", "Mars Base", "", false},
		{"traversal", "../etc/passwd", "", false},
		{"absolute path", "/etc/localtime", "", false},
		{"too long", "America/" + strings.Repeat("x", 80), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Valid(c.in)
			if got != c.want || ok != c.ok {
				t.Fatalf("Valid(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}
