package shim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Task states of the A2A HTTP+JSON binding.
const (
	stateCompleted = "TASK_STATE_COMPLETED"
	stateFailed    = "TASK_STATE_FAILED"
	stateCanceled  = "TASK_STATE_CANCELED"
	stateRejected  = "TASK_STATE_REJECTED"
)

const (
	maxBody    = 4 << 20
	profileURI = "https://kubemoot.org/a2a/turn/v1"
	mediaType  = "application/a2a+json"
)

var errCanceled = errors.New("canceled by the sidecar")

// Server serves the agent card and the A2A operations over the HTTP+JSON binding.
type Server struct {
	cfg       Config
	runner    *Runner
	advertise string

	mu     sync.Mutex
	active map[string]context.CancelCauseFunc
}

// NewServer returns a Server that advertises advertiseURL in its agent card.
func NewServer(cfg Config, runner *Runner, advertiseURL string) *Server {
	return &Server{cfg: cfg, runner: runner, advertise: advertiseURL, active: map[string]context.CancelCauseFunc{}}
}

// Handler routes the card, SendMessage and CancelTask.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/agent-card.json", s.card)
	mux.HandleFunc("POST /message:send", s.send)
	mux.HandleFunc("POST /tasks/{action}", s.cancel)
	return mux
}

func (s *Server) card(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": s.cfg.cardName(), "description": "Runs a command-line agent once per turn for the Moot Mesh.",
		"version": "0.0.0", "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"},
		"skills":       []any{},
		"capabilities": map[string]any{"streaming": false, "extensions": []any{map[string]any{"uri": profileURI}}},
		"supportedInterfaces": []any{map[string]any{
			"url": s.advertise, "protocolBinding": "HTTP+JSON", "protocolVersion": "1.0"}},
	})
}

// begin registers a turn so CancelTask can find it. It returns false when the shim is full.
func (s *Server) begin(id string, cancel context.CancelCauseFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active) >= s.cfg.concurrency() {
		return false
	}
	s.active[id] = cancel
	return true
}

func (s *Server) end(id string) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	turn, err := ParseTurn(body, s.cfg.maxDeadline())
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	id := newTaskID()
	// The turn ends when the sidecar hangs up, when it cancels, or at its deadline.
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	if !s.begin(id, cancel) {
		writeTask(w, id, turn.ContextID, stateRejected, "the harness is running its limit of turns")
		return
	}
	defer s.end(id)
	ctx, stop := context.WithTimeoutCause(ctx, turn.Deadline, fmt.Errorf("the turn deadline of %s passed", turn.Deadline))
	defer stop()
	answer, err := s.runner.Run(ctx, turn)
	writeTask(w, id, turn.ContextID, outcome(err), pick(err, answer))
}

// outcome maps the run's error to the task state.
func outcome(err error) string {
	switch {
	case err == nil:
		return stateCompleted
	case errors.Is(err, errCanceled), errors.Is(err, context.Canceled):
		return stateCanceled
	default:
		return stateFailed
	}
}

func pick(err error, answer string) string {
	if err != nil {
		return err.Error()
	}
	return answer
}

// cancel handles POST /tasks/{id}:cancel.
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("action"), ":cancel")
	if !ok {
		writeStatus(w, http.StatusNotFound, "NOT_FOUND", "no such operation")
		return
	}
	s.mu.Lock()
	stop := s.active[id]
	s.mu.Unlock()
	if stop == nil {
		writeStatus(w, http.StatusNotFound, "NOT_FOUND", "no running task "+id)
		return
	}
	stop(errCanceled)
	writeTask(w, id, "", stateCanceled, "canceled")
}

func writeTask(w http.ResponseWriter, id, contextID, state, text string) {
	task := map[string]any{
		"id": id, "contextId": contextID,
		"status": map[string]any{"state": state, "timestamp": time.Now().UTC().Format(time.RFC3339),
			"message": map[string]any{"role": "ROLE_AGENT", "parts": []any{map[string]any{partText: text}}}},
	}
	if state == stateCompleted {
		task["artifacts"] = []any{map[string]any{"artifactId": "answer", "parts": []any{map[string]any{partText: text}}}}
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func writeStatus(w http.ResponseWriter, code int, status, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"code": code, "status": status, "message": msg}})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newTaskID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "task-" + hex.EncodeToString(b)
}
