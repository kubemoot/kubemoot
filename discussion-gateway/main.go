package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubemoot/kubemoot/discussion-gateway/internal/api"
	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	"github.com/kubemoot/kubemoot/discussion-gateway/internal/grants"
	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/httporigin"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	log := ctrl.Log.WithName("main")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	natsURL := os.Getenv("NATS_URL")

	// Every subject this gateway uses is scoped to its namespace; without one
	// it would publish unscoped subjects, so refuse to start.
	namespace, err := crewscope.NamespaceFromEnvironment()
	if err != nil {
		log.Error(err, "Cannot determine the namespace")
		os.Exit(1)
	}

	// Connect at startup; /ready reports whether the connection is up, so the Service
	// sends requests only to a gateway that can queue and stream them.
	nc := natsclient.NewClient(natsURL)
	if err := nc.Start(); err != nil {
		log.Error(err, "Cannot open the NATS connection", "nats", natsURL)
	}
	defer nc.Close()

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A shutdown ends open streams at once, so their clients reconnect to another
	// gateway and resume instead of waiting out the drain; questions still queue.
	// Browser pages may read a stream only from the origins listed here; empty sends no CORS headers.
	origins, rejected := httporigin.Parse(os.Getenv("ALLOWED_ORIGINS"))
	if len(rejected) > 0 {
		log.Info("Ignoring ALLOWED_ORIGINS entries that are not an exact origin", "entries", rejected)
	}
	settings, err := identity.FromEnv(os.Getenv, kubernetesClient)
	if err != nil {
		log.Error(err, "Cannot set up caller verification; refusing to serve open")
		os.Exit(1)
	}
	handler := api.NewHandler(nc, namespace).AllowOrigins(origins).EndStreamsOn(ctx).
		WithAccess(settings.Authenticator, settings.Authorizer)
	leaseSrv := startCredentials(ctx, handler, log)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// HTTP server

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // SSE requires no write timeout
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Info("Starting discussion-gateway", "port", port, "nats", natsURL, "namespace", namespace)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(err, "Server failed")
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("Shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error(err, "Shutdown error")
	}
	shutdownLease(shutdownCtx, leaseSrv, log)

	fmt.Println("Stopped")
}

// kubernetesClient is the in-cluster client the gateway checks tokens with.
func kubernetesClient() (kubernetes.Interface, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// startCredentials turns on keeping callers' tokens for the turn when a verifier passes them
// to tools, and starts the lease listener when the ServiceAccount allowed to lease is named.
// It returns the lease server, nil when there is none. A bad setting stops the gateway: a
// crew that was meant to pass tokens must not serve as if it did not.
func startCredentials(ctx context.Context, handler *api.Handler, log logr.Logger) *http.Server {
	cfg, err := credentialConfigFromEnv(os.Getenv)
	if err != nil {
		log.Error(err, "Cannot read the credential settings")
		os.Exit(1)
	}
	if !cfg.keepsTokens() {
		return nil
	}
	store := grants.NewMemory(cfg.maxGrants, nil)
	go store.Run(ctx.Done(), sweepInterval)
	handler.WithGrants(store, cfg.issuers)
	if !cfg.leases() {
		log.Info("Keeping callers' tokens for the turn, with no lease listener: " + envBrokerSubject + " is not set")
		return nil
	}
	client, err := leaseClient()
	if err != nil {
		log.Error(err, "Cannot set up the lease listener")
		os.Exit(1)
	}
	srv := cfg.leaseServer(store, client)
	go func() {
		log.Info("Starting the credential lease listener", "port", cfg.internalPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(err, "Lease listener failed")
			os.Exit(1)
		}
	}()
	return srv
}

// shutdownLease stops the lease listener, when there is one.
func shutdownLease(ctx context.Context, srv *http.Server, log logr.Logger) {
	if srv == nil {
		return
	}
	if err := srv.Shutdown(ctx); err != nil {
		log.Error(err, "Lease listener shutdown error")
	}
}
