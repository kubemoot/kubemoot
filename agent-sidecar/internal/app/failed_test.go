package app_test

import (
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bustest"
)

// A sidecar that cannot bind its consumer consumes nothing; it must say so and leave rotation, so the
// container is restarted, instead of reporting ready.
func TestASidecarThatCannotBindItsConsumerFailsInsteadOfStayingReady(t *testing.T) {
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	js := bustest.JetStream(t, nc)
	bustest.DiscussStream(t, js) // the stream exists; the operator has not created the consumer
	r := startRig(t, url, nc, func(c *mesh.Contract) { c.Bus.Consumer = "not-created-yet" })
	select {
	case <-r.app.Failed():
	case <-time.After(15 * time.Second):
		t.Fatal("the sidecar kept running with no subscription")
	}
	if r.app.Ready() {
		t.Error("a sidecar with no subscription must not be ready")
	}
	if r.app.Err() == nil {
		t.Error("the reason is kept")
	}
}
