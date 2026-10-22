// Package toolsdoor serves MCP over streamable HTTP to the harness. It lists only the tools this agent
// may use and calls the crew's gateway (today REST: GET /tools, POST /tools/{name}/call) for each call.
package toolsdoor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// maxGatewayBody bounds what the door reads from the gateway: a tool result, or the tool list.
const maxGatewayBody = 16 << 20

// The keys of the requester's date and time zone in a tool call's _meta. They match the keys the
// tools read (see the card "Tools Get the Requester Date and Time Zone").
const (
	metaRequestDate = "requestDate"
	metaTimeZone    = "timeZone"
)

// Config is the agent's tool policy from the contract file.
type Config struct {
	// RequireTurn refuses a tool call that belongs to no open turn. Listing tools is always allowed.
	RequireTurn bool
	Gateway     string
	Enabled     []string // empty means every tool the gateway lists
	Disabled    []string
}

// Allowed reports whether the agent may use the tool: not disabled, and enabled when a list is given.
func (c Config) Allowed(name string) bool {
	if slices.Contains(c.Disabled, name) {
		return false
	}
	return len(c.Enabled) == 0 || slices.Contains(c.Enabled, name)
}

// MissingDeclared returns the declared tools the gateway does not list.
func (c Config) MissingDeclared(listed []GatewayTool) []string {
	have := map[string]bool{}
	for _, t := range listed {
		have[t.Name] = true
	}
	var missing []string
	for _, e := range c.Enabled {
		if !have[e] {
			missing = append(missing, e)
		}
	}
	return missing
}

