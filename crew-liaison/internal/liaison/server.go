package liaison

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is stamped by the build; the MCP handshake reports it.
var Version = "dev"

// NewMCPServer registers the three tools on a fresh MCP server.
func NewMCPServer(svc *Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "kubemoot-crew-liaison", Version: Version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_crews",
		Description: "List the Kubemoot crews you can ask: name, namespace, what each is for, and whether it is ready.",
	}, svc.ListCrews)
	mcp.AddTool(server, &mcp.Tool{
		Name: "ask",
		Description: "Ask a crew a question. " +
			"The crew deliberates for one to several minutes and returns one synthesized answer. " +
			"Waits up to 45 seconds, then returns a ticket if the crew is still at it; poll get_answer with the ticket. " +
			"Asking the same question again while it runs rejoins the same ticket.",
	}, svc.Ask)
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_answer",
		Description: "Check a ticket from ask. Waits up to 45 seconds for the crew to settle and returns status pending, " +
			"answered (with the answer), or failed. Call it again while pending.",
	}, svc.GetAnswer)
	return server
}

// NewHandler serves MCP at /mcp (bearer-protected when token is set) plus health probes.
func NewHandler(svc *Service, token string) http.Handler {
	server := NewMCPServer(svc)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", requireBearer(token, mcpHandler))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, "ok") })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, "ready") })
	return mux
}

// requireBearer gates next behind a static bearer token; an empty token disables the gate
// (for installs that authenticate at the edge, for example a Cloudflare Access front door).
func requireBearer(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kubemoot-crew-liaison"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
