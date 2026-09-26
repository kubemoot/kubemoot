package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/javajon/kubemoot/mcp-bridge/internal/bridge"
)

func main() {
	// Subcommand: exec mode — redirect stdio to pipes and exec a command
	// Used by main containers to connect to the bridge without requiring a shell
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		runExec(os.Args[2:])
		return
	}

	port := flag.Int("port", 8080, "HTTP port to listen on")
	healthz := flag.String("healthz", "/healthz", "Health check endpoint path")
	pipeDir := flag.String("pipe-dir", "/pipes", "Directory for named pipes (stdin/stdout FIFOs)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kubemoot-mcp-bridge [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Runs as a Kubernetes native sidecar, bridging MCP server stdio pipes to HTTP/SSE.\n")
		fmt.Fprintf(os.Stderr, "Creates named pipes (FIFOs) in --pipe-dir and waits for the MCP server to connect.\n\n")
		fmt.Fprintf(os.Stderr, "Subcommands:\n")
		fmt.Fprintf(os.Stderr, "  exec    Redirect stdio to pipes and exec a command (no shell required)\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down", sig)
		cancel()
	}()

	b := bridge.New(*pipeDir, *port, *healthz)
	if err := b.Run(ctx); err != nil {
		log.Fatalf("Bridge error: %v", err)
	}
}
