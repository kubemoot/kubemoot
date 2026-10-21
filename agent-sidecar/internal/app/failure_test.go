package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

func minimal() mesh.Contract {
	return mesh.Contract{ContractVersion: mesh.ContractVersion,
		Identity: mesh.Identity{Namespace: "n", Crew: "c", Agent: "a"}, Bus: mesh.Bus{NATSURL: "nats://x:4222"},
		Turn:   mesh.Turn{URL: "http://127.0.0.1:9000", Concurrency: 1, DeadlineSeconds: 5},
		Models: mesh.Models{Aliases: map[string]mesh.Alias{mesh.DefaultAlias: {Candidates: []string{"m"}}}}}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConnectOptionsReadTheSidecarsOwnCredentials(t *testing.T) {
	if opts, err := connectOptions(""); err != nil || len(opts) != 0 {
		t.Errorf("no directory is an anonymous connection: %v %v", opts, err)
	}
	empty := t.TempDir()
	if opts, err := connectOptions(empty); err != nil || len(opts) != 0 {
		t.Errorf("an empty directory is anonymous too: %v %v", opts, err)
	}
	user := t.TempDir()
	write(t, user, userFile, "agent\n")
	write(t, user, passwordFile, "secret\n")
	if opts, err := connectOptions(user); err != nil || len(opts) != 1 {
		t.Errorf("a user and password make one option: %v %v", opts, err)
	}
	half := t.TempDir()
	write(t, half, userFile, "agent")
	if _, err := connectOptions(half); err == nil {
		t.Error("a user with no password is refused, not run anonymous")
	}
	creds := t.TempDir()
	write(t, creds, credsFile, "creds")
	write(t, creds, caFile, "ca")
	if opts, err := connectOptions(creds); err != nil || len(opts) != 2 {
		t.Errorf("creds and a CA make two options: %v %v", opts, err)
	}
}

func TestFirst(t *testing.T) {
	if first(nil) != "" || first([]string{"a", "b"}) != "a" {
		t.Error("first returns the first element, or nothing")
	}
}

func TestFailureClosesFailedOnceAndTakesThePodOutOfRotation(t *testing.T) {
	a := New(minimal(), Options{})
	a.natsUp.Store(true)
	a.harnessUp.Store(true)
	a.fatal(context.DeadlineExceeded)
	a.fatal(context.Canceled)
	select {
	case <-a.Failed():
	case <-time.After(time.Second):
		t.Fatal("Failed must be closed")
	}
	if a.Ready() {
		t.Error("a failed sidecar is not ready")
	}
	if a.Err() != context.DeadlineExceeded {
		t.Errorf("the first failure is the one kept: %v", a.Err())
	}
}
