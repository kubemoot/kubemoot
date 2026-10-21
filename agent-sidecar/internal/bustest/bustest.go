// Package bustest is the test kit for code that speaks the bus: a real NATS server with JetStream,
// the streams and consumers the operator creates, and validation of a message against the bus
// contract's JSON Schema files. It is imported by tests only.
package bustest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

const baseURL = "https://kubemoot.test/bus/"

// Server starts a NATS server with JetStream on a free port and returns its URL.
func Server(t testing.TB) string {
	t.Helper()
	s, err := server.NewServer(&server.Options{Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true,
		NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats server did not start")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	return s.ClientURL()
}

// Connect connects to url and closes the connection with the test.
func Connect(t testing.TB, url string, opts ...nats.Option) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// JetStream returns a JetStream context for nc.
func JetStream(t testing.TB, nc *nats.Conn) nats.JetStreamContext {
	t.Helper()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	return js
}

// DiscussStream creates the KUBEMOOT_DISCUSS stream as the chart does.
func DiscussStream(t testing.TB, js nats.JetStreamContext) {
	t.Helper()
	if _, err := js.AddStream(&nats.StreamConfig{Name: "KUBEMOOT_DISCUSS", Subjects: []string{"kubemoot.discuss.>"},
		MaxAge: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
}

// Consumer creates the agent's durable consumer as the operator's EnsureConsumer does: a push
// consumer with a random delivery inbox, explicit acknowledgement, deliver-new and MaxDeliver 3.
// ackWait is the operator's 120 seconds in production; tests shorten it.
func Consumer(t testing.TB, js nats.JetStreamContext, id mesh.Identity, ackWait time.Duration) {
	t.Helper()
	_, err := js.AddConsumer("KUBEMOOT_DISCUSS", &nats.ConsumerConfig{
		Durable: id.ConsumerName(), DeliverPolicy: nats.DeliverNewPolicy, DeliverSubject: nats.NewInbox(),
		AckPolicy: nats.AckExplicitPolicy, AckWait: ackWait, MaxDeliver: 3, FilterSubjects: id.DiscussFilters(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// schemaDir is the bus contract's schema directory, found from this file.
func schemaDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "operator", "pkg", "bus", "schema")
}

var compiled = map[string]*jsonschema.Schema{}

func schema(t testing.TB, name string) *jsonschema.Schema {
	t.Helper()
	if s, ok := compiled[name]; ok {
		return s
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	entries, err := os.ReadDir(schemaDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(schemaDir(), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AddResource(baseURL+e.Name(), doc); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.Compile(baseURL + name)
	if err != nil {
		t.Fatal(err)
	}
	compiled[name] = s
	return s
}

// Validate fails the test unless data matches the named schema file (for example
// "discussion-message.json") and, like the operator's contract test, carries no field the schema
// does not declare: at the top level, and in the metadata of a discussion message.
func Validate(t testing.TB, name string, data []byte) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not JSON: %v: %s", err, data)
	}
	if err := schema(t, name).Validate(inst); err != nil {
		t.Fatalf("%s does not match %s: %v", data, name, err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	undeclared(t, name, name, m)
	if md, ok := m["metadata"].(map[string]any); ok && name == "discussion-message.json" {
		undeclared(t, "discussion-metadata.json", "metadata", md)
		if art, ok := md["artifact"].(map[string]any); ok {
			undeclared(t, "artifact-reference.json", "metadata.artifact", art)
		}
	}
	if art, ok := m["artifact"].(map[string]any); ok && name == "artifact-notice.json" {
		undeclared(t, "artifact-reference.json", "artifact", art)
	}
}

// undeclared fails the test for each key of obj that the schema file does not declare as a property.
func undeclared(t testing.TB, file, where string, obj map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(schemaDir(), file))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for k := range obj {
		if _, ok := doc.Properties[k]; !ok {
			t.Errorf("%s carries %q, which %s does not declare", where, k, file)
		}
	}
}
