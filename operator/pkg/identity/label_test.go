/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"encoding/json"
	"os"
	"testing"
)

// The cases are shared with the agent runtime's TurnContext tests, so both sides hold one rule.
func TestLabelFollowsTheSharedCases(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/principal-label-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		MaxLabelChars int `json:"maxLabelChars"`
		Labels        []struct{ Input, Label string }
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if cases.MaxLabelChars != MaxLabelChars || len(cases.Labels) == 0 {
		t.Fatalf("fixture bound %d, code %d, %d cases", cases.MaxLabelChars, MaxLabelChars, len(cases.Labels))
	}
	for _, c := range cases.Labels {
		if got := Label(c.Input); got != c.Label {
			t.Errorf("Label(%q) = %q, want %q", c.Input, got, c.Label)
		}
	}
}

func TestAWorkloadsNameIsALabelOnlyWhenItIsAPlainToken(t *testing.T) {
	plain := PrincipalOf(Hasher{}, &Identity{Issuer: IssuerKubernetes, Subject: "system:serviceaccount:ns:builder"})
	if plain.DisplayName != "system:serviceaccount:ns:builder" {
		t.Errorf("plain workload name: %q", plain.DisplayName)
	}
	odd := PrincipalOf(Hasher{}, &Identity{Issuer: IssuerKubernetes, Subject: "system:serviceaccount:ns:bu ilder"})
	if odd.DisplayName != "" || odd.ID == "" {
		t.Errorf("odd workload name: %+v", odd)
	}
}
