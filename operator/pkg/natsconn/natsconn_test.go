package natsconn_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
	"github.com/kubemoot/kubemoot/operator/pkg/natsconn/natstest"
)

func TestFromEnvReadsEveryName(t *testing.T) {
	env := map[string]string{
		natsconn.EnvURL: " tls://nats:4222 ", natsconn.EnvCAFile: "/ca.crt",
		natsconn.EnvClientCertFile: "/c.crt", natsconn.EnvClientKeyFile: "/c.key",
		natsconn.EnvCredsFile: "/u.creds", natsconn.EnvUser: "u", natsconn.EnvPassword: "p", natsconn.EnvToken: "t",
	}
	got := natsconn.FromEnv(func(k string) string { return env[k] })
	want := natsconn.Config{URL: "tls://nats:4222", CAFile: "/ca.crt", ClientCertFile: "/c.crt", ClientKeyFile: "/c.key",
		CredsFile: "/u.creds", User: "u", Password: "p", Token: "t"}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if empty := natsconn.FromEnv(func(string) string { return "" }); empty != (natsconn.Config{}) {
		t.Fatalf("unset names must give the zero Config, got %+v", empty)
	}
}

func TestValidateRejectsContradictions(t *testing.T) {
	cases := map[string]natsconn.Config{
		"cert without key":      {ClientCertFile: "/c"},
		"key without cert":      {ClientKeyFile: "/k"},
		"user without password": {User: "u"},
		"password without user": {Password: "p"},
		"creds and token":       {CredsFile: "/c", Token: "t"},
		"user and token":        {User: "u", Password: "p", Token: "t"},
		"creds and user":        {CredsFile: "/c", User: "u", Password: "p"},
	}
	for name, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	for name, c := range map[string]natsconn.Config{"zero": {}, "token": {Token: "t"}, "user": {User: "u", Password: "p"}} {
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOptionsNameTheVariableOfAFileThatIsMissingOrWrong(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cfg  natsconn.Config
		want string
	}{
		{natsconn.Config{CAFile: filepath.Join(dir, "absent")}, natsconn.EnvCAFile},
		{natsconn.Config{CAFile: notPEM}, "no PEM certificate"},
		{natsconn.Config{ClientCertFile: empty, ClientKeyFile: empty}, "is empty"},
		{natsconn.Config{CredsFile: filepath.Join(dir, "absent")}, natsconn.EnvCredsFile},
	}
	for _, c := range cases {
		_, err := c.cfg.Options()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: error %v does not mention %q", c.cfg, err, c.want)
		}
	}
}

func TestOptionsCountsWhatIsSet(t *testing.T) {
	if opts, err := (natsconn.Config{}).Options(); err != nil || len(opts) != 0 {
		t.Fatalf("zero config must add no options, got %d, %v", len(opts), err)
	}
	if opts, err := (natsconn.Config{Token: "t"}).Options(); err != nil || len(opts) != 1 {
		t.Fatalf("a token is one option, got %d, %v", len(opts), err)
	}
}

func TestDescribeNeverShowsASecret(t *testing.T) {
	d := natsconn.Config{User: "alice", Password: "hunter2"}.Describe()
	for _, secret := range []string{"alice", "hunter2"} {
		if strings.Contains(d, secret) {
			t.Errorf("Describe leaked %q: %s", secret, d)
		}
	}
	if got := (natsconn.Config{}).Describe(); !strings.Contains(got, "anonymous") {
		t.Errorf("zero config describes as %q", got)
	}
}

func TestConnectToServers(t *testing.T) {
	cases := []struct {
		name string
		opts natstest.Options
	}{
		{"plain and anonymous", natstest.Options{}},
		{"tls", natstest.Options{TLS: true}},
		{"tls and user", natstest.Options{TLS: true, Auth: natstest.AuthUser}},
		{"tls and token", natstest.Options{TLS: true, Auth: natstest.AuthToken}},
		{"tls and creds", natstest.Options{TLS: true, Auth: natstest.AuthCreds}},
		{"mutual tls and user", natstest.Options{MutualTLS: true, Auth: natstest.AuthUser}},
		{"user without tls", natstest.Options{Auth: natstest.AuthUser}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := natstest.Start(t, c.opts)
			conn, err := env.Config.Connect()
			if err != nil {
				t.Fatalf("connect with the full settings: %v", err)
			}
			conn.Close()
		})
	}
}

func TestConnectFailsClearlyWithoutWhatTheServerDemands(t *testing.T) {
	cases := []struct {
		name   string
		opts   natstest.Options
		strip  func(*natsconn.Config)
		wanted string
	}{
		{"no CA", natstest.Options{TLS: true}, func(c *natsconn.Config) { c.CAFile = "" }, "certificate"},
		{"no credentials", natstest.Options{TLS: true, Auth: natstest.AuthUser}, func(c *natsconn.Config) { c.ClearAuth() }, "uthorization"},
		{"wrong password", natstest.Options{Auth: natstest.AuthUser}, func(c *natsconn.Config) { c.Password = "nope" }, "uthorization"},
		{"wrong token", natstest.Options{Auth: natstest.AuthToken}, func(c *natsconn.Config) { c.Token = "nope" }, "uthorization"},
		{"no creds", natstest.Options{Auth: natstest.AuthCreds}, func(c *natsconn.Config) { c.ClearAuth() }, "uthorization"},
		{"no client certificate", natstest.Options{MutualTLS: true}, func(c *natsconn.Config) { c.ClientCertFile, c.ClientKeyFile = "", "" }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := natstest.Start(t, c.opts)
			cfg := env.Config
			c.strip(&cfg)
			conn, err := cfg.Connect()
			if err == nil {
				conn.Close()
				t.Fatal("expected the connection to be refused")
			}
			if !strings.Contains(err.Error(), env.URL) || !strings.Contains(err.Error(), c.wanted) {
				t.Fatalf("error %q should name the URL %s and mention %q", err, env.URL, c.wanted)
			}
		})
	}
}
