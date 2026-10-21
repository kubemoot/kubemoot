// Package modeldoor serves the OpenAI-compatible endpoint a harness calls. The model field is an
// alias; each call is placed by the placement service, forwarded to the chosen server, streamed back
// unbuffered, measured, and its ticket released.
package modeldoor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/mesh"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const maxBody = 32 << 20

// Config is what the door needs from the sidecar's contract file.
type Config struct {
	Namespace, Crew, Agent string
	// Aliases maps an alias such as "default" to the acceptable models, preferred first.
	Aliases map[string][]string
	Phase   string
	// RequireTurn refuses a chat call that belongs to no open turn: the model is for the agent's
	// discussion turns, and a call outside one would use an accelerator unmetered.
	RequireTurn bool
}

// Door is the http.Handler of /v1.
type Door struct {
	cfg      Config
	placer   placement.Placer
	registry *turn.Registry
	metrics  *obs.Metrics
	upstream *http.Client
}

// New returns a door. metrics may be nil.
func New(cfg Config, placer placement.Placer, registry *turn.Registry, metrics *obs.Metrics) *Door {
	return &Door{cfg: cfg, placer: placer, registry: registry, metrics: metrics,
		upstream: &http.Client{Transport: obs.Transport(&http.Transport{
			MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second, DisableCompression: true,
		}, "model-server")}}
}

// ServeHTTP routes /v1/chat/completions and /v1/models.
func (d *Door) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		d.chat(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		d.models(w)
	default:
		writeError(w, http.StatusNotFound, "not_found", "no such route")
	}
}

func (d *Door) models(w http.ResponseWriter) {
	type model struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	aliases := make([]string, 0, len(d.cfg.Aliases))
	for alias := range d.cfg.Aliases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	list := make([]model, 0, len(aliases))
	for _, alias := range aliases {
		list = append(list, model{ID: alias, Object: "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": list})
}

// call is one chat request being served.
type call struct {
	alias, kind string
	meter       *turn.Meter
	req         map[string]any
	bodyBytes   int
}

func (d *Door) chat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		d.count("bad_request")
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		d.count("bad_request")
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	c := d.newCall(r, req, len(body))
	if d.cfg.RequireTurn && c.meter == nil {
		d.count("no_turn")
		writeError(w, http.StatusForbidden, "kubemoot_no_active_turn", "the model door serves the agent's turns only")
		return
	}
	placed, err := d.place(r.Context(), c)
	if err != nil {
		d.placementFailed(w, c, err)
		return
	}
	d.forward(w, r, c, placed)
}

func (d *Door) newCall(r *http.Request, req map[string]any, bodyBytes int) *call {
	alias, _ := req["model"].(string)
	if _, ok := d.cfg.Aliases[alias]; !ok {
		alias = mesh.DefaultAlias
	}
	kind := "simple"
	switch tools, _ := req["tools"].([]any); {
	case alias == mesh.TriageAlias:
		kind = "triage"
	case len(tools) > 0:
		kind = "tool-turn"
	}
	return &call{alias: alias, kind: kind, meter: d.registry.Lookup(bearer(r)), req: req, bodyBytes: bodyBytes}
}

func (d *Door) place(ctx context.Context, c *call) (placement.Reply, error) {
	maxOut, _ := c.req["max_tokens"].(float64)
	var info turn.Info
	if c.meter != nil {
		info = c.meter.Info()
	}
	started := time.Now()
	reply, err := d.placer.Place(ctx, placement.Request{
		Namespace: d.cfg.Namespace, Crew: d.cfg.Crew, Agent: d.cfg.Agent, ThreadID: info.ThreadID,
		Phase: d.cfg.Phase, Alias: c.alias, Models: d.cfg.Aliases[c.alias],
		PromptTokens: int64(c.bodyBytes / 4), MaxOutputTokens: int64(maxOut), MayUnload: true,
	}, func(reason string) {
		if c.meter != nil {
			c.meter.Waiting(reason)
		}
	})
	d.recordWait(c, reply, err, time.Since(started))
	if err == nil && reply.Degraded && c.meter != nil {
		c.meter.Degraded("placement-unavailable")
	}
	return reply, err
}

// recordWait counts a call that waited for capacity: the service said it queued the call, or the
// call ended in a no-fit after waiting.
func (d *Door) recordWait(c *call, reply placement.Reply, err error, took time.Duration) {
	var noFit *placement.ErrNoFit
	switch {
	case d.metrics == nil:
	case err == nil && reply.Queued:
		d.metrics.Waited(reply.Server, reply.Model, took)
	case errors.As(err, &noFit):
		d.metrics.Waited("", c.alias, took)
	}
}

