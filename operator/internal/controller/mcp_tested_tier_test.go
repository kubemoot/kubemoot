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

package controller

import "testing"

func TestParseSuccessRate(t *testing.T) {
	cases := map[string]float64{
		"85%":    0.85,
		"100%":   1,
		" 50% ":  0.5,
		"72":     0.72,
		"N/A":    0,
		"":       0,
		"lots%":  0,
		"12.5%%": 0,
	}
	for in, want := range cases {
		if got := parseSuccessRate(in); got != want {
			t.Errorf("parseSuccessRate(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTestedMinSuccessRate(t *testing.T) {
	cases := map[string]float64{
		"":      defaultTestedMinSuccessRate,
		"0.65":  0.65,
		"0":     0,
		"seven": defaultTestedMinSuccessRate,
	}
	for in, want := range cases {
		if got := testedMinSuccessRate(in); got != want {
			t.Errorf("testedMinSuccessRate(%q) = %v, want %v", in, got, want)
		}
	}
}
