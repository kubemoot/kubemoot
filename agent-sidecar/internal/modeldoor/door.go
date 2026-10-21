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
	"strconv"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const maxBody = 32 << 20

// Config is what the door needs from the sidecar's contract file.
type Config struct {
	Crew, Agent string
	// Aliases maps an alias such as "default" to the acceptable models, preferred first.
	Aliases map[string][]string
	Phase   string
}

// Door is the http.Handler of /v1.
type Door struct {
	cfg      Config
	placer   placement.Placer
	registry *turn.Registry
	upstream *http.Client
}

// New returns a door.
func New(cfg Config, placer placement.Placer, registry *turn.Registry) *Door {
	return &Door{cfg: cfg, placer: placer, registry: registry, upstream: &http.Client{Transport: &http.Transport{
		MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second, DisableCompression: true,
	}}}
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
	list := make([]model, 0, len(d.cfg.Aliases))
	for alias := range d.cfg.Aliases {
		list = append(list, model{ID: alias, Object: "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": list})
}

func (d *Door) chat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	placed, err := d.placeCall(r.Context(), req, len(body))
	if err != nil {
		d.placementFailed(w, err)
		return
	}
	defer d.release(placed.Ticket)
	d.forward(w, r, req, placed)
}

func (d *Door) placeCall(ctx context.Context, req map[string]any, bodyBytes int) (placement.Reply, error) {
	alias, _ := req["model"].(string)
	models, ok := d.cfg.Aliases[alias]
	if !ok {
		models = d.cfg.Aliases["default"]
	}
	out, _ := req["max_tokens"].(float64)
	return d.placer.Place(ctx, placement.Request{
		Crew: d.cfg.Crew, Agent: d.cfg.Agent, Phase: d.cfg.Phase, Models: models,
		PromptTokens: int64(bodyBytes / 4), MaxOutputTokens: int64(out), MayUnload: true,
	})
}

func (d *Door) placementFailed(w http.ResponseWriter, err error) {
	var noFit *placement.ErrNoFit
	if errors.As(err, &noFit) {
		writeError(w, http.StatusServiceUnavailable, "kubemoot_no_fit", noFit.Reason)
		return
	}
	writeError(w, http.StatusBadGateway, "kubemoot_placement_unavailable", err.Error())
}

func (d *Door) release(ticket string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.placer.Release(ctx, ticket)
}

func (d *Door) forward(w http.ResponseWriter, r *http.Request, req map[string]any, placed placement.Reply) {
	req["model"] = placed.Model
	stream, _ := req["stream"].(bool)
	if stream {
		req["stream_options"] = map[string]any{"include_usage": true}
	}
	payload, _ := json.Marshal(req)
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(placed.Endpoint, "/")+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		writeError(w, http.StatusBadGateway, "bad_endpoint", err.Error())
		return
	}
	up.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := d.upstream.Do(up)
	if err != nil {
		d.recordFailure(r, placed, err)
		writeError(w, http.StatusBadGateway, "kubemoot_model_unreachable", err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	usage := d.copyThrough(w, resp)
	if m := d.registry.Lookup(bearer(r)); m != nil {
		m.ModelCall(placed.Server, placed.Model, placed.Reason, usage.prompt, usage.completion, time.Since(started))
	}
}

func (d *Door) recordFailure(r *http.Request, placed placement.Reply, err error) {
	if m := d.registry.Lookup(bearer(r)); m != nil {
		m.ModelCall(placed.Server, placed.Model, "failed: "+err.Error(), 0, 0, 0)
	}
}

type usage struct{ prompt, completion int64 }

// copyThrough writes the upstream reply to w, flushing every read so a stream is never buffered, and
// returns the token usage the upstream reported.
func (d *Door) copyThrough(w http.ResponseWriter, resp *http.Response) usage {
	for k, v := range resp.Header {
		if !strings.EqualFold(k, "Content-Length") {
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
