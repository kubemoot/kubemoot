package main

import "testing"

func TestEnvHelpers(t *testing.T) {
	t.Setenv("LIAISON_TEST_STR", "x")
	t.Setenv("LIAISON_TEST_INT", "7")
	t.Setenv("LIAISON_TEST_BAD", "no")
	t.Setenv("LIAISON_TEST_ZERO", "0")
	if envOr("LIAISON_TEST_STR", "d") != "x" || envOr("LIAISON_TEST_MISSING", "d") != "d" {
		t.Fatal("envOr")
	}
	if envInt("LIAISON_TEST_INT", 1) != 7 || envInt("LIAISON_TEST_BAD", 1) != 1 || envInt("LIAISON_TEST_ZERO", 1) != 1 || envInt("LIAISON_TEST_MISSING", 3) != 3 {
		t.Fatal("envInt")
	}
}
