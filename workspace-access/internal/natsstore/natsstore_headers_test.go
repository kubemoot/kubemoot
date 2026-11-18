package natsstore

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// natsServer starts the nats-server in NATS_SERVER_BIN with JetStream and returns its URL;
// the test is skipped when the variable does not name an executable.
func natsServer(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("NATS_SERVER_BIN")
	if bin == "" {
		t.Skip("NATS_SERVER_BIN not set")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cmd := exec.Command(bin, "-js", "-p", fmt.Sprint(port), "-sd", t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	for i := 0; i < 50; i++ {
		if nc, err := nats.Connect(url); err == nil {
			nc.Close()
			return url
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nats-server did not start")
	return ""
}

func put(t *testing.T, obs jetstream.ObjectStore, name, body string, headers nats.Header) {
	t.Helper()
	meta := jetstream.ObjectMeta{Name: name, Headers: headers}
	if _, err := obs.Put(context.Background(), meta, bytes.NewReader([]byte(body))); err != nil {
		t.Fatal(err)
	}
}

const (
	keyPods1 = "ns/c/t1/a/001-pods_list"
	keyPods2 = "ns/c/t1/a/002-pods_list"
	toolName = "gen_ai.tool.name"
	toolPods = "pods_list"
	rowsKey  = "kubemoot.rows"
)

// seeded starts a server, fills a bucket as the agent runtime would, and returns a Store on it.
func seeded(t *testing.T) *Store {
	t.Helper()
	url := natsServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	obs, err := js.CreateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: "wq"})
	if err != nil {
		t.Fatal(err)
	}
	put(t, obs, keyPods1, "[1]", nats.Header{toolName: {toolPods}, rowsKey: {"1"}})
	put(t, obs, keyPods2, "[1,2]", nats.Header{toolName: {toolPods}})
	put(t, obs, "ns/c/t1/a/003-gone", "x", nil)
	put(t, obs, "ns/c/t2/a/001-pods_list", "[]", nil)
	if err := obs.Delete(ctx, "ns/c/t1/a/003-gone"); err != nil {
		t.Fatal(err)
	}
	return Open(url, "wq")
}

func TestDescribeReturnsTheSizeAndFirstValueOfEachHeader(t *testing.T) {
	s := seeded(t)

	m, err := s.Describe(context.Background(), keyPods1)

	if err != nil {
		t.Fatal(err)
	}
	if m.Headers[toolName] != toolPods || m.Headers[rowsKey] != "1" || m.Size != 3 {
		t.Fatalf("meta = %+v", m)
	}
	if _, err := s.Describe(context.Background(), "ns/c/t1/a/missing"); err == nil {
		t.Fatal("describing a missing object must fail")
	}
}

func TestListReturnsTheLiveObjectsUnderAPrefixInKeyOrder(t *testing.T) {
	s := seeded(t)

	list, err := s.List(context.Background(), "ns/c/t1/")

	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Key != keyPods1 || list[1].Key != keyPods2 {
		t.Fatalf("list = %+v, want the two live objects of t1 in key order", list)
	}
	if none, err := s.List(context.Background(), "ns/c/t9/"); err != nil || len(none) != 0 {
		t.Fatalf("an unused prefix lists %v, %v", none, err)
	}
}

func TestDescribeAndListReturnConnectErrorsOnUnreachableServer(t *testing.T) {
	s := Open("nats://127.0.0.1:1", "b")
	if _, err := s.Describe(context.Background(), "k"); err == nil {
		t.Fatal("Describe: want an error")
	}
	if _, err := s.List(context.Background(), "k"); err == nil {
		t.Fatal("List: want an error")
	}
}
