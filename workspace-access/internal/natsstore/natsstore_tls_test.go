package natsstore

import (
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

func TestStoreConnectsOverTLSWithCredentials(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthToken, JetStream: true})
	t.Setenv(natsconn.EnvCAFile, env.Config.CAFile)
	t.Setenv(natsconn.EnvToken, env.Config.Token)

	nc, _, err := Open(env.URL, "bucket").connect("test")
	if err != nil {
		t.Fatalf("connect with the CA and token from the environment: %v", err)
	}
	nc.Close()
}

func TestStoreWithoutTheOptionsFailsClearly(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthToken, JetStream: true})

	_, _, err := Open(env.URL, "bucket").connect("test")
	if err == nil || !strings.Contains(err.Error(), env.URL) {
		t.Fatalf("want an error naming %s, got %v", env.URL, err)
	}
}
