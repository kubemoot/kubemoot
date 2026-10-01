/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"testing"
	"time"
)

func TestParseMaxDuration(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Duration
	}{
		{"90 seconds", 90 * time.Second},
		{"2 minutes", 2 * time.Minute},
		{"120 seconds", 120 * time.Second},
		{"120s", 120 * time.Second},
		{"2m30s", 2*time.Minute + 30*time.Second},
		{"", defaultMaxDuration},
		{"garbage", defaultMaxDuration},
	}

	for _, c := range cases {
		got := parseMaxDuration(c.input)
		if got != c.expected {
			t.Errorf("parseMaxDuration(%q) = %v, want %v", c.input, got, c.expected)
		}
	}
}
