// The crew liaison: Kubemoot crews as single MCP agents. See internal/liaison.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/liaison"
)

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	log := ctrl.Log.WithName("main")

	port := envOr("PORT", "8080")
	token := os.Getenv("LIAISON_TOKEN")
	maxInflight := envInt("LIAISON_MAX_INFLIGHT", 4)
	ttl := time.Duration(envInt("LIAISON_TICKET_TTL_MINUTES", 60)) * time.Minute

	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		log.Error(err, "scheme")
		os.Exit(1)
	}
	k8s, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "kubernetes client")
		os.Exit(1)
	}

	tickets := liaison.NewStore(ttl)
	svc := liaison.NewService(liaison.NewLister(k8s), liaison.NewGateway(), tickets, maxInflight)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           liaison.NewHandler(svc, token),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sweep(ctx, tickets)
	go func() {
		log.Info("crew liaison listening",
			"port", port, "maxInflight", maxInflight, "auth", token != "", "version", liaison.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(err, "server")
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// sweep expires old tickets once a minute.
func sweep(ctx context.Context, tickets *liaison.Store) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickets.Sweep()
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}
