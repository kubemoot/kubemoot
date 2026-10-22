// Command harness-shim serves a command-line agent to the Moot Mesh sidecar as an A2A harness.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kubemoot/kubemoot/harness-shim/internal/shim"
)

func main() {
	cfgPath := flag.String("config", os.Getenv("HARNESS_SHIM_CONFIG"), "the shim's configuration file (JSON)")
	flag.Parse()
	if err := serve(*cfgPath); err != nil {
		slog.Error("harness-shim stopped", "error", err)
		os.Exit(1)
	}
}

func serve(cfgPath string) error {
	cfg, err := shim.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	if err := shim.BecomeSubreaper(); err != nil {
		slog.Warn("cannot become a subreaper; orphans are reaped only as PID 1", "error", err)
	}
	env := shim.EnvironmentFrom(os.Getenv)
	s := shim.NewServer(cfg, shim.NewRunner(cfg, env), "http://127.0.0.1:"+env.Port)
	srv := &http.Server{Addr: "127.0.0.1:" + env.Port, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	slog.Info("harness-shim listening", "addr", srv.Addr, "command", cfg.Argv[0])
	return srv.ListenAndServe()
}
