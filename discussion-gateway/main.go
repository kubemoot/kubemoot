package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/javajon/kubemoot/discussion-gateway/internal/api"
	natsclient "github.com/javajon/kubemoot/discussion-gateway/internal/nats"
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

	// NATS client (lazy connect)
	nc := natsclient.NewClient(natsURL)
	defer nc.Close()

	// HTTP server
	handler := api.NewHandler(nc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // SSE requires no write timeout
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("Starting discussion-gateway", "port", port, "nats", natsURL)
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
