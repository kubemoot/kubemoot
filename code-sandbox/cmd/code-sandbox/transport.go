package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/code-sandbox/internal/server"
)

const (
	transportStdio = "stdio"
	transportHTTP  = "http"

	defaultPort     = 3000
	maxPort         = 65535
	shutdownTimeout = 5 * time.Second
	headerTimeout   = 10 * time.Second
)

// parseTransport validates the MCP_TRANSPORT value; empty means stdio.
func parseTransport(v string) (string, error) {
	switch v {
	case "", transportStdio:
		return transportStdio, nil
	case transportHTTP:
		return transportHTTP, nil
	}
	return "", fmt.Errorf("unknown MCP_TRANSPORT %q: use %q or %q", v, transportStdio, transportHTTP)
}

// parsePort validates the PORT value for http mode; empty means defaultPort.
func parsePort(v string) (int, error) {
	if v == "" {
		return defaultPort, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > maxPort {
		return 0, fmt.Errorf("invalid PORT %q: use a number from 1 to %d", v, maxPort)
	}
	return p, nil
}

// listenPort is the port to serve on: PORT in http mode, unused in stdio mode.
func listenPort(transport, v string) (int, error) {
	if transport != transportHTTP {
		return defaultPort, nil
	}
	return parsePort(v)
}

// serve runs the MCP server on the chosen transport until ctx ends.
func serve(ctx context.Context, transport string, s *server.Server, port int) error {
	switch transport {
	case transportHTTP:
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return err
		}
		return serveListener(ctx, s, ln)
	case transportStdio:
		srv, err := s.MCP()
		if err != nil {
			return err
		}
		return srv.Run(ctx, &mcp.StdioTransport{})
	}
	return fmt.Errorf("unknown transport %q", transport)
}

// serveListener serves HTTP on ln until ctx ends, then drains open requests.
func serveListener(ctx context.Context, s *server.Server, ln net.Listener) error {
	handler, err := s.HTTPHandler()
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: handler, ReadHeaderTimeout: headerTimeout}
	done := make(chan error, 1)
	go func() { done <- hs.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Runs still going when the drain time ends are cut: the pod is stopping.
	if err := hs.Shutdown(sctx); err != nil {
		_ = hs.Close()
		<-done
		return err
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
