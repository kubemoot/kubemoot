package liaison

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/javajon/kubemoot/operator/pkg/sse"
)

// Event is the part of a discussion gateway stream event the liaison acts on.
// Agent names, signals, and summaries are deliberately not decoded.
type Event struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Event types emitted by the discussion gateway that the liaison reacts to.
const (
	EventFinding   = "finding"
	EventSynthesis = "synthesis"
	EventError     = "error"
	EventDone      = "done"
)

// Gateway talks to a crew's discussion gateway over its HTTP API.
type Gateway struct {
	client  *http.Client
	baseURL func(namespace, crew string) string
}

// NewGateway returns a client that reaches each crew's gateway through its
// in-cluster Service, <crew>-discussion in the crew's namespace.
func NewGateway() *Gateway {
	return &Gateway{
		client: &http.Client{Timeout: 0}, // streams are long-lived; callers bound them with ctx
		baseURL: func(namespace, crew string) string {
			return fmt.Sprintf("http://%s-discussion.%s.svc/api/v1/discussions/%s", crew, namespace, crew)
		},
	}
}

// NewGatewayAt returns a client whose gateway URLs come from resolve; tests use it.
func NewGatewayAt(resolve func(namespace, crew string) string) *Gateway {
	g := NewGateway()
	g.baseURL = resolve
	return g
}

// Start opens a discussion and returns its conversation id.
func (g *Gateway) Start(ctx context.Context, namespace, crew, question string) (string, error) {
	body, err := json.Marshal(map[string]string{"message": question})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL(namespace, crew), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("crew %s/%s gateway unreachable: %w", namespace, crew, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("crew %s/%s gateway refused the question: %s %s", namespace, crew, resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		ConversationID string `json:"conversationId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ConversationID == "" {
		return "", fmt.Errorf("crew %s/%s gateway returned no conversation id", namespace, crew)
	}
	return out.ConversationID, nil
}

// Follow reads the discussion's event stream and hands each event to onEvent until
// the stream ends, ctx is done, or onEvent returns false.
func (g *Gateway) Follow(ctx context.Context, namespace, crew, conversation string, onEvent func(Event) bool) error {
	url := g.baseURL(namespace, crew) + "/" + conversation + "/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("crew %s/%s stream unreachable: %w", namespace, crew, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("crew %s/%s stream refused: %s", namespace, crew, resp.Status)
	}
	return readEvents(resp.Body, onEvent)
}

// readEvents decodes each data payload of the stream and hands it to onEvent.
func readEvents(r io.Reader, onEvent func(Event) bool) error {
	return sse.Data(r, func(data string) bool {
		var ev Event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return true // a malformed line is the gateway's problem, not the ticket's
		}
		return onEvent(ev)
	})
}
