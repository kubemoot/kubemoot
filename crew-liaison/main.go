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
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/go-logr/logr"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/liaison"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/signin"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
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

	auth, err := callerAuthenticator()
	if err != nil {
		log.Error(err, "caller verification")
		os.Exit(1)
	}

	tickets := liaison.NewStore(ttl)
	svc := liaison.NewService(liaison.NewLister(k8s), liaison.NewGateway(), tickets, maxInflight)
	if reportURL := os.Getenv("LIAISON_OPERATOR_REPORT_URL"); reportURL != "" {
		svc.WithExporter(liaison.NewExporter(reportURL))
	}

	kept := tokenstore.NewMemory(nil)
	front, err := signInFront(log, auth, kept)
	if err != nil {
		log.Error(err, "sign-in front")
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           liaison.NewHandlerWithFront(svc, token, auth, front),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go everyMinute(ctx, func() { tickets.Sweep() })
	go everyMinute(ctx, kept.Sweep)
	go func() {
		log.Info("crew liaison listening",
			"port", port, "maxInflight", maxInflight, "auth", token != "",
			"kubernetesTokens", auth.Enforcing(), "signIn", front != nil,
			"version", liaison.Version)
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

// everyMinute runs sweep once a minute until ctx is done: old tickets, and kept values
// whose time is over, leave memory even when nobody asks for them again.
func everyMinute(ctx context.Context, sweep func()) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
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

// callerAuthenticator reads the caller settings from the same ACCESS_* environment the
// discussion gateway reads: verifying Kubernetes tokens when ACCESS_TOKEN_REVIEW is "true".
func callerAuthenticator() (*identity.Authenticator, error) {
	settings, err := identity.FromEnv(os.Getenv, func() (kubernetes.Interface, error) {
		cfg, err := ctrl.GetConfig()
		if err != nil {
			return nil, err
		}
		return kubernetes.NewForConfig(cfg)
	})
	return settings.Authenticator, err
}

// errSignInNeedsVerifier stops a liaison whose sign-in could not say who signed in.
var errSignInNeedsVerifier = errors.New(
	"sign-in is enabled but no verifier names the person: set ACCESS_SERVICE_TOKENS (liaison.auth.serviceTokens)")

// signInFront builds the OAuth sign-in front from LIAISON_SIGNIN_* settings. It returns nil
// when LIAISON_SIGNIN_UPSTREAM is not set, which leaves the liaison without a sign-in. The
// front names the person who signed in with the door's own verifiers, so it needs them,
// and keeps each person's token from the upstream in store.
func signInFront(log logr.Logger, auth *identity.Authenticator, store tokenstore.Store) (*liaison.SignInFront, error) {
	cfg, err := signin.ConfigFromEnv(os.Getenv, nil)
	if err != nil || cfg == nil {
		return nil, err
	}
	if auth == nil || !auth.Enforcing() {
		return nil, errSignInNeedsVerifier
	}
	cfg.Logger = log.WithName("signin")
	cfg.Identify = liaison.PersonFrom(auth)
	cfg.Store = store
	front, err := signin.New(cfg)
	if err != nil {
		return nil, err
	}
	return &liaison.SignInFront{
		Handler: front.Handler(), ResourceMetadataURL: signin.ResourceMetadataURL(cfg.PublicURL),
		Tokens: front.Resolve,
	}, nil
}
