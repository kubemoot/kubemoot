package app

import "testing"

func TestTheExposeSwitchIsOnOnlyForTheExactValueTrue(t *testing.T) {
	cases := map[string]bool{"true": true, "": false, "false": false, "TRUE": false, "True": false, "1": false, "yes": false, " true": false, "garbage": false}
	for value, want := range cases {
		got := ExposeCallerFromEnv(func(name string) string {
			if name != "KUBEMOOT_EXPOSE_CALLER" {
				t.Fatalf("read %s", name)
			}
			return value
		})
		if got != want {
			t.Errorf("%q: %v, want %v", value, got, want)
		}
	}
}
