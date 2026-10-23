package app_test

import (
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bustest"
)

// A consumer still bound by the pod being replaced, or not yet created by the operator, is waited
// for: the sidecar stays up and ready (a rollout needs the new pod ready before the old one goes)
// and is not failed, so the container is not restarted into a crash loop.
func TestASidecarThatCannotBindItsConsumerYetWaitsInsteadOfFailing(t *testing.T) {
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	js := bustest.JetStream(t, nc)
	bustest.DiscussStream(t, js) // the stream exists; the operator has not created the consumer
	r := startRig(t, url, nc, func(c *mesh.Contract) { c.Bus.Consumer = "not-created-yet" })
	r.waitReady(t)
	select {
	case <-r.app.Failed():
		t.Fatalf("the sidecar gave up waiting for its consumer: %v", r.app.Err())
	case <-time.After(1500 * time.Millisecond):
	}
}
