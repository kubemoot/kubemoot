package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bus"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/modeldoor"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/toolsdoor"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// Options are what a deployment does not put in the contract: where to listen.
type Options struct {
	// HTTPAddr is the pod-facing listener (health, metrics, chat ingress); the model and tools
	// doors listen on loopback only. Empty values take the contract's ports.
	HTTPAddr, ModelAddr, ToolsAddr string
	Log                            *slog.Logger
	// ObserveEvery is how often readiness is read from the harness's card and the connection.
	ObserveEvery time.Duration
	// HeartbeatEvery is the liveness entry interval.
	HeartbeatEvery time.Duration
}

// App is a running sidecar.
type App struct {
	c    mesh.Contract
	o    Options
	log  *slog.Logger
	met  *obs.Metrics
	reg  *turn.Registry
	tp   func(context.Context) error
	conn atomic.Pointer[nats.Conn]

	placer  placement.Placer
	aliases map[string][]string

	natsUp, harnessUp atomic.Bool
	failed            chan struct{}
	failOnce          sync.Once
	failErr           atomic.Pointer[error]
	servers           []*http.Server
	addrs             Addrs
	cancel            context.CancelFunc
	wg                sync.WaitGroup
}

// Addrs are the addresses the listeners bound.
type Addrs struct{ HTTP, Model, Tools string }

// New returns an app for the contract.
func New(c mesh.Contract, o Options) *App {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.ObserveEvery <= 0 {
		o.ObserveEvery = 2 * time.Second
	}
	if o.HeartbeatEvery <= 0 {
		o.HeartbeatEvery = 30 * time.Second
	}
	return &App{c: c, o: o, log: o.Log, reg: turn.NewRegistry(), failed: make(chan struct{}),
		met: obs.New(obs.Labels{Namespace: c.Identity.Namespace, Crew: c.Identity.Crew, Agent: c.Identity.Agent},
			c.Placement.Policy)}
}

// Failed is closed when the sidecar cannot do its job after it started (NATS cannot be reached, or the bus side
// cannot start). The pod is then not ready, and the caller should exit so the kubelet restarts the container.
func (a *App) Failed() <-chan struct{} { return a.failed }

// Err is why the sidecar failed, once Failed is closed.
func (a *App) Err() error {
	if e := a.failErr.Load(); e != nil {
		return *e
	}
	return nil
}

// fatal records that the sidecar cannot serve and takes the pod out of rotation.
func (a *App) fatal(err error) {
	a.log.Error("the sidecar cannot serve", "error", err)
	a.natsUp.Store(false)
	a.harnessUp.Store(false)
	a.failOnce.Do(func() {
		a.failErr.Store(&err)
		close(a.failed)
	})
}

// Addrs returns the bound listener addresses; valid after Start.
func (a *App) Addrs() Addrs { return a.addrs }

// Ready is the pod's readiness: connected to NATS and the harness's card answering. It is state,
// read from the connection and the card; no timer gates it.
func (a *App) Ready() bool { return a.natsUp.Load() && a.harnessUp.Load() }

// Start binds the listeners, sets up tracing, connects to NATS, finds the harness and starts the
// bus side. It returns once the doors are listening; connection and discovery continue in the
// background until ctx ends, so the sidecar's own startup probe never waits on the harness.
func (a *App) Start(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)
	tp, err := obs.Setup(ctx, obs.Config{Endpoint: observed(a.c), Attributes: map[string]string{
		"namespace": a.c.Identity.Namespace, "crew": a.c.Identity.Crew, "agent": a.c.Identity.Agent,
		"harness": a.c.Identity.Harness}})
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	a.tp = tp
	if a.placer, a.aliases, err = a.buildPlacer(); err != nil {
		return err
	}
	modelDoor, toolsDoor := a.doors()
	if err := a.listen(modelDoor, toolsDoor); err != nil {
		return err
	}
	a.goRun(func() { a.connectAndRun(ctx, toolsDoor) })
	return nil
}

func observed(c mesh.Contract) string {
	if c.Observability == nil {
		return ""
	}
	return c.Observability.OTLPEndpoint
}

// Stop ends the sidecar: it stops the bus side, the listeners and the tracer.
func (a *App) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	for _, s := range a.servers {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.Shutdown(ctx)
		cancel()
	}
	a.wg.Wait()
	if c := a.conn.Load(); c != nil {
		c.Close()
	}
	if a.tp != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.tp(ctx)
		cancel()
	}
}

