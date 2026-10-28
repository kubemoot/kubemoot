package server

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Paths the HTTP transport serves. The gateway reaches the MCP endpoint at
// MCPPath; the health paths are for an MCPServer's healthPath and readinessPath.
const (
	MCPPath       = "/mcp"
	HealthPath    = "/healthz"
	ReadinessPath = "/readyz"
)

// HTTPHandler serves the MCP Streamable HTTP transport at MCPPath, and a plain
// 200 on the two health paths. All sessions share one MCP server, as the tools
// hold no per-session state.
func (s *Server) HTTPHandler() (http.Handler, error) {
	srv, err := s.MCP()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle(MCPPath, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	ok := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	}
	mux.HandleFunc(HealthPath, ok)
	mux.HandleFunc(ReadinessPath, ok)
	return mux, nil
}
