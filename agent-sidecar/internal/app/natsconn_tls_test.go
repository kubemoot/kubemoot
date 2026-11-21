package app

import (
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

func setSharedEnv(t *testing.T, c natsconn.Config) {
	t.Helper()
	t.Setenv(natsconn.EnvCAFile, c.CAFile)
	t.Setenv(natsconn.EnvUser, c.User)
	t.Setenv(natsconn.EnvPassword, c.Password)
}

func TestSidecarConnectsOverTLSWithTheSharedOptions(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthUser})
	setSharedEnv(t, env.Config)

	opts, err := busOptions("")
	if err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(env.URL, opts...)
	if err != nil {
		t.Fatalf("connect with the cluster-wide options: %v", err)
	}
	nc.Close()
}

func TestSidecarWithoutTheOptionsIsRefused(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthUser})
	setSharedEnv(t, natsconn.Config{})

	opts, err := busOptions("")
	if err != nil {
		t.Fatal(err)
	}
	if nc, err := nats.Connect(env.URL, opts...); err == nil {
		nc.Close()
		t.Fatal("a TLS server with credentials must refuse a bare client")
	}
}

func TestTheAgentsOwnCredentialsReplaceTheSharedOnes(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthUser})
	// The shared credential is wrong; the agent's own directory holds the right one.
	setSharedEnv(t, natsconn.Config{CAFile: env.Config.CAFile, User: "shared", Password: "wrong"})
	dir := t.TempDir()
	write(t, dir, userFile, env.Config.User)
	write(t, dir, passwordFile, env.Config.Password)

	opts, err := busOptions(dir)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(env.URL, opts...)
	if err != nil {
		t.Fatalf("the agent's own credentials must win over the shared ones: %v", err)
	}
	nc.Close()
}

func TestBusOptionsRefuseAContradictoryEnvironment(t *testing.T) {
	t.Setenv(natsconn.EnvClientCertFile, "/only/a/cert")
	if _, err := busOptions(""); err == nil {
		t.Fatal("a client certificate without its key must be refused at startup")
	}
}
