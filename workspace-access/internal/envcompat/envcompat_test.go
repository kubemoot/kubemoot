package envcompat

import (
	"fmt"
	"testing"
)

const (
	currentBucketVar = "WORKSPACE_BUCKET"
	legacyBucketVar  = "ARTIFACT_BUCKET"
	sameBucket       = "same"
)

func lookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestTheCurrentNameWinsAndWarnsOfNothing(t *testing.T) {
	var warnings []string
	get := Wrap(lookup(map[string]string{currentBucketVar: "new", legacyBucketVar: "old"}),
		func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) })
	if got := get(currentBucketVar); got != "new" {
		t.Errorf("got %q, want the current name's value", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestTheLegacyNameAnswersAndWarnsOnce(t *testing.T) {
	var warnings []string
	get := Wrap(lookup(map[string]string{"ARTIFACT_QUERY_ENGINE": "sqlite"}),
		func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) })
	for i := 0; i < 3; i++ {
		if got := get("WORKSPACE_QUERY_ENGINE"); got != "sqlite" {
			t.Fatalf("got %q, want the legacy value", got)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	want := "ARTIFACT_QUERY_ENGINE is deprecated and is removed in the next release; set WORKSPACE_QUERY_ENGINE instead"
	if warnings[0] != want {
		t.Errorf("warning = %q", warnings[0])
	}
}

func TestOtherNamesPassThroughAndAnUnsetNameIsEmpty(t *testing.T) {
	warned := false
	get := Wrap(lookup(map[string]string{"NATS_URL": "nats://x", "ARTIFACT_X": "legacy"}),
		func(string, ...any) { warned = true })
	if got := get("NATS_URL"); got != "nats://x" {
		t.Errorf("got %q", got)
	}
	if got := get("ARTIFACT_X"); got != "legacy" || warned {
		t.Errorf("a name outside WORKSPACE_ must pass through unchanged and unwarned: %q %v", got, warned)
	}
	if got := get("WORKSPACE_NONE"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestBucketsAreTheWorkspaceBucketThenTheLegacyOne(t *testing.T) {
	got := Buckets(lookup(nil), func(string, ...any) { t.Error("nothing set, nothing to warn of") })
	if len(got) != 2 || got[0] != "kubemoot_workspace" || got[1] != "kubemoot_discussion_artifacts" {
		t.Errorf("got %v", got)
	}
}

func TestBucketsHonorBothVariablesAndWarnOfTheOldOne(t *testing.T) {
	var warned int
	warn := func(string, ...any) { warned++ }
	got := Buckets(lookup(map[string]string{currentBucketVar: "w", legacyBucketVar: "a"}), warn)
	if len(got) != 2 || got[0] != "w" || got[1] != "a" || warned != 0 {
		t.Errorf("both set: got %v, warned %d", got, warned)
	}
	got = Buckets(lookup(map[string]string{legacyBucketVar: "custom"}), warn)
	if len(got) != 2 || got[0] != "kubemoot_workspace" || got[1] != "custom" || warned != 1 {
		t.Errorf("only the old one set: got %v, warned %d", got, warned)
	}
}

func TestABucketNamedTwiceIsReadOnce(t *testing.T) {
	got := Buckets(lookup(map[string]string{currentBucketVar: sameBucket, legacyBucketVar: sameBucket}), func(string, ...any) {})
	if len(got) != 1 || got[0] != sameBucket {
		t.Errorf("got %v", got)
	}
}
