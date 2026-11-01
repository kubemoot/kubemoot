/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command ompi-export writes one crew's memory, read from the NATS key-value
// bucket kubemoot_crew_memory, in the interchange form of the Open Memory
// Protocol Initiative (OMPI) exchange-and-runtime working spec v0.2: a manifest
// and one JSON memory object per fact, and with --markdown the runtime
// projection as Markdown files. It only reads the bucket; it never writes,
// deletes or touches a fact.
//
//	kubectl -n nats port-forward svc/nats 4222:4222
//	go run ./cmd/ompi-export --namespace crew-homelab-pilot --crew homelab-pilot --out ./memory
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/natskv"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory/ompi"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

type options struct {
	natsURL   string
	creds     string
	namespace string
	crew      string
	out       string
	origin    string
	markdown  bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ompi-export:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("ompi-export", flag.ContinueOnError)
	fs.StringVar(&o.natsURL, "nats-url", nats.DefaultURL, "NATS server URL")
	fs.StringVar(&o.creds, "creds", "", "NATS credentials file (optional)")
	fs.StringVar(&o.namespace, "namespace", "", "namespace of the crew")
	fs.StringVar(&o.crew, "crew", "", "name of the crew")
	fs.StringVar(&o.out, "out", "", "directory to write; must not exist or be empty")
	fs.StringVar(&o.origin, "origin", "", "origin id for the manifest (default: derived from bucket, namespace, crew)")
	fs.BoolVar(&o.markdown, "markdown", false, "also write the runtime projection as Markdown files")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.namespace == "" || o.crew == "" || o.out == "" {
		return o, fmt.Errorf("--namespace, --crew and --out are required")
	}
	return o, nil
}

type opener func(options) (crewmemory.EntryReader, func(), error)

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
	exp, err := export(reader, scope, o, time.Now())
	if err != nil {
		return err
	}
	for _, s := range exp.Skipped {
		if _, err := fmt.Fprintf(stdout, "left out %q: %s\n", s.Key, s.Reason); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(stdout, "wrote %d memory object(s) of %s/%s to %s\n",
		len(exp.Memories), scope.Namespace, scope.Crew, o.out)
	return err
}

// export reads the crew's facts, maps them to memory objects, checks every
// document against the schema and writes the directory.
func export(reader crewmemory.EntryReader, scope crewscope.Scope, o options, now time.Time) (ompi.Export, error) {
	return ompi.ExportCrew(reader, scope, o.out, o.markdown, ompi.Options{ExportedAt: now, Origin: o.origin})
}

// connect opens the bucket for reading. It is not the operator's Publisher on
// purpose: the Publisher treats an unreachable server as an empty bucket, and an
// export that silently finds no facts is worse than one that fails.
func connect(o options) (crewmemory.EntryReader, func(), error) {
	var opts []nats.Option
	if o.creds != "" {
		opts = append(opts, nats.UserCredentials(o.creds))
	}
	nc, err := nats.Connect(o.natsURL, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("connect %s: %w", o.natsURL, err)
	}
	reader, err := natskv.Open(nc)
	if err != nil {
		nc.Close()
		return nil, nil, err
	}
	return reader, nc.Close, nil
}