func (a *App) goRun(f func()) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		f()
	}()
}

// doors builds the model door and the tools door from the contract. Both serve open turns only.
func (a *App) doors() (*modeldoor.Door, *toolsdoor.Door) {
	m := modeldoor.New(modeldoor.Config{Namespace: a.c.Identity.Namespace, Crew: a.c.Identity.Crew,
		Agent: a.c.Identity.Agent, Aliases: a.aliases, Phase: a2a.PhaseEvaluating, RequireTurn: true},
		a.placer, a.reg, a.met)
	t := toolsdoor.New(toolsdoor.Config{Gateway: a.c.Tools.Gateway, Enabled: a.c.Tools.Enabled,
		Disabled: a.c.Tools.Disabled, RequireTurn: true}, a.reg, a.met)
	return m, t
}

// buildPlacer chooses the placement path: the service with the operator-bound server as the floor, or the
// bound server alone when the contract names no service.
func (a *App) buildPlacer() (placement.Placer, map[string][]string, error) {
	bound := placement.Bound{Default: mesh.DefaultAlias, Aliases: map[string]placement.Endpoint{}}
	aliases := map[string][]string{}
	for name, al := range a.c.Models.Aliases {
		aliases[name] = al.Candidates
		if f := al.Fallback; f != nil {
			bound.Aliases[name] = placement.Endpoint{Server: f.Server, Endpoint: f.Endpoint, Model: f.Model}
		}
	}
	if a.c.Placement.ServiceURL == "" {
		if len(bound.Aliases) == 0 {
			return nil, nil, errors.New("the contract names neither a placement service nor a bound server")
		}
		return bound, aliases, nil
	}
	return placement.Fallback{Primary: placement.NewClient(a.c.Placement.ServiceURL), Bound: bound}, aliases, nil
}

// listen binds the three listeners and serves them.
func (a *App) listen(m *modeldoor.Door, t *toolsdoor.Door) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", a.readyz)
	mux.Handle("/metrics", a.met.Handler())
	mux.HandleFunc("/api/chat", chatNotServed)
	tools := http.NewServeMux()
	tools.Handle("/mcp", t.Handler())
	httpAddr := a.addrOr(a.o.HTTPAddr, fmt.Sprintf(":%d", mesh.DefaultHTTPPort))
	modelAddr := a.addrOr(a.o.ModelAddr, mesh.ModelDoorAddr)
	toolsAddr := a.addrOr(a.o.ToolsAddr, mesh.ToolsDoorAddr)
	var err error
	if a.addrs.HTTP, err = a.serve(httpAddr, mux, "http"); err != nil {
		return err
	}
	if a.addrs.Model, err = a.serve(modelAddr, obs.Handler(m, "model-door"), "model"); err != nil {
		return err
	}
	a.addrs.Tools, err = a.serve(toolsAddr, obs.Handler(tools, "tools-door"), "tools")
	return err
}

func (a *App) addrOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func (a *App) serve(addr string, h http.Handler, name string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("%s listener: %w", name, err)
	}
	s := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	a.servers = append(a.servers, s)
	a.goRun(func() {
		if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Error("listener stopped", "listener", name, "error", err)
		}
	})
	return ln.Addr().String(), nil
}

func (a *App) readyz(w http.ResponseWriter, _ *http.Request) {
	if !a.Ready() {
		http.Error(w, fmt.Sprintf("not ready: nats=%t harness=%t", a.natsUp.Load(), a.harnessUp.Load()),
			http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// chatNotServed answers direct chat until the sidecar serves it; the Service points at this port.
func chatNotServed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"error":"direct chat is not served by the Moot Mesh sidecar yet; ` +
		`the agent answers discussion turns only"}`))
}

