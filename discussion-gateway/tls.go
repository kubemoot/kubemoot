package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
)

// The environment of the TLS listener. A crew's gateway carries callers' bearer tokens, so the
// operator mounts a serving certificate and sets both files; the gateway then serves https on
// TLS_PORT. The plain listener stays on PORT unless PLAIN_LISTENER is "false": a caller that
// cannot speak TLS yet (the Gateway API route, the API server's service proxy) needs it.
const (
	envTLSCertFile   = "TLS_CERT_FILE"
	envTLSKeyFile    = "TLS_KEY_FILE"
	envTLSPort       = "TLS_PORT"
	envPlainListener = "PLAIN_LISTENER"

	defaultTLSPort = "8443"
	// plainOff is the value of PLAIN_LISTENER that turns the plain listener off.
	plainOff = "false"
)

// tlsSettings is what the environment says about the listeners.
type tlsSettings struct {
	certFile, keyFile, port string
	plain                   bool
}

// tlsSettingsFromEnv reads the settings. A certificate without its key (or the reverse), or a
// plain listener switched off with no TLS to replace it, would leave the gateway unreachable or
// half configured, so each is an error and the gateway does not start.
func tlsSettingsFromEnv(getenv func(string) string) (tlsSettings, error) {
	t := tlsSettings{
		certFile: getenv(envTLSCertFile), keyFile: getenv(envTLSKeyFile), port: getenv(envTLSPort),
		plain: getenv(envPlainListener) != plainOff,
	}
	if t.port == "" {
		t.port = defaultTLSPort
	}
	if (t.certFile == "") != (t.keyFile == "") {
		return t, fmt.Errorf("%s and %s must be set together", envTLSCertFile, envTLSKeyFile)
	}
	if !t.plain && !t.enabled() {
		return t, fmt.Errorf("%s=false needs %s and %s: the gateway would serve nothing",
			envPlainListener, envTLSCertFile, envTLSKeyFile)
	}
	return t, nil
}

// enabled reports whether the TLS listener is to run.
func (t tlsSettings) enabled() bool { return t.certFile != "" }

// gatewayServer is one listener of the gateway.
type gatewayServer struct {
	name string
	srv  *http.Server
	tls  bool
}

// serve runs the listener until it stops; a failure other than a clean shutdown is returned.
func (g gatewayServer) serve() error {
	var err error
	if g.tls {
		err = g.srv.ListenAndServeTLS("", "")
	} else {
		err = g.srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// newHTTPServer is the gateway's server on addr. There is no write timeout: SSE streams run long.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadTimeout: 10 * time.Second, WriteTimeout: 0, IdleTimeout: 120 * time.Second,
	}
}

// buildServers returns the listeners the settings call for. The certificate is read again as it
// changes on disk, so a renewed one is served without a restart; the watcher runs until ctx ends.
func buildServers(
	ctx context.Context, t tlsSettings, port string, handler http.Handler, fatal func(error),
) ([]gatewayServer, error) {
	var servers []gatewayServer
	if t.plain {
		servers = append(servers, gatewayServer{name: "http", srv: newHTTPServer(":"+port, handler)})
	}
	if !t.enabled() {
		return servers, nil
	}
	watcher, err := certwatcher.New(t.certFile, t.keyFile)
	if err != nil {
		return nil, fmt.Errorf("the serving certificate cannot be read: %w", err)
	}
	go func() {
		// The watcher ends with ctx; any other end means a renewed certificate would never be
		// served, so the pod must restart rather than serve an expiring one.
		if err := watcher.Start(ctx); err != nil {
			fatal(fmt.Errorf("the certificate watcher stopped: %w", err))
		}
	}()
	secure := newHTTPServer(":"+t.port, handler)
	secure.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: watcher.GetCertificate}
	return append(servers, gatewayServer{name: "https", srv: secure, tls: true}), nil
}

// startServers reads the TLS settings, builds the listeners and starts each one. A bad setting or
// an unreadable certificate stops the gateway: a crew meant to serve TLS must not start without it.
func startServers(ctx context.Context, log logr.Logger, port string, handler http.Handler) []gatewayServer {
	settings, err := tlsSettingsFromEnv(os.Getenv)
	if err != nil {
		log.Error(err, "Cannot read the TLS settings")
		os.Exit(1)
	}
	servers, err := buildServers(ctx, settings, port, handler, func(err error) {
		log.Error(err, "Cannot keep the serving certificate current")
		os.Exit(1)
	})
	if err != nil {
		log.Error(err, "Cannot set up the listeners")
		os.Exit(1)
	}
	for _, g := range servers {
		go func() {
			log.Info("Starting discussion-gateway", "listener", g.name, "addr", g.srv.Addr)
			if err := g.serve(); err != nil {
				log.Error(err, "Server failed", "listener", g.name)
				os.Exit(1)
			}
		}()
	}
	return servers
}

// shutdownServers stops every listener at once, waiting for open requests until ctx ends, so a
// slow listener does not use up another's time to drain.
func shutdownServers(ctx context.Context, log logr.Logger, servers []gatewayServer) {
	var wg sync.WaitGroup
	for _, g := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.srv.Shutdown(ctx); err != nil {
				log.Error(err, "Shutdown error", "listener", g.name)
			}
		}()
	}
	wg.Wait()
}
