/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command accelerator-mcp is the stdio MCP server the scheduling crew uses to see
// and measure the cluster's model providers: list them, read one's live state,
// probe a model on one, and record a calibration on its status.
//
// Configuration via env vars:
//
//	NATS_URL   NATS server (default nats://nats.nats.svc.cluster.local:4222)
//
// The Kubernetes client uses the in-cluster service account (or KUBECONFIG). It
// needs get and list on modelproviders and models, and patch on modelproviders/status.
//
// Reads JSON-RPC messages line by line on stdin and writes responses on stdout.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/cluster"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/probe"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/server"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/store"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/tools"
)

// Version is stamped at build time from the commit; "dev" in a local build.
var Version = "dev"

const (
	protocolVersion = "2024-11-05"
	serverName      = "kubemoot-accelerator-mcp"
	// holderPrefix starts the ticket holder and history agent the probes write as;
	// the pod name follows, so replicas see each other's tickets as other calls and
	// each writes its own history key.
	holderPrefix = "kubemoot/accelerator-mcp/"
	// idleUnload is how long a model the probes loaded may sit unprobed before it is unloaded.
	idleUnload = 10 * time.Minute
)

func main() {
	log.SetOutput(os.Stderr) // stdout carries the MCP protocol
	natsURL := getenv("NATS_URL", "nats://nats.nats.svc.cluster.local:4222")

	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		log.Fatalf("scheme: %v", err)
	}
	k8s, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}
	nc, err := nats.Connect(natsURL, nats.MaxReconnects(-1), nats.ReconnectWait(2*time.Second),
		nats.Name(serverName))
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}
	defer func() { _ = nc.Drain() }()
	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("jetstream: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	set := newToolSet(ctx, k8s, store.NATS{JS: js}, holderID(), engine.NATSLifecycle{Conn: nc, Agent: serverName})
	if err := set.RecoverInterrupted(ctx); err != nil {
		log.Printf("recording interrupted calibrations: %v", err)
	}
	go unloadIdle(ctx, set.Prober.(*probe.Prober))

	log.Printf("accelerator-mcp %s starting: nats=%s", Version, natsURL)
	srv := &server.Server{Handlers: set, ProtocolVersion: protocolVersion, ServerName: serverName,
		ServerVersion: Version}
	if err := srv.Run(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		log.Fatalf("server: %v", err)
	}
}

// holderID names this server instance: the prefix and the pod's host name.
func holderID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "local"
	}
	return holderPrefix + host
}

// newToolSet builds the tool set; calibrations it starts run under ctx.
func newToolSet(
	ctx context.Context, k8s client.Client, kv store.KV, holder string, lifecycle engine.Lifecycle,
) *tools.Set {
	httpClient := &http.Client{Timeout: 10 * time.Minute}
	engineFor := func(endpoint string) engine.Engine {
		return engine.OllamaEngine{Endpoint: endpoint, Client: httpClient}
	}
	tickets := store.Tickets{KV: kv, Holder: holder, Now: time.Now}
	history := store.History{KV: kv, Agent: holder, Now: time.Now}
	return &tools.Set{
		Cluster: cluster.Cluster{Client: k8s},
		Prober: &probe.Prober{Engine: engineFor, For: vllmFor(httpClient, lifecycle), Tickets: tickets, History: history,
			Prompts: probe.DefaultPrompts(), Now: time.Now},
		Tickets: tickets,
		History: history,
		Engine:  engineFor,
		Now:     time.Now,
		Jobs:    &tools.Jobs{Base: ctx},
	}
}

// vllmFor is the engine client for a vLLM probe: the server of the probed model, woken
// and put to sleep through the operator, never directly.
func vllmFor(httpClient *http.Client, lifecycle engine.Lifecycle) func(probe.Request) engine.Engine {
	return func(req probe.Request) engine.Engine {
		return engine.VLLMEngine{Endpoint: req.Endpoint, Provider: req.Provider, Model: req.Model,
			Client: httpClient, Lifecycle: lifecycle}
	}
}

// unloadIdle is the safety net for an agent that never finishes a calibration.
func unloadIdle(ctx context.Context, p *probe.Prober) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.FinishIdle(ctx, idleUnload)
		}
	}
}

func getenv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
