package main

import (
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

func TestWithLeaderElectionReleasesTheLeaseOnShutdown(t *testing.T) {
	f := managerFlags{
		enableLeaderElection: true,
		leaseDuration:        60 * time.Second,
		renewDeadline:        50 * time.Second,
		retryPeriod:          10 * time.Second,
	}
	o := withLeaderElection(ctrl.Options{HealthProbeBindAddress: ":8081"}, f)

	if !o.LeaderElection || !o.LeaderElectionReleaseOnCancel {
		t.Fatalf("leader election %v, release on cancel %v; want both true",
			o.LeaderElection, o.LeaderElectionReleaseOnCancel)
	}
	if o.LeaderElectionID != "7b551ae9.kubemoot.ai" {
		t.Errorf("lease ID %q changed; a new ID would let two operators lead during an upgrade", o.LeaderElectionID)
	}
	if *o.LeaseDuration != 60*time.Second || *o.RenewDeadline != 50*time.Second || *o.RetryPeriod != 10*time.Second {
		t.Errorf("durations %v/%v/%v do not follow the flags", *o.LeaseDuration, *o.RenewDeadline, *o.RetryPeriod)
	}
	if o.HealthProbeBindAddress != ":8081" {
		t.Errorf("other options were dropped: probe address %q", o.HealthProbeBindAddress)
	}
}

func TestWithLeaderElectionDisabled(t *testing.T) {
	o := withLeaderElection(ctrl.Options{}, managerFlags{})
	if o.LeaderElection {
		t.Error("leader election must stay off when the flag is off")
	}
}
