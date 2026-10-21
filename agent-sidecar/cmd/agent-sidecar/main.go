// Command agent-sidecar is the Moot Mesh sidecar of one agent pod (spike): it serves the model door and
// the tools door on loopback, and answers discussion turns by handing them to the harness over A2A.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bus"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/modeldoor"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/toolsdoor"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// contract is the spike's slice of contract.yaml, as JSON.
type contract struct {
	Namespace, Crew, Agent, Role string
	NATS                         string
	TurnURL                      string
	Placement                    string
	Gateway                      string
	EnabledTools                 []string
	DisabledTools                []string
	Aliases                      map[string][]string
	DeadlineSeconds              int
}

// readiness is state, not a timer: NATS connected and the harness's card answered.
type readiness struct{ nats, harness atomic.Bool }

func (r *readiness) ready() bool { return r.nats.Load() && r.harness.Load() }

func main() {
	path := flag.String("config", "contract.json", "contract file (JSON)")
	modelAddr := flag.String("model-addr", "127.0.0.1:8081", "model door")
	toolsAddr := flag.String("tools-addr", "127.0.0.1:8082", "tools door")
	healthAddr := flag.String("health-addr", "", "health and metrics listener (empty: off)")
	flag.Parse()
	c, err := load(*path)
	if err != nil {
		log.Fatal(err)
	}
	registry := turn.NewRegistry()
	rd := &readiness{}
	if *healthAddr != "" {
		serve(*healthAddr, healthMux(rd))
	}
	models := modeldoor.Config{Crew: c.Crew, Agent: c.Agent, Aliases: c.Aliases, Phase: "EVALUATING"}
	serve(*modelAddr, modeldoor.New(models, placerFor(c.Placement), registry))
	tools := toolsdoor.Config{Gateway: c.Gateway, Enabled: c.EnabledTools, Disabled: c.DisabledTools}
	serve(*toolsAddr, mcpMux(toolsdoor.New(tools, registry)))
	run(c, registry, rd)
}

// placerFor returns the bound placer for "bound:<endpoint>" and the placement service client otherwise.
func placerFor(spec string) placement.Placer {
	if endpoint, ok := strings.CutPrefix(spec, "bound:"); ok {
		return placement.Bound{Server: "bound", Endpoint: endpoint}
	}
	return placement.NewClient(spec)
}

func load(path string) (contract, error) {
	var c contract
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(data, &c)
}

func healthMux(rd *readiness) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !rd.ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func mcpMux(d *toolsdoor.Door) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", d.Handler())
	return mux
}

func serve(addr string, h http.Handler) {
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("door %s %s %s", addr, r.Method, r.URL.Path)
		h.ServeHTTP(w, r)
	})
	go func() {
		log.Fatal((&http.Server{Addr: addr, Handler: logged, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
	}()
}

func connectNATS(url string, rd *readiness) *nats.Conn {
	for {
		nc, err := nats.Connect(url, nats.MaxReconnects(-1),
			nats.DisconnectErrHandler(func(*nats.Conn, error) { rd.nats.Store(false) }),
			nats.ReconnectHandler(func(*nats.Conn) { rd.nats.Store(true) }))
		if err == nil {
			rd.nats.Store(true)
			return nc
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func discover(root string, rd *readiness) *a2a.Client {
	for {
		cl, _, err := a2a.Discover(context.Background(), root)
		if err == nil {
			rd.harness.Store(true)
			go watchCard(root, rd)
			return cl
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// watchCard keeps readiness true only while the harness answers its card, so a restarting harness takes
// the pod out of rotation without restarting the sidecar.
func watchCard(root string, rd *readiness) {
	for range time.Tick(2 * time.Second) {
		_, _, err := a2a.Discover(context.Background(), root)
		rd.harness.Store(err == nil)
	}
}

func run(c contract, registry *turn.Registry, rd *readiness) {
	nc := connectNATS(c.NATS, rd)
	client := discover(c.TurnURL, rd)
	turner := &a2a.Turner{Client: client, Agent: c.Agent, Crew: c.Crew, Role: c.Role, Phase: "EVALUATING",
		Models: map[string]any{"default": "default"}, DeadlineSeconds: c.DeadlineSeconds}
	r := &bus.Runner{Scope: bus.Scope{Namespace: c.Namespace, Crew: c.Crew}, Agent: c.Agent, Turner: turner,
		Registry: registry, Conn: nc, Deadline: time.Duration(c.DeadlineSeconds) * time.Second}
	if _, err := r.Start(); err != nil {
		log.Fatal(err)
	}
	log.Printf("agent-sidecar ready: agent=%s scope=%s.%s", c.Agent, c.Namespace, c.Crew)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
