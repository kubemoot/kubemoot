package toolsdoor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const toolNope = "nope"

// failingGateway answers every route with status and a JSON body that is not the success shape.
func failingGateway(t *testing.T, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"upstream is down"}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestAGatewayErrorIsNotAnEmptySuccess(t *testing.T) {
	gw := failingGateway(t, http.StatusInternalServerError)
	d := New(Config{Gateway: gw.URL, Enabled: []string{toolFetch}}, turn.NewRegistry(), nil)
	if _, err := d.List(context.Background()); err == nil {
		t.Error("a gateway that answers 500 lists no tools; that is an error, not an empty list")
	}
	text, failed, err := d.call(context.Background(), toolFetch, nil, nil)
	if err != nil || !failed || !strings.Contains(text, "500") || !strings.Contains(text, "upstream is down") {
		t.Errorf("a tool call the gateway failed must be a failed call that says why: %q %v %v", text, failed, err)
	}
}

func TestMissingReportsEveryDeclaredToolWhenTheGatewayCannotBeListed(t *testing.T) {
	gw := failingGateway(t, http.StatusServiceUnavailable)
	d := New(Config{Gateway: gw.URL, Enabled: []string{toolFetch, toolTime}}, turn.NewRegistry(), nil)
	missing, detail := d.Missing(context.Background())
	if len(missing) != 2 || !strings.Contains(detail, gw.URL) {
		t.Errorf("%v %q", missing, detail)
	}
	if m, _ := New(Config{Gateway: gw.URL}, turn.NewRegistry(), nil).Missing(context.Background()); len(m) != 0 {
		t.Errorf("an agent that declares no tools misses none: %v", m)
	}
	none, why := New(Config{Enabled: []string{toolFetch}}, turn.NewRegistry(), nil).Missing(context.Background())
	if len(none) != 1 || why == "" {
		t.Errorf("declared tools with no gateway at all are missing: %v %q", none, why)
	}
	ok := gateway(t)
	if m, _ := New(Config{Gateway: ok.URL, Enabled: []string{toolFetch, toolNope}}, turn.NewRegistry(), nil).
		Missing(context.Background()); len(m) != 1 || m[0] != toolNope {
		t.Errorf("only the tool the gateway lacks: %v", m)
	}
}

func TestACallOutsideAnyTurnIsRefusedWhenTurnsAreRequired(t *testing.T) {
	gw := gateway(t)
	reg := turn.NewRegistry()
	s := connect(t, Config{Gateway: gw.URL, Enabled: []string{toolFetch}, RequireTurn: true}, reg)
	if got := strings.Join(names(t, s), ","); got != toolFetch {
		t.Fatalf("listing needs no turn: %s", got)
	}
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: toolFetch, Arguments: map[string]any{}})
	if err == nil && (res == nil || !res.IsError) {
		t.Fatalf("a call with no open turn must not run: %+v", res)
	}
	reg.Open("k", turn.Info{})
	if res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: toolFetch, Arguments: map[string]any{}}); err != nil || res == nil {
		t.Fatalf("inside a turn the call goes through: %v", err)
	}
}
