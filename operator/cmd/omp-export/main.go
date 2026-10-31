/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command omp-export writes one crew's memory, read from the NATS key-value
// bucket kubemoot_crew_memory, as an Open Memory Protocol directory. It only
// reads the bucket; it never writes, deletes or touches a fact.
//
//	kubectl -n nats port-forward svc/nats 4222:4222
//	go run ./cmd/omp-export --namespace crew-homelab-pilot --crew homelab-pilot --out ./memory
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/operator/internal/crewmemory"
	"github.com/kubemoot/kubemoot/operator/internal/crewmemory/omp"
	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
)

const timeout = 30 * time.Second

type options struct {
	natsURL   string
	creds     string
	namespace string
	crew      string
	out       string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "omp-export:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("omp-export", flag.ContinueOnError)
	fs.StringVar(&o.natsURL, "nats-url", nats.DefaultURL, "NATS server URL")
	fs.StringVar(&o.creds, "creds", "", "NATS credentials file (optional)")
	fs.StringVar(&o.namespace, "namespace", "", "namespace of the crew")
	fs.StringVar(&o.crew, "crew", "", "name of the crew")
	fs.StringVar(&o.out, "out", "", "directory to write; must not exist or be empty")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.namespace == "" || o.crew == "" || o.out == "" {
		return o, fmt.Errorf("--namespace, --crew and --out are required")
	}
	return o, nil
}

type opener func(options) (crewmemory.Reader, func(), error)

func run(args []string, stdout io.Writer) error {
	return runWith(args, stdout, connect)
}

func runWith(args []string, stdout io.Writer, open opener) error {
	o, err := parseFlags(args)
	if err != nil {
		return err
	}
	scope, err := crewscope.New(o.namespace, o.crew)
	if err != nil {
		return err
	}
	reader, closeFn, err := open(o)
	if err != nil {
		return err
	}
	defer closeFn()
	n, err := export(reader, scope, o.out)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %d fact(s) of %s/%s to %s\n", n, scope.Namespace, scope.Crew, o.out)
	return err
}

// export reads the crew's facts and writes them as an OMP directory.
func export(reader crewmemory.Reader, scope crewscope.Scope, out string) (int, error) {
	facts, err := crewmemory.ReadFacts(reader, scope)
	if err != nil {
		return 0, err
	}
	tree, err := omp.Build(scope, facts)
	if err != nil {
		return 0, err
	}
	if err := omp.ValidateTree(tree); err != nil {
		return 0, err
	}
	return len(facts), omp.Write(out, tree)
}

// connect opens the bucket for reading. It is not the operator's Publisher on
// purpose: the Publisher treats an unreachable server as an empty bucket, and an
// export that silently finds no facts is worse than one that fails.
func connect(o options) (crewmemory.Reader, func(), error) {
	var opts []nats.Option
	if o.creds != "" {
		opts = append(opts, nats.UserCredentials(o.creds))
	}
	nc, err := nats.Connect(o.natsURL, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("connect %s: %w", o.natsURL, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	kv, err := js.KeyValue(ctx, crewmemory.Bucket)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("open bucket %s: %w", crewmemory.Bucket, err)
	}
	return &kvReader{kv: kv}, nc.Close, nil
}

// kvReader adapts a JetStream bucket to crewmemory.Reader. It offers no write.
// Each call gets its own timeout.
type kvReader struct {
	kv jetstream.KeyValue
}

func (r *kvReader) ListKVKeys(string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	keys, err := r.kv.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return nil, nil
	}
	return keys, err
}

func (r *kvReader) GetKVValue(_, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	e, err := r.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return e.Value(), nil
}
