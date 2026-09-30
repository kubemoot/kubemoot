// Package server implements the line-delimited JSON-RPC stdio loop that
// the Model Context Protocol uses over stdio transports. This server
// handles `initialize`, `notifications/initialized`, `tools/list`, and
// `tools/call`. Anything else returns a method-not-found error.
//
// The actual tool work lives in package handlers; this file is the wire
// protocol only.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/kubemoot/kubemoot/scheduling-mcp/internal/handlers"
)

// JSON-RPC 2.0 message types we care about.

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Server holds the handler set the dispatcher consults.
type Server struct {
	Handlers *handlers.Set
	// ProtocolVersion is reported in the initialize response.
	ProtocolVersion string
	// ServerName is reported in the initialize response.
	ServerName string
	// ServerVersion is reported in the initialize response.
	ServerVersion string
}

// Run reads JSON-RPC messages line-by-line from `in`, dispatches them,
// and writes responses line-by-line to `out`. It blocks until `in` is
// closed or ctx is done.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	// Allow large messages — tools/list responses can run to a few KB.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	enc := json.NewEncoder(out)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(errorResponse(nil, -32700, "Parse error", err.Error()))
			continue
		}
		resp := s.dispatch(ctx, &req)
		// Notifications (no id) get no response per JSON-RPC spec.
		if req.ID == nil {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	return scanner.Err()
}

func (s *Server) dispatch(ctx context.Context, req *request) *response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized", "initialized":
		// Acknowledged; no result.
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return errorResponse(req.ID, -32601, "Method not found", req.Method)
	}
}

// initialize: report capabilities + identity.
func (s *Server) handleInitialize(req *request) *response {
	result := map[string]any{
		"protocolVersion": s.ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{}, // we support tools/* methods
		},
		"serverInfo": map[string]any{
			"name":    s.ServerName,
			"version": s.ServerVersion,
		},
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

// tools/list: return all registered tools.
func (s *Server) handleToolsList(req *request) *response {
	result := map[string]any{
		"tools": s.Handlers.Specifications(),
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

// tools/call: dispatch to the named handler.
func (s *Server) handleToolsCall(ctx context.Context, req *request) *response {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResponse(req.ID, -32602, "Invalid params", err.Error())
	}
	out, err := s.Handlers.Call(ctx, params.Name, params.Arguments)
	if err != nil {
		// Surface tool-level errors via the MCP convention: result with
		// isError=true plus a text content block. This lets the calling
		// LLM see the error in-band rather than treating it as a transport
		// failure.
		return &response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"isError": true,
				"content": []map[string]any{
					{"type": "text", "text": err.Error()},
				},
			},
		}
	}
	return &response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": out},
			},
		},
	}
}

func errorResponse(id json.RawMessage, code int, msg, data string) *response {
	return &response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg, Data: data},
	}
}
