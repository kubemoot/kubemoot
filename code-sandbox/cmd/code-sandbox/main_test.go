package main

import "testing"

func TestEnvInt(t *testing.T) {
	cases := map[string]int{"": 30, "45": 45, "0": 30, "-5": 30, "abc": 30}
	for value, want := range cases {
		t.Setenv("CODE_SANDBOX_TEST_INT", value)
		if got := envInt("CODE_SANDBOX_TEST_INT", 30); got != want {
			t.Errorf("envInt(%q) = %d, want %d", value, got, want)
		}
	}
}
