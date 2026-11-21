package nats

import (
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

// setClientEnv gives the operator process the settings a chart install sets on it.
func setClientEnv(t *testing.T, c natsconn.Config) {
	t.Helper()
	t.Setenv(natsconn.EnvCAFile, c.CAFile)
	t.Setenv(natsconn.EnvUser, c.User)
	t.Setenv(natsconn.EnvPassword, c.Password)
}

func TestPublisherConnectsOverTLSWithCredentials(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthUser, JetStream: true})
	setClientEnv(t, env.Config)

	p := NewPublisher(env.URL)
	defer p.Close()
	_, _ = p.OpenCrewMemory() // the bucket may be absent; the connection is what is under test
	if !p.Connected() {
		t.Fatal("the publisher must connect with the CA and the credentials from its environment")
	}
}

func TestPublisherRefusedWithoutTheOptionsSaysSo(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthUser})
	setClientEnv(t, natsconn.Config{})

	p := NewPublisher(env.URL)
	defer p.Close()
	_, err := p.OpenCrewMemory()
	if err == nil || !strings.Contains(err.Error(), "connect NATS") || !strings.Contains(err.Error(), env.URL) {
		t.Fatalf("want a connection error naming the URL, got %v", err)
	}
	if p.Connected() {
		t.Fatal("must not connect without the CA and credentials")
	}
}
