package app_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bustest"
)

// syncBuf collects the sidecar's log.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) has(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(s.b.String(), text)
}

// A consumer still bound by the pod being replaced, or not yet created by the operator, is waited
// for: the sidecar says so, stays ready (a rollout needs the new pod ready before the old one goes)
// and is not failed, so the container is not restarted into a crash loop. Its readiness body and
// subscription state say it is not receiving the discussion.
func TestASidecarThatCannotBindItsConsumerYetWaitsInsteadOfFailing(t *testing.T) {
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	js := bustest.JetStream(t, nc)
	bustest.DiscussStream(t, js) // the stream exists; the operator has not created the consumer
	r := startRig(t, url, nc, func(c *mesh.Contract) { c.Bus.Consumer = "not-created-yet" })
	r.waitReady(t)
	deadline := time.Now().Add(10 * time.Second)
	for !r.logs.has("not subscribed to the crew's discussion yet") {
		if time.Now().After(deadline) {
			t.Fatal("the wait for the consumer was never logged")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-r.app.Failed():
		t.Fatalf("the sidecar gave up waiting for its consumer: %v", r.app.Err())
	default:
	}
	if !r.app.Ready() || r.app.Subscribed() {
		t.Errorf("ready=%t subscribed=%t, want ready and not subscribed", r.app.Ready(), r.app.Subscribed())
	}
	resp, err := http.Get("http://" + r.app.Addrs().HTTP + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "subscribed=false") {
		t.Errorf("/readyz = %d %q", resp.StatusCode, body)
	}
}

// A misconfiguration waiting cannot cure still fails the sidecar, so it crash-loops visibly.
func TestASidecarWhoseStreamDoesNotExistFails(t *testing.T) {
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	r := startRig(t, url, nc, func(c *mesh.Contract) { c.Bus.Stream = "NO_SUCH_STREAM" })
	select {
	case <-r.app.Failed():
	case <-time.After(15 * time.Second):
		t.Fatal("the sidecar kept waiting on a stream that does not exist")
	}
	if r.app.Err() == nil || r.app.Ready() {
		t.Errorf("err=%v ready=%t", r.app.Err(), r.app.Ready())
	}
}

// Once the consumer exists the sidecar binds it and says so.
func TestASidecarBindsOnceTheConsumerExists(t *testing.T) {
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	js := bustest.JetStream(t, nc)
	bustest.DiscussStream(t, js)
	r := startRig(t, url, nc, nil)
	r.waitReady(t)
	bustest.Consumer(t, js, identity, 30*time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for !r.app.Subscribed() {
		if time.Now().After(deadline) {
			t.Fatal("never subscribed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
