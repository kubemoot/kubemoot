package nats

import (
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

func TestClientConnectsOverTLSWithCredentials(t *testing.T) {
	env := natstest.Start(t, natstest.Options{MutualTLS: true, Auth: natstest.AuthUser, JetStream: true})
	t.Setenv(natsconn.EnvCAFile, env.Config.CAFile)
	t.Setenv(natsconn.EnvClientCertFile, env.Config.ClientCertFile)
	t.Setenv(natsconn.EnvClientKeyFile, env.Config.ClientKeyFile)
	t.Setenv(natsconn.EnvUser, env.Config.User)
	t.Setenv(natsconn.EnvPassword, env.Config.Password)

	c := NewClient(env.URL)
	defer c.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the connection over mutual TLS and a user", c.Connected)
}

func TestClientStartFailsAtOnceOnAnUnusableOption(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true})
	t.Setenv(natsconn.EnvCAFile, "/no/such/ca.crt")

	c := NewClient(env.URL)
	defer c.Close()
	if err := c.Start(); err == nil {
		t.Fatal("a CA file that does not exist must stop the gateway at startup, not retry forever")
	}
}

func TestClientWithoutTheCAStaysDisconnected(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthUser})

	c := NewClient(env.URL)
	defer c.Close()
	_ = c.Start() // retries in the background; the TLS handshake is never accepted
	time.Sleep(500 * time.Millisecond)
	if c.Connected() {
		t.Fatal("a client with no CA and no credentials must not connect")
	}
}
