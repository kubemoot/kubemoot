// Package app wires the sidecar: the contract, the NATS connection with the agent's own
// credentials, the three doors, the bus side, readiness and shutdown.
package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
)

// Names of the files in the credentials directory the operator mounts into the sidecar only.
const (
	credsFile    = "creds"
	userFile     = "user"
	passwordFile = "password"
	caFile       = "ca.crt"
)

// connectOptions reads the sidecar's own NATS credentials from dir: a creds file, or a user and
// password, and optionally a CA certificate. An absent or empty directory means an anonymous
// connection, which is how a cluster without NATS accounts runs today. The harness is never given
// this directory.
func connectOptions(dir string) ([]nats.Option, error) {
	if dir == "" {
		return nil, nil
	}
	var opts []nats.Option
	if _, err := os.Stat(filepath.Join(dir, credsFile)); err == nil {
		opts = append(opts, nats.UserCredentials(filepath.Join(dir, credsFile)))
	} else if user, err := read(dir, userFile); err == nil {
		pass, perr := read(dir, passwordFile)
		if perr != nil {
			return nil, errors.New("the credentials hold a user but no password")
		}
		opts = append(opts, nats.UserInfo(user, pass))
	}
	if _, err := os.Stat(filepath.Join(dir, caFile)); err == nil {
		opts = append(opts, nats.RootCAs(filepath.Join(dir, caFile)))
	}
	return opts, nil
}

func read(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(b)), err
}

// busOptions are the connection options of the sidecar: the cluster-wide ones every Kubemoot
// component reads from the environment (NATS_CA_FILE, NATS_CLIENT_CERT_FILE, a credential),
// then the agent's own credentials directory, which replaces the environment's credential and
// CA when it holds one. An agent with its own NATS account thus never presents the shared one.
func busOptions(dir string) ([]nats.Option, error) {
	own, err := connectOptions(dir)
	if err != nil {
		return nil, err
	}
	shared := natsconn.ForURL("")
	if dir != "" {
		if has(dir, credsFile) || has(dir, userFile) {
			shared.ClearAuth()
		}
		if has(dir, caFile) {
			shared.CAFile = ""
		}
	}
	base, err := shared.Options()
	if err != nil {
		return nil, err
	}
	return append(base, own...), nil
}

func has(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}
