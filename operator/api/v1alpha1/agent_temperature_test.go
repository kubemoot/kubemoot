/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	"regexp"
	"testing"
)

// crdBasesDir is where controller-gen writes the generated CRDs.
const crdBasesDir = "bases"

// publishedCRDDirs lists where every CRD is published: generated and in the chart.
func publishedCRDDirs() [][]string {
	return [][]string{{"..", "..", "config", crdDir, crdBasesDir}, {"..", "..", "chart", "kubemoot-operator", crdsDir}}
}

// An Agent's temperature is optional with no default: unset means the model
// server applies the model's own setting. A value is accepted from 0 to 2 and
// anything else is refused by the CRD schema.
func TestAgentTemperatureSchema(t *testing.T) {
	for _, dir := range publishedCRDDirs() {
		crd := loadCRD(t, append(dir, "kubemoot.ai_agents.yaml")...)
		spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
		temp, ok := spec.Properties["temperature"]
		if !ok {
			t.Fatalf("%v: spec.temperature is missing", dir)
		}
		if temp.Default != nil {
			t.Errorf("%v: spec.temperature must have no default, got %s", dir, temp.Default.Raw)
		}
		for _, r := range spec.Required {
			if r == "temperature" {
				t.Errorf("%v: spec.temperature must stay optional", dir)
			}
		}
		re := regexp.MustCompile(temp.Pattern)
		for _, good := range []string{"", "0", "0.1", "0.65", "1", "1.5", "2", "2.0"} {
			if !re.MatchString(good) {
				t.Errorf("%v: %q should be accepted", dir, good)
			}
		}
		for _, bad := range []string{"-0.1", "2.1", "3", "hot", "0,5", "1e-1", ".5", "0.", " 0.3", "10"} {
			if re.MatchString(bad) {
				t.Errorf("%v: %q should be refused", dir, bad)
			}
		}
	}
}
