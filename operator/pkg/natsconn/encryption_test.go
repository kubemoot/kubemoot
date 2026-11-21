package natsconn_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

const secretPayload = "kubemoot-plaintext-marker-7f3a"

// encryptionConfig is the recipe the install guide gives: the key comes from an environment
// variable (a Secret in the pod), never from the file.
func encryptionConfig(t *testing.T, dir string, encrypted bool) string {
	t.Helper()
	conf := fmt.Sprintf("host: 127.0.0.1\nport: -1\njetstream {\n  store_dir: %q\n", filepath.Join(dir, "js"))
	if encrypted {
		conf += "  cipher: chachapoly\n  encryption_key: $JS_ENCRYPTION_KEY\n"
	}
	conf += "}\n"
	p := filepath.Join(dir, "nats.conf")
	if err := os.WriteFile(p, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runServer(t *testing.T, conf string) (*server.Server, func()) {
	t.Helper()
	opts, err := server.ProcessConfigFile(conf)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	opts.NoLog, opts.NoSigs = true, true
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("server not ready")
	}
	return s, func() { s.Shutdown(); s.WaitForShutdown() }
}

func publishOne(t *testing.T, s *server.Server) {
	t.Helper()
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "S", Subjects: []string{"s.>"}, Storage: nats.FileStorage}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
	if _, err := js.Publish("s.one", []byte(secretPayload)); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func storeHoldsPlaintext(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(filepath.Join(dir, "js"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			if b, rerr := os.ReadFile(p); rerr == nil && bytes.Contains(b, []byte(secretPayload)) {
				found = true
			}
		}
		return nil
	})
	return found
}

func TestJetStreamFileStoreIsEncryptedWithTheDocumentedConfig(t *testing.T) {
	t.Setenv("JS_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef-test")
	dir := t.TempDir()
	s, stop := runServer(t, encryptionConfig(t, dir, true))
	publishOne(t, s)
	stop()
	if storeHoldsPlaintext(t, dir) {
		t.Fatal("a message is readable in the file store: the cipher and key were not applied")
	}
}

func TestJetStreamFileStoreIsPlaintextWithoutTheKey(t *testing.T) {
	dir := t.TempDir()
	s, stop := runServer(t, encryptionConfig(t, dir, false))
	publishOne(t, s)
	stop()
	if !storeHoldsPlaintext(t, dir) {
		t.Fatal("the control store should hold the message in plaintext")
	}
}

// A store written without a key is read and rewritten encrypted when the key is turned on.
// Turning the key off, or changing it, afterwards leaves the server unable to read the store.
func TestKeyOnOverAnExistingStoreEncryptsItAndTheKeyIsThenRequired(t *testing.T) {
	dir := t.TempDir()
	s, stop := runServer(t, encryptionConfig(t, dir, false))
	publishOne(t, s)
	stop()

	t.Setenv("JS_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef-test")
	s2, stop2 := runServer(t, encryptionConfig(t, dir, true))
	if n := messages(t, s2); n != 1 {
		t.Fatalf("the existing message must survive turning the key on, got %d", n)
	}
	stop2()
	if storeHoldsPlaintext(t, dir) {
		t.Fatal("the existing store was not encrypted when the key was turned on")
	}

	s3, stop3 := runServer(t, encryptionConfig(t, dir, false))
	defer stop3()
	t.Logf("messages readable after dropping the key: %d", messages(t, s3))
	if n := messages(t, s3); n != 0 {
		t.Fatalf("an encrypted store must not be readable without its key, got %d messages", n)
	}
}

func messages(t *testing.T, s *server.Server) uint64 {
	t.Helper()
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := nc.JetStream()
	info, err := js.StreamInfo("S")
	if err != nil {
		return 0
	}
	return info.State.Msgs
}
