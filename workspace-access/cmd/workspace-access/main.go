// Command workspace-access is the Kubemoot collaborative-artifact data-access
// component. It has two modes, selected by WORKSPACE_MODE (one binary, two deployments; ARTIFACT_MODE
// still works for one release):
//
//   - materializer (default): the no-network sandbox's sidecar. It subscribes to
//     its namespace's artifact subject and streams each referenced NATS Object Store blob to
//     a local directory the sandbox reads, so bulk data never crosses the discussion
//     bus or enters an LLM context.
//   - mcp: a shared read-ops MCP stdio service. Network-capable agents call its tools
//     (head/tail/grep/select/count/rows/jq) to sip a slice of a referenced artifact
//     near the data, so "sip, don't slurp" is the default for non-code agents too.
//
// In mcp mode stdin/stdout carry the MCP protocol; all logs go to stderr.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/workspace-access/internal/artifact"
	"github.com/kubemoot/kubemoot/workspace-access/internal/envcompat"
	"github.com/kubemoot/kubemoot/workspace-access/internal/materializer"
	"github.com/kubemoot/kubemoot/workspace-access/internal/mcpserver"
	"github.com/kubemoot/kubemoot/workspace-access/internal/natsstore"
	"github.com/kubemoot/kubemoot/workspace-access/internal/scope"
	"github.com/kubemoot/kubemoot/workspace-access/internal/workspace"
)

// getenv reads a WORKSPACE_* variable and falls back to its ARTIFACT_* name for one release.
var getenv = envcompat.Wrap(os.Getenv, log.Printf)

func env(key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		mode    = env("WORKSPACE_MODE", "materializer")
		natsURL = env("NATS_URL", nats.DefaultURL)
		buckets = envcompat.Buckets(os.Getenv, log.Printf)
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The store reads on a fresh connection per Get (see natsstore); only the
	// materializer's reference subscription needs a persistent connection.
	store := natsstore.Open(natsURL, buckets...)

	switch mode {
	case "materializer":
		runMaterializer(ctx, natsURL, store)
	case "mcp":
		runMCP(ctx, store)
	default:
		log.Fatalf("unknown WORKSPACE_MODE %q (want materializer|mcp)", mode)
	}
}

// runMaterializer streams referenced objects to a shared local directory for the
// no-network sandbox, until a termination signal arrives.
func runMaterializer(ctx context.Context, natsURL string, store *natsstore.Store) {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Producers publish a reference envelope per crew on
	// kubemoot.artifacts.<ns>.<crew>.<thread>; the default follows every crew
	// and thread of this pod's namespace and no other namespace.
	subject, err := scope.MaterializerSubjectFromEnvironment(getenv)
	if err != nil {
		log.Fatalf("artifact subject: %v", err)
	}

	nc, err := nats.Connect(natsURL,
		nats.Name("workspace-access-sub"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		log.Fatalf("connect NATS %s: %v", natsURL, err)
	}
	defer func() { _ = nc.Drain() }()

	dir := env("WORKSPACE_DIR", "/artifacts")

	// 0o755 so the non-root sandbox container sharing this volume can traverse it.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("artifact dir %s: %v", dir, err)
	}
	mat := &materializer.Materializer{Store: store, BaseDir: dir}

	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		ref, err := artifact.ParseMessage(string(msg.Data))
		if err != nil {
			// Not a reference (or malformed) - this subject should only carry
			// references, but be tolerant and ignore anything else.
			return
		}
		path, err := mat.Materialize(ctx, ref)
		if err != nil {
			log.Printf("materialize key=%s: %v", ref.Key, err)
			return
		}
		log.Printf("materialized key=%s bytes=%d -> %s", ref.Key, ref.Bytes, path)
	})
	if err != nil {
		log.Fatalf("subscribe %s: %v", subject, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	log.Printf("workspace-access[materializer] up: buckets=%v dir=%s subject=%s", store.Buckets(), dir, subject)
	<-ctx.Done()
	log.Print("shutting down")
}

// runMCP serves the bounded read-ops over MCP stdio until stdin closes or a signal
// arrives. stdin/stdout are the protocol; logs stay on stderr.
func runMCP(ctx context.Context, store *natsstore.Store) {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := mcpserver.New(store)
	// workspace_query is offered only when WORKSPACE_QUERY_ENGINE asks for an engine;
	// an engine that cannot start leaves the other tools working and says why.
	switch q, err := workspace.FromEnv(getenv, store, env("WORKSPACE_DIR", "/artifacts")); {
	case err != nil:
		log.Printf("workspace_query is not offered: %v", err)
	case q != nil:
		srv.Query = q
		log.Printf("workspace_query offered: engine=%s", env(workspace.EnvEngine, workspace.EngineNone))
	}

	log.Printf("workspace-access[mcp] up: buckets=%v (stdio)", store.Buckets())
	if err := srv.MCP().Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp server: %v", err)
	}
	log.Print("shutting down")
}
