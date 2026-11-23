package registry

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConnForms(t *testing.T) {
	env := Env{User: envUser, Password: envPass, DBName: envDB}
	cases := []struct {
		endpoint string
		want     Conn
	}{
		{"postgresql://u:p%40ss@db.example:6543/vecs", Conn{exampleHost, 6543, "vecs", "u", "p@ss"}},
		{"postgres://db.example", Conn{exampleHost, 5432, defaultDBName, "postgres", ""}},
		{exampleHost + ":5433", Conn{exampleHost, 5433, envDB, envUser, envPass}},
		{exampleHost + ":5433/other", Conn{exampleHost, 5433, "other", envUser, envPass}},
		{exampleHost, Conn{exampleHost, 5432, envDB, envUser, envPass}},
	}
	for _, c := range cases {
		got, err := ResolveConn(VectorStore{Endpoint: c.endpoint}, env)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v, %v; want %+v", c.endpoint, got, err, c.want)
		}
	}
}

func TestResolveConnDefaultsWithoutEnv(t *testing.T) {
	got, err := ResolveConn(VectorStore{Endpoint: "db"}, Env{})
	want := Conn{"db", 5432, defaultDBName, "postgres", ""}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestResolveConnRejectsBadPorts(t *testing.T) {
	for _, ep := range []string{"db:notaport", "postgres://db:notaport/x"} {
		if _, err := ResolveConn(VectorStore{Endpoint: ep}, Env{}); err == nil {
			t.Errorf("%s accepted", ep)
		}
	}
}

func TestCredentialsDirOverridesTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "password"), "s3cret\n")
	write(t, filepath.Join(dir, "username"), "svc\n")
	got, err := ResolveConn(VectorStore{Endpoint: "db:5432", CredentialsDir: dir}, Env{User: envUser, Password: envPass})
	if err != nil || got.User != "svc" || got.Password != "s3cret" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCredentialsDirKeepsTheUserWhenOnlyAPasswordIsMounted(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "password"), "s3cret")
	got, err := ResolveConn(VectorStore{Endpoint: "db:5432", CredentialsDir: dir}, Env{User: envUser})
	if err != nil || got.User != envUser || got.Password != "s3cret" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestConnStringHidesThePasswordAndURLEscapesIt(t *testing.T) {
	c := Conn{Host: "h", Port: 1, DBName: "d", User: "u s", Password: "p@:/?"}
	if strings.Contains(c.String(), "p@") {
		t.Errorf("String leaks the password: %s", c)
	}
	if got := c.URL(); !strings.HasPrefix(got, "postgres://u%20s:p%40%3A%2F%3F@h:1/d") {
		t.Errorf("URL %s", got)
	}
}

const (
	exampleHost = "db.example"
	envDB       = "envdb"
	envUser     = "envuser"
	envPass     = "envpass"
)

func TestAConnectionNeverMarshalsItsPassword(t *testing.T) {
	b, err := json.Marshal(Conn{Host: "h", Password: "hunter2"})
	if err != nil || strings.Contains(string(b), "hunter2") {
		t.Fatalf("%s %v", b, err)
	}
}
