package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/app"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/bustest"
)

const (
	agent       = "notes-reader"
	general     = "general"
	modelName   = "qwen3:14b"
	ephemeral   = "127.0.0.1:0"
	toolFetch   = "fetch"
	signalAgree = "agree"
)

var identity = mesh.Identity{Namespace: "homelab", Crew: "pilot", Agent: agent, Role: "tooler",
	Channels: []string{general}, Harness: "e2e-harness"}

// modelServer is an OpenAI-compatible server that reports usage.
func modelServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"two nodes"}}],"usage":{"prompt_tokens":50,"completion_tokens":7}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

// toolGateway lists one tool and answers its calls.
func toolGateway(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tools": []map[string]any{{"name": toolFetch, "description": toolFetch},
			{"name": "shell_exec", "description": "no"}}})
	})
	mux.HandleFunc("POST /tools/{name}/call", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"isError":false,"content":"page body"}`))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// realHarness is a harness built on the doors only: it learns its model and tools URLs from the sidecar,
// calls the model with the turn key as its API key, calls a tool over MCP, and answers.
type realHarness struct {
	*httptest.Server
	modelURL, toolsURL string
	mu                 sync.Mutex
	offered            []string
	blocked            string
}

func newRealHarness(t *testing.T) *realHarness {
	t.Helper()
	h := &realHarness{}
	exec := a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[sdk.Event, error] {
		return func(yield func(sdk.Event, error) bool) {
			yield(sdk.NewMessage(sdk.MessageRoleAgent, sdk.NewTextPart(h.work(ctx, ec.Message))), nil)
		}
	})
	mux := http.NewServeMux()
	mux.Handle("/", a2asrv.NewRESTHandler(a2asrv.NewHandler(exec)))
	h.Server = httptest.NewServer(mux)
	card := &sdk.AgentCard{Name: "e2e", SupportedInterfaces: []*sdk.AgentInterface{
		sdk.NewAgentInterface(h.URL, sdk.TransportProtocolHTTPJSON)}, DefaultInputModes: []string{"text"},
		DefaultOutputModes: []string{"text"}}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	t.Cleanup(h.Close)
	return h
}

func (h *realHarness) work(ctx context.Context, msg *sdk.Message) string {
	raw, _ := json.Marshal(msg.Parts[1].Data())
	var in a2a.TurnInput
	_ = json.Unmarshal(raw, &in)
	body, _ := json.Marshal(map[string]any{"model": "default", "messages": []map[string]string{{"role": "user", "content": msg.Parts[0].Text()}}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.modelURL+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+in.TurnKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	_ = resp.Body.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "h", Version: "1"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.toolsURL,
		HTTPClient: &http.Client{Transport: bearer{in.TurnKey}}}, nil)
	if err != nil {
		return ""
	}
	defer func() { _ = sess.Close() }()
	list, _ := sess.ListTools(ctx, nil)
	h.mu.Lock()
	for _, tool := range list.Tools {
		h.offered = append(h.offered, tool.Name)
	}
	h.mu.Unlock()
	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: toolFetch, Arguments: map[string]any{}}); err != nil {
		return ""
	}
	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "shell_exec", Arguments: map[string]any{}}); err != nil {
		h.mu.Lock()
		h.blocked = err.Error()
		h.mu.Unlock()
	}
	return "Two nodes have a GPU, from " + in.Agent
}

type rig struct {
	app     *app.App
	nc      *nats.Conn
	url     string
	harness *realHarness
	got     chan kbus.DiscussionMessage
	raw     chan []byte
	logs    *syncBuf
}

// newRig starts a sidecar on a real NATS server with a real A2A harness behind it.
func newRig(t *testing.T, mutate func(*mesh.Contract)) *rig {
	t.Helper()
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	js := bustest.JetStream(t, nc)
	bustest.DiscussStream(t, js)
	bustest.Consumer(t, js, identity, 30*time.Second)
	return startRig(t, url, nc, mutate)
}

func startRig(t *testing.T, url string, nc *nats.Conn, mutate func(*mesh.Contract)) *rig {
	t.Helper()
	model, gw := modelServer(t), toolGateway(t)
	h := newRealHarness(t)
	c := mesh.Contract{ContractVersion: mesh.ContractVersion, Identity: identity,
		Bus:  mesh.Bus{NATSURL: url, Stream: kbus.DiscussStream, Consumer: identity.ConsumerName(), ProgressAckSeconds: 1},
		Turn: mesh.Turn{URL: h.URL, Concurrency: 1, DeadlineSeconds: 30},
		Models: mesh.Models{Aliases: map[string]mesh.Alias{mesh.DefaultAlias: {Candidates: []string{modelName},
			Fallback: &mesh.Endpoint{Server: "gpu1", Endpoint: model.URL, Model: modelName}}}},
		Tools: mesh.Tools{Gateway: gw.URL, Enabled: []string{toolFetch}, NativeTools: mesh.NativeToolsNone}}
	if mutate != nil {
		mutate(&c)
	}
	logs := &syncBuf{}
	a := app.New(c, app.Options{Log: slog.New(slog.NewTextHandler(logs, nil)), HTTPAddr: ephemeral, ModelAddr: ephemeral, ToolsAddr: ephemeral,
		ObserveEvery: 100 * time.Millisecond, HeartbeatEvery: 200 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); a.Stop() })
	h.modelURL, h.toolsURL = "http://"+a.Addrs().Model+"/v1", "http://"+a.Addrs().Tools+"/mcp"
	r := &rig{app: a, nc: nc, url: url, logs: logs, harness: h, got: make(chan kbus.DiscussionMessage, 64), raw: make(chan []byte, 64)}
	if _, err := nc.Subscribe("kubemoot.discuss.>", func(m *nats.Msg) {
		var msg kbus.DiscussionMessage
		if json.Unmarshal(m.Data, &msg) == nil && msg.AgentName == agent {
			r.raw <- m.Data
			r.got <- msg
		}
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !r.app.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("the sidecar never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (r *rig) advisory(t *testing.T, thread string) {
	t.Helper()
	msg := kbus.DiscussionMessage{MessageID: "adv-" + thread, ThreadID: thread, AgentName: "coordinator",
		MessageType: "advisory_ready", Channel: general, Metadata: map[string]any{"userQuery": "Which nodes have a GPU?",
			"innerCircle": []any{agent}}}
	data, _ := json.Marshal(msg)
	if err := r.nc.Publish(identity.DiscussSubject(general, thread), data); err != nil {
		t.Fatal(err)
	}
}

// terminal waits for the agent's terminal signal, validating every message on the way.
func (r *rig) terminal(t *testing.T) kbus.DiscussionMessage {
	t.Helper()
	for {
		select {
		case raw := <-r.raw:
			bustest.Validate(t, "discussion-message.json", raw)
			m := <-r.got
			switch m.MessageType {
			case signalAgree, "concern", "stand_aside", "failure", "block":
				return m
			}
		case <-time.After(15 * time.Second):
			t.Fatal("no terminal signal")
		}
	}
}

func TestATurnGoesThroughAllThreeDoorsAndTheSidecarMeasuresIt(t *testing.T) {
	r := newRig(t, nil)
	r.waitReady(t)
	r.advisory(t, "e1")
	m := r.terminal(t)
	if m.MessageType != signalAgree || !strings.HasPrefix(m.Content, "Two nodes have a GPU, from "+agent) {
		t.Fatalf("%+v", m)
	}
	checkMeasured(t, m.Metadata)
	r.harness.mu.Lock()
	defer r.harness.mu.Unlock()
	if strings.Join(r.harness.offered, ",") != toolFetch {
		t.Errorf("the harness is offered only the tools the agent may use: %v", r.harness.offered)
	}
	if r.harness.blocked == "" {
		t.Error("a tool outside the allow-list must not run")
	}
}

// checkMeasured asserts what the sidecar measured from the calls that went through its doors.
func checkMeasured(t *testing.T, md map[string]any) {
	t.Helper()
	if md["inputTokens"] != float64(50) || md["outputTokens"] != float64(7) || md["server"] != "gpu1" ||
		md["modelName"] != modelName || md["harness"] != "e2e-harness" {
		t.Errorf("the sidecar measures what went through its doors: %v", md)
	}
	called, _ := md["toolsCalled"].([]any)
	if len(called) != 1 || called[0] != toolFetch {
		t.Errorf("toolsCalled %v", md["toolsCalled"])
	}
}

func TestReadinessIsStateNotATimer(t *testing.T) {
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	js := bustest.JetStream(t, nc)
	bustest.DiscussStream(t, js)
	bustest.Consumer(t, js, identity, 30*time.Second)
	r := startRig(t, url, nc, nil)
	base := "http://" + r.app.Addrs().HTTP
	get := func(path string) int {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if get("/healthz") != 200 {
		t.Error("the sidecar is alive as soon as it listens, so its own startup probe never waits on the harness")
	}
	r.waitReady(t)
	if get("/readyz") != 200 {
		t.Error("ready once NATS is connected and the harness card answers")
	}
	r.harness.Close()
	deadline := time.Now().Add(10 * time.Second)
	for get("/readyz") != 503 {
		if time.Now().After(deadline) {
			t.Fatal("a harness that stopped answering must take the pod out of rotation")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if get("/healthz") != 200 {
		t.Error("a dead harness is not a dead sidecar")
	}
}

func TestMetricsAreServedUnderAgentRuntimesNamesWithUniformLabels(t *testing.T) {
	r := newRig(t, nil)
	r.waitReady(t)
	r.advisory(t, "e2")
	r.terminal(t)
	resp, err := http.Get("http://" + r.app.Addrs().HTTP + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		`kubemoot_scheduler_calls_total{agent="notes-reader",crew="pilot",kind="simple",model="qwen3:14b",namespace="homelab",outcome="ok"`,
		`kubemoot_scheduler_call_seconds_bucket`, `kubemoot_scheduler_generated_tokens_total{agent="notes-reader"`,
		`kubemoot_sidecar_turns_total{agent="notes-reader",crew="pilot",namespace="homelab",signal="agree"} 1`,
		`kubemoot_sidecar_door_requests_total`, `kubemoot_sidecar_progress_acks_total`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

func TestChatIsRefusedPlainlyUntilTheSidecarServesIt(t *testing.T) {
	r := newRig(t, nil)
	resp, err := http.Post("http://"+r.app.Addrs().HTTP+"/api/chat", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestWithoutAServiceOrABoundServerTheContractIsRefused(t *testing.T) {
	url := bustest.Server(t)
	nc := bustest.Connect(t, url)
	gw := toolGateway(t)
	c := mesh.Contract{ContractVersion: mesh.ContractVersion, Identity: identity, Bus: mesh.Bus{NATSURL: url},
		Turn:   mesh.Turn{URL: "http://127.0.0.1:1", Concurrency: 1, DeadlineSeconds: 5},
		Models: mesh.Models{Aliases: map[string]mesh.Alias{mesh.DefaultAlias: {Candidates: []string{"m"}}}},
		Tools:  mesh.Tools{Gateway: gw.URL}}
	a := app.New(c, app.Options{HTTPAddr: ephemeral, ModelAddr: ephemeral, ToolsAddr: ephemeral})
	if err := a.Start(context.Background()); err == nil {
		a.Stop()
		t.Fatal("a sidecar that can place no call must not start")
	}
	_ = nc
}

func TestTheSidecarRunsWithOnlyThePermissionsTheContractGivesIt(t *testing.T) {
	opts := &server.Options{Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true}
	perms := identity.Permissions()
	opts.Users = []*server.User{
		{Username: "admin", Password: "admin"},
		{Username: agent, Password: "secret", Permissions: &server.Permissions{
			Publish:   &server.SubjectPermission{Allow: perms.Publish},
			Subscribe: &server.SubjectPermission{Allow: perms.Subscribe}}},
	}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("no server")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	admin := bustest.Connect(t, s.ClientURL(), nats.UserInfo("admin", "admin"))
	js := bustest.JetStream(t, admin)
	bustest.DiscussStream(t, js)
	bustest.Consumer(t, js, identity, 30*time.Second)
	dir := t.TempDir()
	writeFile(t, dir+"/user", agent)
	writeFile(t, dir+"/password", "secret")
	r := startRig(t, s.ClientURL(), admin, func(c *mesh.Contract) { c.Bus.CredentialsDir = dir })
	r.waitReady(t)
	r.advisory(t, "p1")
	m := r.terminal(t)
	if m.MessageType != signalAgree {
		t.Fatalf("under its own permissions the agent still answers: %+v", m)
	}
	// What the agent may not do: publish as another agent, or write another agent's heartbeat.
	own := bustest.Connect(t, s.ClientURL(), nats.UserInfo(agent, "secret"))
	other := mesh.Identity{Namespace: "homelab", Crew: "pilot", Agent: "someone-else"}
	_ = own.Publish(other.LifecycleSubject("ready"), []byte("{}"))
	_ = own.Publish("kubemoot.discuss.homelab.other-crew.general.t", []byte("{}"))
	errs := make(chan error, 4)
	own.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err })
	_ = own.Publish(other.LifecycleSubject("ready"), []byte("{}"))
	_ = own.Flush()
	select {
	case err := <-errs:
		if !strings.Contains(strings.ToLower(err.Error()), "permission") {
			t.Errorf("unexpected error %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("publishing as another agent must be refused by the server")
	}
}

func TestBadCredentialsKeepTheSidecarOutOfTheBus(t *testing.T) {
	opts := &server.Options{Port: -1, NoLog: true, NoSigs: true, Users: []*server.User{{Username: "u", Password: "right"}}}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("no server")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	dir := t.TempDir()
	writeFile(t, dir+"/user", "u")
	writeFile(t, dir+"/password", "wrong")
	model, gw := modelServer(t), toolGateway(t)
	c := mesh.Contract{ContractVersion: mesh.ContractVersion, Identity: identity,
		Bus:    mesh.Bus{NATSURL: s.ClientURL(), CredentialsDir: dir},
		Turn:   mesh.Turn{URL: "http://127.0.0.1:1", Concurrency: 1, DeadlineSeconds: 5},
		Models: mesh.Models{Aliases: map[string]mesh.Alias{mesh.DefaultAlias: {Candidates: []string{"m"}, Fallback: &mesh.Endpoint{Server: "s", Endpoint: model.URL, Model: "m"}}}},
		Tools:  mesh.Tools{Gateway: gw.URL}}
	a := app.New(c, app.Options{HTTPAddr: ephemeral, ModelAddr: ephemeral, ToolsAddr: ephemeral})
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	time.Sleep(1500 * time.Millisecond)
	if a.Ready() {
		t.Fatal("a sidecar the server refused must not be ready")
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
