// Command agent-sidecar is the Moot Mesh sidecar of one agent pod. It reads contract.yaml, serves the
// model door and the tools door to the agent's harness on loopback, takes the agent's turns from
// the crew's bus, hands each to the harness over A2A, and publishes the signals.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/app"
)

func main() {
	path := flag.String("config", mesh.ContractDir+"/"+mesh.ContractFile, "contract.yaml")
	httpAddr := flag.String("http-addr", "", "pod-facing listener: health, metrics (default :8080)")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	data, err := os.ReadFile(*path)
	if err != nil {
		log.Error("contract not readable", "path", *path, "error", err)
		os.Exit(1)
	}
	contract, err := mesh.Parse(data)
	if err != nil {
		log.Error("contract refused", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := app.New(contract, app.Options{HTTPAddr: *httpAddr, Log: log})
	if err := a.Start(ctx); err != nil {
		log.Error("sidecar did not start", "error", err)
		os.Exit(1)
	}
	log.Info("agent-sidecar listening", "agent", contract.Identity.Agent, "crew", contract.Identity.Crew,
		"harness", contract.Identity.Harness)
	select {
	case <-ctx.Done():
	case <-a.Failed():
		log.Error("exiting so the container is restarted", "error", a.Err())
		a.Stop()
		os.Exit(1)
	}
	a.Stop()
}
