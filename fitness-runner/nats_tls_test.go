package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

func TestPutObjectWritesOverTLSWithCredentials(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthUser, JetStream: true})
	t.Setenv(natsconn.EnvCAFile, env.Config.CAFile)
	t.Setenv(natsconn.EnvUser, env.Config.User)
	t.Setenv(natsconn.EnvPassword, env.Config.Password)

	if err := putObject(env.URL, "tls_artifacts", "ns/suite/run/s0-i0.json", []byte(`{}`), time.Hour); err != nil {
		t.Fatalf("write with the CA and credentials from the environment: %v", err)
	}
}

func TestPutObjectWithoutTheOptionsFailsNamingTheServer(t *testing.T) {
	env := natstest.Start(t, natstest.Options{TLS: true, Auth: natstest.AuthUser, JetStream: true})

	err := putObject(env.URL, "tls_artifacts", "k", []byte(`{}`), time.Hour)
	if err == nil || !strings.Contains(err.Error(), env.URL) {
		t.Fatalf("want a connection error naming %s, got %v", env.URL, err)
	}
}
