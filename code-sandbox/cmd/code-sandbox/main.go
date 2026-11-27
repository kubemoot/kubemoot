// Command code-sandbox is an MCP server, over stdio or Streamable HTTP, that
// runs short Python or bash programs for an agent. It is meant to run in a pod
// that is itself the sandbox: no credentials, no network except what the pod's
// policy allows, and a shared /kubemoot/workspace directory holding the data the program
// reads.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/confine"
	"github.com/kubemoot/kubemoot/code-sandbox/internal/inputs"
	"github.com/kubemoot/kubemoot/code-sandbox/internal/runner"
	"github.com/kubemoot/kubemoot/code-sandbox/internal/server"
)

const (
	defaultTimeoutSeconds = 30
	defaultMaxOutputBytes = 64 * 1024
)

// fatal logs err with the program prefix and exits.
func fatal(err error) {
	log.Fatalf("code-sandbox: %v", err)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == confine.Arg {
		confine.Main(os.Args[2:]) // a program's confining helper, not the server
	}
	log.SetOutput(os.Stderr) // in stdio mode stdout carries the MCP protocol
	transport, err := parseTransport(os.Getenv("MCP_TRANSPORT"))
	if err != nil {
		fatal(err)
	}
	port, err := listenPort(transport, os.Getenv("PORT"))
	if err != nil {
		fatal(err)
	}
	r := &runner.Runner{
		Timeout:   time.Duration(envInt("EXECUTION_TIMEOUT", defaultTimeoutSeconds)) * time.Second,
		MaxOutput: envInt("MAX_OUTPUT_BYTES", defaultMaxOutputBytes),
		TempDir:   os.Getenv("SANDBOX_TMPDIR"),

		WorkspaceDirs: workspaceDirs(),
		Unconfined:    os.Getenv("SANDBOX_CONFINEMENT") == confinementOff,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if !r.Unconfined {
		if extra := confine.UnlistedWorkspaceMountsOfHost(r.WorkspaceDirs); len(extra) > 0 {
			fatal(fmt.Errorf("a workspace volume is mounted at %v, which a program would see; "+
				"name it in WORKSPACE_DIR or SANDBOX_WORKSPACE_DIRS (or set SANDBOX_CONFINEMENT=off)", extra))
		}
	}
	log.Printf("code-sandbox up: languages=%v timeout=%s maxOutput=%d transport=%s port=%d workspace=%v confined=%t",
		runner.Names(), r.Timeout, r.MaxOutput, transport, port, r.WorkspaceDirs, !r.Unconfined)
	srv := &server.Server{Runner: r, Inputs: inputs.Resolver{Roots: r.WorkspaceDirs}}
	if err := serve(ctx, transport, srv, port); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

const (
	defaultWorkspaceDir = "/kubemoot/workspace"
	// legacyWorkspaceDir is the old mount name, which a crew may still mount beside the new one.
	legacyWorkspaceDir = "/artifacts"
	confinementOff     = "off"
)

// workspaceDirs are the paths the workspace volume may be mounted at: the one workspace-access
// stages into (WORKSPACE_DIR, or its old name ARTIFACT_DIR), the old /artifacts, and any named in
// SANDBOX_WORKSPACE_DIRS (comma separated).
func workspaceDirs() []string {
	dirs := baseWorkspaceDirs()
	for _, extra := range strings.Split(os.Getenv("SANDBOX_WORKSPACE_DIRS"), ",") {
		if extra = strings.TrimSpace(extra); extra != "" {
			dirs = append(dirs, extra)
		}
	}
	return dirs
}

func baseWorkspaceDirs() []string {
	dir := os.Getenv("WORKSPACE_DIR")
	if dir == "" {
		dir = os.Getenv("ARTIFACT_DIR")
	}
	if dir == "" {
		dir = defaultWorkspaceDir
	}
	if dir == legacyWorkspaceDir {
		return []string{dir}
	}
	return []string{dir, legacyWorkspaceDir}
}

// envInt reads a positive integer from the environment, or returns def.
func envInt(name string, def int) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v <= 0 {
		return def
	}
	return v
}