func (d *Door) placementFailed(w http.ResponseWriter, c *call, err error) {
	var noFit *placement.ErrNoFit
	if errors.As(err, &noFit) {
		if c.meter != nil {
			c.meter.NoFit(noFit.Kind)
		}
		d.count("no_fit")
		writeError(w, http.StatusServiceUnavailable, "kubemoot_no_fit", noFit.Reason)
		return
	}
	d.count("placement_failed")
	writeError(w, http.StatusBadGateway, "kubemoot_placement_unavailable", err.Error())
}

func (d *Door) count(result string) {
	if d.metrics != nil {
		d.metrics.Door("model", result)
	}
}

func (d *Door) forward(w http.ResponseWriter, r *http.Request, c *call, placed placement.Reply) {
	started := time.Now()
	status, u, err := d.send(w, r, c, placed)
	took := time.Since(started)
	ok := err == nil && status < http.StatusBadRequest
	d.release(placed, ok, u, took)
	if c.meter != nil {
		reason := placed.Reason
		if err != nil {
			reason = "failed: " + err.Error()
		}
		c.meter.ModelCall(placed.Server, placed.Model, reason, u.prompt, u.completion, took)
	}
	if d.metrics != nil {
		d.metrics.Call(obs.ModelCall{Server: placed.Server, Model: placed.Model, Kind: c.kind,
			Cold: placed.ColdLoad, OK: ok, Duration: took, OutputTokens: u.completion})
		d.metrics.Door("model", obs.Result(ok))
	}
}

// send forwards the request to the placed server and copies the reply through. It returns the
// upstream status, the usage the reply reported, and the transport error if the server was not reached.
func (d *Door) send(w http.ResponseWriter, r *http.Request, c *call, placed placement.Reply) (int, usage, error) {
	c.req["model"] = placed.Model
	if stream, _ := c.req["stream"].(bool); stream {
		c.req["stream_options"] = map[string]any{"include_usage": true}
	}
	payload, _ := json.Marshal(c.req)
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(placed.Endpoint, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		writeError(w, http.StatusBadGateway, "bad_endpoint", err.Error())
		return http.StatusBadGateway, usage{}, err
	}
	up.Header.Set("Content-Type", "application/json")
	resp, err := d.upstream.Do(up)
	if err != nil {
		writeError(w, http.StatusBadGateway, "kubemoot_model_unreachable", err.Error())
		return http.StatusBadGateway, usage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, copyThrough(w, resp), nil
}

func (d *Door) release(placed placement.Reply, ok bool, u usage, took time.Duration) {
	if placed.Ticket == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.placer.Release(ctx, placement.Release{Ticket: placed.Ticket, OK: ok, PromptTokens: u.prompt,
		CompletionTokens: u.completion, DurationMs: took.Milliseconds()})
}

type usage struct{ prompt, completion int64 }

// copyThrough writes the upstream reply to w, flushing every read so a stream is never buffered, and
// returns the token usage the upstream reported.
func copyThrough(w http.ResponseWriter, resp *http.Response) usage {
	for k, v := range resp.Header {
		if !hopByHop(k) {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	tap := &usageTap{}
	buf := make([]byte, 16<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			_ = rc.Flush()
			tap.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return tap.result()
}

// hopByHop reports a header that describes one connection and must not be passed on, with Content-Length,
// which the copy does not keep (the reply is streamed).
func hopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer",
		"transfer-encoding", "upgrade", "content-length":
		return true
	}
	return false
}

// usageTap finds the last "usage" object in a JSON body or in server-sent events.
type usageTap struct{ tail []byte }

func (t *usageTap) Write(p []byte) {
	t.tail = append(t.tail, p...)
	if len(t.tail) > 8<<10 {
		t.tail = t.tail[len(t.tail)-(8<<10):]
	}
}

var (
	promptRe     = regexp.MustCompile(`"prompt_tokens"\s*:\s*(\d+)`)
	completionRe = regexp.MustCompile(`"completion_tokens"\s*:\s*(\d+)`)
)

// result reads the last prompt_tokens and completion_tokens in the tail of the body, which is where
// both a JSON reply and the final server-sent event carry them.
func (t *usageTap) result() usage {
	return usage{prompt: lastInt(promptRe, t.tail), completion: lastInt(completionRe, t.tail)}
}

func lastInt(re *regexp.Regexp, b []byte) int64 {
	all := re.FindAllSubmatch(b, -1)
	if len(all) == 0 {
		return 0
	}
	n, _ := strconv.ParseInt(string(all[len(all)-1][1]), 10, 64)
	return n
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"message": msg, "type": "kubemoot_error", "code": code,
	}})
}
