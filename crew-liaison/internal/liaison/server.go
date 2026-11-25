package liaison

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

// Version is stamped by the build; the MCP handshake reports it.
var Version = "dev"

// DefaultDisplayName is the door's title when the install sets none.
const DefaultDisplayName = "Kubemoot"

// NewMCPServer registers the four tools on a fresh MCP server.
func NewMCPServer(svc *Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name: "kubemoot-crew-liaison", Title: svc.displayName(), Version: Version,
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_crews",
		Description: "List the Kubemoot crews you can ask: name, namespace, what each is for, and whether it is ready.",
	}, svc.ListCrews)
	mcp.AddTool(server, &mcp.Tool{
		Name: "ask",
		Description: "Ask a crew a question. " +
			"The crew deliberates for one to several minutes and returns one synthesized answer. " +
			"Waits up to 45 seconds, then returns a ticket if the crew is still at it; poll get_answer with the ticket. " +
			"To retry a question that is still running, pass the ticket from the first ask in the ticket argument to rejoin it.",
		InputSchema: askInputSchema(),
	}, svc.Ask)
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_answer",
		Description: "Check a ticket from ask. Waits up to 45 seconds for the crew to settle and returns status pending, " +
			"answered (with the answer), or failed. Call it again while pending.",
		InputSchema: getAnswerInputSchema(),
	}, svc.GetAnswer)
	mcp.AddTool(server, &mcp.Tool{
		Name: "export_conversation",
		Description: "Take away a conversation: one conversation id returns that conversation as a zip (the turns " +
			"readable and structured, the stored receipts, the workspace files, and the crew memory facts learned " +
			"in it, with a manifest of what had already expired); several ids return a zip of zips with a manifest " +
			"listing each as included, refused or expired. Built on request and never stored. A zip too large for a " +
			"tool result is not returned; the manifest says so and names the other ways to take it.",
		InputSchema: exportInputSchema(),
	}, svc.ExportConversation)
	return server
}

// NewHandler serves MCP at /mcp (bearer-protected when token is set) plus health probes.
func NewHandler(svc *Service, token string) http.Handler {
	return NewHandlerWithAuth(svc, token, nil)
}

// NewHandlerWithAuth is NewHandler with an authenticator that verifies callers' Kubernetes
// tokens. A nil or non-enforcing authenticator leaves the door as NewHandler makes it.
func NewHandlerWithAuth(svc *Service, token string, auth *identity.Authenticator) http.Handler {
	return NewHandlerWithFront(svc, token, auth, nil)
}

// SignInFront is a sign-in front mounted beside the door: its routes, and the address of
// the protected-resource metadata that a 401 on /mcp points the app to.
type SignInFront struct {
	Handler             http.Handler
	ResourceMetadataURL string
}

// NewHandlerWithFront is NewHandlerWithAuth with a sign-in front (nil for none). The front
// answers the metadata documents and the /oauth routes; the door's 401 names its metadata.
func NewHandlerWithFront(svc *Service, token string, auth *identity.Authenticator, front *SignInFront) http.Handler {
	metadataURL := ""
	if front != nil {
		metadataURL = front.ResourceMetadataURL
	}
	server := NewMCPServer(svc)
	mcpHandler := newMCPHandler(server)
	mux := http.NewServeMux()
	mux.Handle("/mcp", requireCaller(token, auth, metadataURL, mcpHandler))
	if front != nil {
		mux.Handle("/.well-known/", front.Handler)
		mux.Handle("/oauth/", front.Handler)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, "ok") })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, "ready") })
	return mux
}

// newMCPHandler serves server over Streamable HTTP, stateless with JSON responses: no session
// outlives a request, so a client's capabilities are known only when the request carries them.
func newMCPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

// requireBearer gates next behind a static bearer token; an empty token disables the gate
// (for installs that authenticate at the edge, for example a Cloudflare Access front door).
func requireBearer(token, metadataURL string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", withMetadata(`Bearer realm="kubemoot-crew-liaison"`, metadataURL))
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
