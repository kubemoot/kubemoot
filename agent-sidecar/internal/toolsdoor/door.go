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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// Config is the agent's tool policy from the contract file.
type Config struct {
	Gateway  string
	Enabled  []string // empty means every tool the gateway lists
	Disabled []string
}

// Allowed reports whether the agent may use the tool: not disabled, and enabled when a list is given.
func (c Config) Allowed(name string) bool {
	for _, d := range c.Disabled {
		if d == name {
			return false
		}
	}
	if len(c.Enabled) == 0 {
		return true
	}
	for _, e := range c.Enabled {
		if e == name {
			return true
		}
	}
	return false
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
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Door builds the MCP handler.
type Door struct {
	cfg      Config
	registry *turn.Registry
	http     *http.Client
}

// New returns a tools door.
func New(cfg Config, registry *turn.Registry) *Door {
	return &Door{cfg: cfg, registry: registry, http: &http.Client{Timeout: 60 * time.Second}}
}

// Handler returns the streamable HTTP handler for /mcp. Each session sees the tools the gateway lists now,
// filtered by the allow-list.
func (d *Door) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		key := bearerKey(r)
		srv := mcp.NewServer(&mcp.Implementation{Name: "kubemoot-sidecar", Version: "spike"}, nil)
		tools, err := d.List(r.Context())
		if err != nil {
			return srv
		}
		for _, t := range tools {
			if !d.cfg.Allowed(t.Name) {
				continue
			}
			tool := &mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: schemaOf(t)}
			srv.AddTool(tool, d.callHandler(t.Name, key))
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

func (d *Door) callHandler(name, key string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !d.cfg.Allowed(name) {
			return nil, fmt.Errorf("tool %q is not allowed for this agent", name)
		}
		text, failed, err := d.call(ctx, name, req.Params.Arguments)
		if m := d.registry.Lookup(key); m != nil {
			m.ToolCall(name, failed || err != nil)
		}
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{IsError: failed, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
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
	var out struct {
		Tools []GatewayTool `json:"tools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (d *Door) call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	var arguments map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", true, err
		}
	}
	payload, _ := json.Marshal(map[string]any{"arguments": arguments})
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
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		IsError bool   `json:"isError"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return string(body), true, nil
	}
	return out.Content, out.IsError, nil
}
