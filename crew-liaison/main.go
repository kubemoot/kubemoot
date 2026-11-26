// The crew liaison: Kubemoot crews as single MCP agents. See internal/liaison.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/go-logr/logr"
	"github.com/kubemoot/kubemoot/crew-liaison/internal/accounts"
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
	gateway, err := crewGateway(os.Getenv)
	if err != nil {
		log.Error(err, "gateway TLS")
		os.Exit(1)
	}
	svc := liaison.NewService(liaison.NewLister(k8s), gateway, tickets, maxInflight)
	svc.WithDisplayName(os.Getenv("LIAISON_DISPLAY_NAME"))
	attachExporter(svc, log)

	state, err := openState(os.Getenv)
	if err != nil {
		log.Error(err, "state backend")
		os.Exit(1)
	}
	defer state.Close()

	acct, err := buildAccounts(os.Getenv, state, k8s, log.WithName("accounts"))
	if err != nil {
		log.Error(err, "connected accounts")
		os.Exit(1)
	}

	front, err := signInFront(log, state, acct)
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
	go sweep(ctx, tickets)
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

// The environment of the conversation exporter: the report service's URL, the install CA's
// public certificates that verify it (LIAISON_CA_FILE), and the opt-in to a URL that is not https.
const (
	envCAFile        = "LIAISON_CA_FILE"
	envReportURL     = "LIAISON_OPERATOR_REPORT_URL"
	envAllowInsecure = "LIAISON_ALLOW_INSECURE_REPORT"
)

// reportExporter builds the conversation exporter from the environment, or nil when none is
// configured. The call carries a bearer token, so an https URL is verified against the CA bundle
// in LIAISON_CA_FILE when it is set, and a URL that is not https is refused unless
// LIAISON_ALLOW_INSECURE_REPORT=true accepts it.
func reportExporter(getenv func(string) string) (*liaison.Exporter, error) {
	reportURL := getenv(envReportURL)
	if reportURL == "" {
		return nil, nil
	}
	if !strings.HasPrefix(strings.ToLower(reportURL), "https://") {
		if getenv(envAllowInsecure) != "true" {
			return nil, errors.New(envReportURL + " is not https and would carry a bearer token in the clear; " +
				"serve the report service over TLS, or set " + envAllowInsecure + "=true to accept it")
		}
		return liaison.NewExporter(reportURL), nil
	}
	if file := getenv(envCAFile); file != "" {
		return liaison.NewExporterWithCA(reportURL, file)
	}
	return liaison.NewExporter(reportURL), nil
}

// envAllowInsecureGateway lets the liaison call the crews' discussion gateways over plain http. The
// calls carry the asking person's bearer token, so without it the liaison uses https and
// verifies each gateway against LIAISON_CA_FILE; the chart sets it only when gateway TLS is off.
const envAllowInsecureGateway = "LIAISON_ALLOW_INSECURE_GATEWAY"

// crewGateway builds the client of the crews' discussion gateways.
func crewGateway(getenv func(string) string) (*liaison.Gateway, error) {
	if getenv(envAllowInsecureGateway) == "true" {
		return liaison.NewGateway(), nil
	}
	file := getenv(envCAFile)
	if file == "" {
		return nil, errors.New(envCAFile + " is required to call the crews' gateways over https; " +
			"set " + envAllowInsecureGateway + "=true to call them over plain http")
	}
	return liaison.NewGatewayWithCA(file)
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

// signInFront builds the OAuth sign-in from LIAISON_SIGNIN_* settings: the door's own identity
// when LIAISON_SIGNIN_IDENTITY is set, else the front that relays one service's sign-in when
// LIAISON_SIGNIN_UPSTREAM is set. It returns nil when neither is, which leaves the liaison
// without a sign-in.
func signInFront(log logr.Logger, state *tokenstore.Opener, acct *accounts.Service) (*liaison.SignInFront, error) {
	doorCfg, err := signin.DoorConfigFromEnv(os.Getenv, nil)
	if err != nil {
		return nil, err
	}
	if doorCfg != nil {
		doorCfg.Logger = log.WithName("signin")
		doorCfg.DisplayName = os.Getenv("LIAISON_DISPLAY_NAME")
		if err := shareReplay(doorCfg, state, log); err != nil {
			return nil, err
		}
		if acct != nil {
			doorCfg.Accounts = acct
		}
		return doorFront(doorCfg)
	}
	cfg, err := signin.ConfigFromEnv(os.Getenv, nil)
	if err != nil {
		return nil, err
	}
	if acct != nil {
		return nil, errAccountsNeedDoor
	}
	if cfg == nil {
		return nil, nil
	}
	cfg.Logger = log.WithName("signin")
	front, err := signin.New(cfg)
	if err != nil {
		return nil, err
	}
	return &liaison.SignInFront{
		Handler: front.Handler(), ResourceMetadataURL: signin.ResourceMetadataURL(cfg.PublicURL),
	}, nil
}

// attachExporter turns conversation export on when the report service is configured; a bad
// setting leaves export off and says why.
func attachExporter(svc *liaison.Service, log logr.Logger) {
	exporter, err := reportExporter(os.Getenv)
	if err != nil {
		log.Error(err, "conversation export is off")
		return
	}
	if exporter != nil {
		svc.WithExporter(exporter)
	}
}

// openState connects the backend that holds what the liaison must remember between requests.
func openState(getenv func(string) string) (*tokenstore.Opener, error) {
	cfg, err := tokenstore.ConfigFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return cfg.Open()
}

// shareReplay moves the door's single-use memory into the shared store when the install runs
// one, so every replica refuses a code or refresh token another replica already saw. With the
// memory backend the door keeps it in its own process.
func shareReplay(cfg *signin.DoorConfig, state *tokenstore.Opener, log logr.Logger) error {
	if state == nil || !state.Shared() {
		return nil
	}
	backend, err := state.Backend("door", cfg.ReplayLife())
	if err != nil {
		return err
	}
	sealer, err := signin.ParseKeys(cfg.SealKeys)
	if err != nil {
		return err
	}
	store, err := tokenstore.NewSealed(backend, sealer, cfg.ReplayLife())
	if err != nil {
		return err
	}
	cfg.Replay, err = signin.NewSharedReplay(store, log.WithName("replay"))
	return err
}

func doorFront(cfg *signin.DoorConfig) (*liaison.SignInFront, error) {
	door, err := signin.NewDoor(cfg)
	if err != nil {
		return nil, err
	}
	return &liaison.SignInFront{
		Handler: door.Handler(), ResourceMetadataURL: door.ResourceMetadataURL(),
		Verifier: door.Verifier(), Hop: door.HopSigner(),
	}, nil
}