// GatewayTool is one entry of the gateway's tool list.
type GatewayTool struct {
	Name        string         `json:"name"`
	ServerID    string         `json:"serverId,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// Annotations are the tool's MCP annotations (readOnlyHint, idempotentHint), when the gateway
	// passes them on. A tool without them is never answered from an earlier call.
	Annotations map[string]any `json:"annotations,omitempty"`
}

// Reusable reports whether an identical repeat call returns the same result: the tool declares
// readOnlyHint as true. idempotentHint is not enough, it promises no extra effect, not the same result.
func (t GatewayTool) Reusable() bool {
	return t.Annotations["readOnlyHint"] == true
}

// reusedNote is what the model reads in place of a repeated call's result.
const reusedNote = "This exact call already returned the result above in this turn; it was not called again."

// callKey identifies a call by server, tool and arguments. Arguments are decoded and re-encoded so
// key order and spacing do not make two identical calls look different.
func callKey(serverID, name string, args json.RawMessage) string {
	var canon any
	if len(args) == 0 || json.Unmarshal(args, &canon) != nil {
		return serverID + "\x00" + name + "\x00" + string(args)
	}
	norm, _ := json.Marshal(canon)
	return serverID + "\x00" + name + "\x00" + string(norm)
}

// nameCounts counts how often each tool name appears in the tool list.
func nameCounts(tools []GatewayTool) map[string]int {
	n := map[string]int{}
	for _, t := range tools {
		n[t.Name]++
	}
	return n
}

// Door builds the MCP handler.
type Door struct {
	cfg      Config
	registry *turn.Registry
	metrics  *obs.Metrics
	http     *http.Client
}

// New returns a tools door. metrics may be nil.
func New(cfg Config, registry *turn.Registry, metrics *obs.Metrics) *Door {
	return &Door{cfg: cfg, registry: registry, metrics: metrics, http: &http.Client{Timeout: 60 * time.Second,
		Transport: obs.Transport(nil, "mcp-gateway")}}
}

// Handler returns the streamable HTTP handler for /mcp. Each session sees the tools the gateway lists now,
// filtered by the allow-list.
func (d *Door) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		key := bearerKey(r)
		srv := mcp.NewServer(&mcp.Implementation{Name: "kubemoot-sidecar", Version: "1"}, nil)
		tools, err := d.List(r.Context())
		if err != nil {
			return srv
		}
		names := nameCounts(tools)
		for _, t := range tools {
			if !d.cfg.Allowed(t.Name) {
				continue
			}
			tool := &mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: schemaOf(t)}
			srv.AddTool(tool, d.callHandler(t.Name, key, t.ServerID, t.Reusable() && names[t.Name] == 1))
		}
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}

func schemaOf(t GatewayTool) map[string]any {
	if t.InputSchema == nil {
		return map[string]any{"type": "object"}
	}
	return t.InputSchema
}

func bearerKey(r *http.Request) string {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(p) {
		return h[len(p):]
	}
	return ""
}

func (d *Door) callHandler(name, key, serverID string, reusable bool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !d.cfg.Allowed(name) {
			d.count("denied")
			return nil, fmt.Errorf("tool %q is not allowed for this agent", name)
		}
		meter := d.registry.Lookup(key)
		if d.cfg.RequireTurn && meter == nil {
			d.count("no_turn")
			return nil, fmt.Errorf("tool %q was called outside a turn", name)
		}
		return d.run(ctx, meter, name, req.Params.Arguments, serverID, reusable)
	}
}

// run answers a repeat of a reusable call from the turn, and otherwise calls the gateway and
// records the outcome on the turn.
func (d *Door) run(ctx context.Context, meter *turn.Meter, name string, args json.RawMessage,
	serverID string, reusable bool) (*mcp.CallToolResult, error) {
	ck := callKey(serverID, name, args)
	if _, seen := answeredIn(meter, reusable, ck); seen {
		d.count("reused")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: reusedNote}}}, nil
	}
	text, failed, err := d.call(ctx, name, args, requesterMeta(meter))
	ok := !failed && err == nil
	if meter != nil {
		meter.ToolCall(name, !ok)
		if reusable && ok {
			meter.Remember(ck, text)
		}
	}
	d.count(obs.Result(ok))
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{IsError: failed, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
}

// answeredIn returns the result the turn already holds for the call, only for a reusable tool.
func answeredIn(meter *turn.Meter, reusable bool, key string) (string, bool) {
	if meter == nil || !reusable {
		return "", false
	}
	return meter.Answered(key)
}

// Missing implements the bus's tool guard: the declared tools the gateway does not list. A gateway
// that cannot be listed (or is not configured) reports every declared tool missing, with the reason.
func (d *Door) Missing(ctx context.Context) ([]string, string) {
	if len(d.cfg.Enabled) == 0 {
		return nil, ""
	}
	if d.cfg.Gateway == "" {
		return append([]string(nil), d.cfg.Enabled...), "the crew has no tool gateway"
	}
	listed, err := d.List(ctx)
	if err != nil {
		return append([]string(nil), d.cfg.Enabled...), "gateway " + d.cfg.Gateway + ": " + err.Error()
	}
	return d.cfg.MissingDeclared(listed), "gateway " + d.cfg.Gateway + " does not list them"
}

func (d *Door) count(result string) {
	if d.metrics != nil {
		d.metrics.Door("tools", result)
	}
}

// requesterMeta is the _meta a tool call carries: the requester's date and time zone when the
// turn has them, nothing otherwise (no _meta at all, so a gateway that does not read it sees the
// call it always saw).
func requesterMeta(m *turn.Meter) map[string]string {
	if m == nil {
		return nil
	}
	info := m.Info()
	meta := map[string]string{}
	if info.RequestDate != "" {
		meta[metaRequestDate] = info.RequestDate
	}
	if info.TimeZone != "" {
		meta[metaTimeZone] = info.TimeZone
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// List returns the gateway's tools, unfiltered.
func (d *Door) List(ctx context.Context) ([]GatewayTool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.cfg.Gateway+"/tools", nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("the gateway answered %d", resp.StatusCode)
	}
	var out struct {
		Tools []GatewayTool `json:"tools"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGatewayBody)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (d *Door) call(ctx context.Context, name string, args json.RawMessage,
	meta map[string]string) (string, bool, error) {
	var arguments map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", true, err
		}
	}
	call := map[string]any{"arguments": arguments}
	if len(meta) > 0 {
		call["_meta"] = meta
	}
	payload, _ := json.Marshal(call)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.cfg.Gateway+"/tools/"+url.PathEscape(name)+"/call", bytes.NewReader(payload))
	if err != nil {
		return "", true, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return "", true, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxGatewayBody))
	if resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Sprintf("the gateway answered %d: %s", resp.StatusCode, bytes.TrimSpace(body)), true, nil
	}
	var out struct {
		IsError bool   `json:"isError"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return string(body), true, nil
	}
	return out.Content, out.IsError, nil
}
