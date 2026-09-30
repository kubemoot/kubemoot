package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/api"
	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
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
	handler := api.NewHandler(nc, namespace).EndStreamsOn(ctx)
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

	fmt.Println("Stopped")
}
