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

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kubemoot/kubemoot/operator/internal/controller"
)

// Every replica must serve the aggregated report API: the API server reaches the Service,
// which picks any pod, so a replica that waits for leadership would make calls fail at random.
func TestEveryReplicaServesWithoutLeadership(t *testing.T) {
	var r manager.LeaderElectionRunnable = everyReplica{&controller.AggregatedReportServer{}}
	if r.NeedLeaderElection() {
		t.Error("the aggregated report API must not wait for leader election")
	}
}
