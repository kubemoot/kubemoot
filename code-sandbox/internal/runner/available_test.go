package runner

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestConfinementAvailableReportsATurnedOffRunner(t *testing.T) {
	r := newRunner(t)
	r.Unconfined = true
	if err := r.ConfinementAvailable(context.Background()); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("got %v, want confinement is turned off", err)
	}
}

func TestConfinementAvailableMatchesWhatTheHostAllows(t *testing.T) {
	r := &Runner{Timeout: newRunner(t).Timeout, MaxOutput: 1024, TempDir: t.TempDir()}
	err := r.ConfinementAvailable(context.Background())
	if err != nil && os.Getenv("REQUIRE_CONFINEMENT") != "" {
		t.Fatalf("this host must allow confinement: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "user namespaces") {
		t.Fatalf("a refusal should name what is missing: %v", err)
	}
}
