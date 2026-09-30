// Command code-sandbox is an MCP stdio server that runs short Python or bash
// programs for an agent. It is meant to run in a pod that is itself the sandbox:
// no credentials, no network except what the pod's policy allows, and a shared
// /artifacts directory holding the data the program reads.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/runner"
	"github.com/kubemoot/kubemoot/code-sandbox/internal/server"
)

const (
	defaultTimeoutSeconds = 30
	defaultMaxOutputBytes = 64 * 1024
)

func main() {
	log.SetOutput(os.Stderr) // stdout carries the MCP protocol
	r := &runner.Runner{
		Timeout:   time.Duration(envInt("EXECUTION_TIMEOUT", defaultTimeoutSeconds)) * time.Second,
		MaxOutput: envInt("MAX_OUTPUT_BYTES", defaultMaxOutputBytes),
		TempDir:   os.Getenv("SANDBOX_TMPDIR"),
	}
	srv, err := (&server.Server{Runner: r}).MCP()
	if err != nil {
		log.Fatalf("code-sandbox: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("code-sandbox up: languages=%v timeout=%s maxOutput=%d (stdio)", runner.Names(), r.Timeout, r.MaxOutput)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatalf("code-sandbox: %v", err)
	}
}

// envInt reads a positive integer from the environment, or returns def.
func envInt(name string, def int) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v <= 0 {
		return def
	}
	return v
}
