// Command scheduling-mcp is the stdio MCP server for kubemoot's
// scheduling primitives. It writes ScheduleRecords to a NATS KV bucket
// (kubemoot_scheduled) that the operator's scheduler poller fires.
//
// Configuration via env vars:
//
//	NATS_URL       — NATS server (default nats://nats.nats.svc.cluster.local:4222)
//	KUBEMOOT_CREW       — the crew this MCP serves (required)
//	KUBEMOOT_NAMESPACE  — the crew's namespace; falls back to the
//	                      service-account namespace file (one is required)
//
// Reads JSON-RPC messages line-by-line on stdin, writes responses on
// stdout. See internal/server for the protocol, internal/handlers for
// the four tools (set_reminder, schedule_followup, list_scheduled,
// cancel_scheduled).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/scheduling-mcp/internal/handlers"
	"github.com/kubemoot/kubemoot/scheduling-mcp/internal/scope"
	"github.com/kubemoot/kubemoot/scheduling-mcp/internal/server"
	"github.com/kubemoot/kubemoot/scheduling-mcp/pkg/record"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "kubemoot-scheduling-mcp"
	serverVersion   = "0.1.0"
)

func main() {
	natsURL := getenv("NATS_URL", "nats://nats.nats.svc.cluster.local:4222")
	crew := os.Getenv("KUBEMOOT_CREW")
	if crew == "" {
		fmt.Fprintln(os.Stderr, "KUBEMOOT_CREW is required")
		os.Exit(2)
	}
	namespace, err := scope.NamespaceFromEnvironment()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	// Log to stderr; stdout is reserved for the MCP wire protocol.
	log.SetOutput(os.Stderr)
	log.Printf("scheduling-mcp starting: namespace=%s crew=%s nats=%s", namespace, crew, natsURL)

	nc, err := nats.Connect(natsURL,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.Name("kubemoot-scheduling-mcp"),
	)
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}
	defer func() {
		if err := nc.Drain(); err != nil {
			log.Printf("nats drain: %v", err)
		}
	}()

	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	kv := &natsKV{js: js}
	set := handlers.New(kv, namespace, crew)

	srv := &server.Server{
		Handlers:        set,
		ProtocolVersion: protocolVersion,
		ServerName:      serverName,
		ServerVersion:   serverVersion,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Print("scheduling-mcp shutting down")
		cancel()
	}()

	if err := srv.Run(ctx, os.Stdin, os.Stdout); err != nil && err != context.Canceled {
		log.Fatalf("server: %v", err)
	}
}

// ─── NATS KV adapter ────────────────────────────────────────────────────

// natsKV implements handlers.KV against a real NATS JetStream KV bucket.
// The bucket is created on first write if absent — that mirrors how the
// rest of kubemoot creates KV buckets lazily.
type natsKV struct {
	js     nats.JetStreamContext
	bucket nats.KeyValue // lazily resolved
}

func (k *natsKV) ensure() (nats.KeyValue, error) {
	if k.bucket != nil {
		return k.bucket, nil
	}
	kv, err := k.js.KeyValue(record.Bucket)
	if err != nil {
		kv, err = k.js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket:  record.Bucket,
			History: 1,
		})
		if err != nil {
			return nil, fmt.Errorf("create kv bucket %s: %w", record.Bucket, err)
		}
	}
	k.bucket = kv
	return kv, nil
}

func (k *natsKV) Put(_ context.Context, key string, v []byte) error {
	kv, err := k.ensure()
	if err != nil {
		return err
	}
	_, err = kv.Put(key, v)
	return err
}

func (k *natsKV) Get(_ context.Context, key string) ([]byte, error) {
	kv, err := k.ensure()
	if err != nil {
		return nil, err
	}
	entry, err := kv.Get(key)
	if err != nil {
		if err == nats.ErrKeyNotFound {
			return nil, nil
		}
		return nil, err
	}
	return entry.Value(), nil
}

func (k *natsKV) Delete(_ context.Context, key string) error {
	kv, err := k.ensure()
	if err != nil {
		return err
	}
	return kv.Delete(key)
}

func (k *natsKV) Keys(_ context.Context) ([]string, error) {
	kv, err := k.ensure()
	if err != nil {
		return nil, err
	}
	lister, err := kv.ListKeys()
	if err != nil {
		// Empty bucket returns an error from some NATS versions; treat as no keys.
		return nil, nil
	}
	defer func() {
		if err := lister.Stop(); err != nil {
			log.Printf("stop key lister: %v", err)
		}
	}()
	var keys []string
	for k := range lister.Keys() {
		keys = append(keys, k)
	}
	return keys, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
