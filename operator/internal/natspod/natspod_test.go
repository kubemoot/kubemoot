package natspod

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubemoot/kubemoot/operator/pkg/natsconn"
)

func pod() *corev1.PodSpec {
	return &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "kubemoot-sidecar"}},
		Containers:     []corev1.Container{{Name: "agent"}, {Name: "other"}},
	}
}

func envOf(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

func TestNothingSetChangesNothing(t *testing.T) {
	spec := pod()
	fromGetenv(func(string) string { return "" }).Apply(spec, "agent", "kubemoot-sidecar")
	if len(spec.Volumes) != 0 || len(spec.Containers[0].Env) != 0 || len(spec.InitContainers[0].Env) != 0 {
		t.Fatalf("an unset source must leave the pod alone: %+v", spec)
	}
}

func TestEverythingSetMountsAndNamesIt(t *testing.T) {
	env := map[string]string{
		EnvCASecret: "nats-ca", EnvCAKey: "bundle.pem", EnvClientSecret: "nats-client",
		EnvAuthSecret: "nats-auth", EnvAuthCredsKey: "app.creds",
	}
	spec := pod()
	fromGetenv(func(k string) string { return env[k] }).Apply(spec, "agent", "kubemoot-sidecar")

	if len(spec.Volumes) != 3 {
		t.Fatalf("want 3 volumes, got %d", len(spec.Volumes))
	}
	if items := spec.Volumes[0].Secret.Items; len(items) != 1 || items[0].Key != "bundle.pem" || items[0].Path != "ca.crt" {
		t.Errorf("the CA key must be projected as ca.crt: %+v", items)
	}
	for _, c := range []corev1.Container{spec.Containers[0], spec.InitContainers[0]} {
		checkMounted(t, c)
	}
	if len(spec.Containers[1].Env) != 0 || len(spec.Containers[1].VolumeMounts) != 0 {
		t.Errorf("a container not named gets nothing: %+v", spec.Containers[1])
	}
}

func TestUserAndPasswordAndTokenComeFromTheSecretNotTheSpec(t *testing.T) {
	up := fromGetenv(func(k string) string {
		return map[string]string{EnvAuthSecret: authName, EnvAuthUserKey: "u", EnvAuthPasswdKey: "p"}[k]
	})
	spec := pod()
	up.Apply(spec, "agent")
	for name, key := range map[string]string{natsconn.EnvUser: "u", natsconn.EnvPassword: "p"} {
		e := envOf(spec.Containers[0], name)
		if e == nil || e.Value != "" || e.ValueFrom.SecretKeyRef.Name != authName || e.ValueFrom.SecretKeyRef.Key != key {
			t.Errorf("%s must be a reference to key %s of the Secret: %+v", name, key, e)
		}
	}
	if len(spec.Volumes) != 0 {
		t.Errorf("a user and password need no volume: %+v", spec.Volumes)
	}

	tok := fromGetenv(func(k string) string { return map[string]string{EnvAuthSecret: authName, EnvAuthTokenKey: "t"}[k] })
	spec = pod()
	tok.Apply(spec, "agent")
	if e := envOf(spec.Containers[0], natsconn.EnvToken); e == nil || e.ValueFrom.SecretKeyRef.Key != "t" {
		t.Errorf("token reference missing: %+v", e)
	}
}

func TestAnAuthSecretWithNoKeyAddsNothing(t *testing.T) {
	spec := pod()
	fromGetenv(func(k string) string { return map[string]string{EnvAuthSecret: authName}[k] }).Apply(spec, "agent")
	if len(spec.Containers[0].Env) != 0 || len(spec.Volumes) != 0 {
		t.Fatalf("no key chosen, nothing to pass: %+v", spec)
	}
}

func TestApplyTwiceDoesNotDuplicate(t *testing.T) {
	s := fromGetenv(func(k string) string { return map[string]string{EnvCASecret: "ca"}[k] })
	spec := pod()
	s.Apply(spec, "agent")
	s.Apply(spec, "agent")
	if len(spec.Volumes) != 1 || len(spec.Containers[0].Env) != 1 || len(spec.Containers[0].VolumeMounts) != 1 {
		t.Fatalf("applied twice: %+v", spec)
	}
}

func TestSecretNamesAreDistinctAndSkipBlanks(t *testing.T) {
	s := Source{CASecret: sharedName, ClientSecret: sharedName, AuthSecret: authName}
	got := s.SecretNames()
	if len(got) != 2 || got[0] != sharedName || got[1] != authName {
		t.Fatalf("got %v", got)
	}
	if n := (Source{}).SecretNames(); len(n) != 0 {
		t.Fatalf("empty source names %v", n)
	}
}

const (
	authName   = "auth"
	sharedName = "shared"
)

func checkMounted(t *testing.T, c corev1.Container) {
	t.Helper()
	for name, want := range map[string]string{
		natsconn.EnvCAFile:         "/etc/kubemoot/nats-conn/ca/ca.crt",
		natsconn.EnvClientCertFile: "/etc/kubemoot/nats-conn/client/tls.crt",
		natsconn.EnvClientKeyFile:  "/etc/kubemoot/nats-conn/client/tls.key",
		natsconn.EnvCredsFile:      "/etc/kubemoot/nats-conn/auth/nats.creds",
	} {
		if e := envOf(c, name); e == nil || e.Value != want {
			t.Errorf("%s: %s = %+v, want %s", c.Name, name, e, want)
		}
	}
	if len(c.VolumeMounts) != 3 {
		t.Errorf("%s: want 3 mounts, got %d", c.Name, len(c.VolumeMounts))
	}
}
