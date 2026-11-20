/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package nats

import (
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/internal/retention"
)

// startServer runs a throwaway nats-server with JetStream and returns its URL. The test
// is skipped when no nats-server binary is on the PATH.
func startServer(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("nats-server")
	if err != nil {
		t.Skip("nats-server is not on the PATH")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cmd := exec.Command(bin, "-js", "-a", "127.0.0.1", "-p", fmt.Sprint(port), "-sd", t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	for i := 0; i < 50; i++ {
		if c, err := nats.Connect(url); err == nil {
			c.Close()
			return url
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nats-server did not start")
	return ""
}

func TestKVEntriesListsOnlyThePrefixWithItsWriteTime(t *testing.T) {
	p, entries := listConversationEntries(t)
	defer p.Close()
	checkLiveEntries(t, entries)
	if got, err := p.KVEntries("no_such_bucket", "x."); err != nil || got != nil {
		t.Errorf("a missing bucket lists nothing, got %v %v", got, err)
	}
}

func listConversationEntries(t *testing.T) (*Publisher, []retention.KVEntry) {
	t.Helper()
	url := startServer(t)
	p := NewPublisher(url)
	for _, k := range []string{"conv.ns.a.1", "conv.ns.a.2", "conv.ns.b.1", "conv.ns.ab.1"} {
		if err := p.PutKVValue("kubemoot_conversations", k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.DeleteKVKey("kubemoot_conversations", "conv.ns.a.2"); err != nil {
		t.Fatal(err)
	}
	entries, err := p.KVEntries("kubemoot_conversations", "conv.ns.a.")
	if err != nil {
		t.Fatal(err)
	}
	return p, entries
}

func checkLiveEntries(t *testing.T, entries []retention.KVEntry) {
	t.Helper()
	if len(entries) != 1 || entries[0].Key != "conv.ns.a.1" {
		t.Fatalf("want only the live key conv.ns.a.1, got %+v", entries)
	}
	if time.Since(entries[0].Written) > time.Minute || entries[0].Written.IsZero() {
		t.Errorf("write time not read: %v", entries[0].Written)
	}
}

func TestPurgeStreamBeforeRemovesOnlyOldMessagesOfTheSubject(t *testing.T) {
	url := startServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := nc.JetStream()
	if _, err := js.AddStream(&nats.StreamConfig{Name: "KUBEMOOT_DISCUSS", Subjects: []string{"kubemoot.discuss.>"}}); err != nil {
		t.Fatal(err)
	}
	pub := func(subject string, n int) {
		for i := 0; i < n; i++ {
			if _, err := js.Publish(subject, []byte("m")); err != nil {
				t.Fatal(err)
			}
		}
	}
	mine, other := "kubemoot.discuss.ns.a.general.t1", "kubemoot.discuss.ns.b.general.t1"
	pub(mine, 3)
	pub(other, 2)
	time.Sleep(1500 * time.Millisecond)
	cutoff := time.Now()
	time.Sleep(200 * time.Millisecond)
	pub(mine, 2)

	p := NewPublisher(url)
	defer p.Close()
	n, err := p.PurgeStreamBefore("KUBEMOOT_DISCUSS", "kubemoot.discuss.ns.a.>", cutoff)
	if err != nil || n != 3 {
		t.Fatalf("want 3 removed, got %d %v", n, err)
	}
	for subject, want := range map[string]int{"kubemoot.discuss.ns.a.>": 2, "kubemoot.discuss.ns.b.>": 2} {
		if got, _, _ := subjectCount(js, "KUBEMOOT_DISCUSS", subject); got != want {
			t.Errorf("%s holds %d, want %d", subject, got, want)
		}
	}
	checkPurgeEverythingOlder(t, p, js)
}

// checkPurgeEverythingOlder removes all of the crew's messages, leaving the others, and
// treats a missing stream as empty.
func checkPurgeEverythingOlder(t *testing.T, p *Publisher, js nats.JetStreamContext) {
	t.Helper()
	n, err := p.PurgeStreamBefore("KUBEMOOT_DISCUSS", "kubemoot.discuss.ns.a.>", time.Now().Add(time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("want the last 2 removed, got %d %v", n, err)
	}
	if got, _, _ := subjectCount(js, "KUBEMOOT_DISCUSS", "kubemoot.discuss.ns.b.>"); got != 2 {
		t.Errorf("the other crew lost messages: %d", got)
	}
	if n, err := p.PurgeStreamBefore("NO_SUCH_STREAM", "x.>", time.Now()); n != 0 || err != nil {
		t.Errorf("a missing stream removes nothing, got %d %v", n, err)
	}
}