// connectAndRun connects to NATS, finds the harness, starts the bus side and then follows readiness
// until ctx ends.
func (a *App) connectAndRun(ctx context.Context, tools *toolsdoor.Door) {
	conn, err := a.connect(ctx)
	if err != nil {
		if ctx.Err() == nil {
			a.fatal(fmt.Errorf("no NATS connection: %w", err))
		}
		return
	}
	a.conn.Store(conn)
	hc := &http.Client{Transport: obs.Transport(nil, "harness")}
	harness := a.discover(ctx, hc)
	if harness == nil {
		return
	}
	a.goRun(func() { a.observe(ctx, hc) })
	pub, run, err := a.runner(conn, harness, tools)
	if err != nil {
		a.fatal(fmt.Errorf("the bus side did not start: %w", err))
		return
	}
	defer run.Wait()
	a.goRun(func() { a.holdSubscription(ctx, run) })
	life := &bus.Lifecycle{Pub: pub, Ready: a.Ready, NATS: a.natsUp.Load, Harness: a.harnessUp.Load,
		LastInference: a.reg.LastCall, Poll: 500 * time.Millisecond, Beat: a.o.HeartbeatEvery, Log: a.log}
	life.Run(ctx)
}

// holdSubscription subscribes to the crew's discussion and keeps the subscription until ctx ends. A
// consumer still bound by the pod being replaced is waited for, never a failure: readiness does not
// depend on the subscription, so the new pod becomes ready, the old one is removed, and this binds.
func (a *App) holdSubscription(ctx context.Context, run *bus.Runner) {
	sub := run.SubscribeWhenFree(ctx)
	if sub == nil {
		return
	}
	<-ctx.Done()
	_ = sub.Unsubscribe()
}

// connect keeps trying until NATS answers; the connection state feeds readiness.
func (a *App) connect(ctx context.Context) (*nats.Conn, error) {
	creds, err := connectOptions(a.c.Bus.CredentialsDir)
	if err != nil {
		return nil, err
	}
	opts := append(creds, nats.MaxReconnects(-1), nats.Name("kubemoot-sidecar-"+a.c.Identity.Agent),
		nats.DisconnectErrHandler(func(*nats.Conn, error) { a.natsUp.Store(false) }),
		nats.ReconnectHandler(func(*nats.Conn) { a.natsUp.Store(true) }))
	for {
		conn, err := nats.Connect(a.c.Bus.NATSURL, opts...)
		if err == nil {
			a.natsUp.Store(true)
			return conn, nil
		}
		a.log.Warn("NATS not reachable yet", "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// discover finds the harness's card, retrying until it answers.
func (a *App) discover(ctx context.Context, hc *http.Client) *a2a.Client {
	for {
		c, err := a2a.Discover(ctx, a.c.Turn.URL, hc)
		if err == nil {
			a.harnessUp.Store(true)
			return c
		}
		if errors.Is(err, a2a.ErrContractMismatch) {
			a.log.Error("the harness speaks another turn profile; the agent will not join the table", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// observe keeps readiness true only while the harness answers its card, so a restarting harness takes
// the pod out of rotation without restarting the sidecar.
func (a *App) observe(ctx context.Context, hc *http.Client) {
	t := time.NewTicker(a.o.ObserveEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			a.harnessUp.Store(a2a.Alive(cctx, a.c.Turn.URL, hc))
			cancel()
		}
	}
}

// runner builds the publisher and the bus runner.
func (a *App) runner(conn *nats.Conn, harness *a2a.Client, tools *toolsdoor.Door) (*bus.Publisher, *bus.Runner, error) {
	js, err := conn.JetStream()
	if err != nil {
		return nil, nil, err
	}
	pol := mesh.Policy{SystemPromptFile: mesh.PolicyDir + "/" + mesh.SystemPromptFile, SkillsDir: mesh.SkillsDir}
	if a.c.Policy != nil {
		pol = *a.c.Policy
	}
	def := a.c.Models.Aliases[mesh.DefaultAlias].Candidates
	pub := &bus.Publisher{Conn: conn, JS: js, ID: a.c.Identity, Role: a.c.Identity.Role, Harness: a.c.Identity.Harness,
		Model: first(def), EnabledTools: a.c.Tools.Enabled, Metrics: a.met}
	turnAliases := map[string]any{}
	for name := range a.c.Models.Aliases {
		turnAliases[name] = name
	}
	planner, _ := a.placer.(placement.Planner)
	run := &bus.Runner{ID: a.c.Identity, Bus: a.c.Bus, Turn: a.c.Turn, Conn: conn, JS: js, Harness: harness,
		Registry: a.reg, Pub: pub, Planner: planner, Tools: tools, Metrics: a.met, Log: a.log, Aliases: a.aliases,
		ModelAliases: turnAliases, Instructions: bus.LoadInstructions(pol.SystemPromptFile, pol.SkillsDir)}
	return pub, run, nil
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
