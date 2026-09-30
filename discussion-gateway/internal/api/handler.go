package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	headerContentType    = "Content-Type"
	mimeJSON             = "application/json"
	errNATSNotConfigured = "NATS not configured"
	agentNameGateway     = "discussion-gateway"
	// errorChannel is the channel token of the synthetic error thread.
	errorChannel = "broadcast"
)

var handlerLog = logf.Log.WithName("api-handler")

// Handler serves the Discussion API endpoints for crews in one namespace.
type Handler struct {
	natsClient *natsclient.Client
	requests   *requestLog
	namespace  string
	// streamsEnd ends every open stream when it is done, such as at shutdown.
	streamsEnd context.Context
}

// NewHandler creates a new API handler whose NATS subjects are scoped to namespace.
func NewHandler(natsClient *natsclient.Client, namespace string) *Handler {
	return &Handler{
		natsClient: natsClient,
		requests:   newRequestLog(time.Hour),
		namespace:  namespace,
		streamsEnd: context.Background(),
	}
}

// EndStreamsOn makes every open stream end when ctx is done. Other requests are not
// affected, so a question posted during a shutdown is still queued.
func (h *Handler) EndStreamsOn(ctx context.Context) *Handler {
	h.streamsEnd = ctx
	return h
}

// scopeFor returns the crew's scope in this gateway's namespace, writing a 400
// and returning false when the crew name is not a valid subject token.
func (h *Handler) scopeFor(w http.ResponseWriter, crew string) (crewscope.Scope, bool) {
	scope, err := crewscope.New(h.namespace, crew)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return crewscope.Scope{}, false
	}
	return scope, true
}

// RegisterRoutes registers all API routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.healthHandler)
	mux.HandleFunc("GET /ready", h.readyHandler)
	mux.HandleFunc("POST /api/v1/discussions/{crew}", h.postDiscussion)
	mux.HandleFunc("GET /api/v1/discussions/{crew}/{conversationId}/stream", h.streamDiscussion)
}

// postDiscussionRequest is the body for starting a discussion.
type postDiscussionRequest struct {
	Message        string `json:"message"`
	ConversationID string `json:"conversationId,omitempty"`
}

// postDiscussionResponse is returned after starting a discussion.
type postDiscussionResponse struct {
	ConversationID string `json:"conversationId"`
	// RequestedAt is when the gateway queued the request. A client sends it back as the
	// stream's since parameter, so any gateway replica, including one started after the
	// request, can tell this turn's thread from an earlier turn's.
	RequestedAt string `json:"requestedAt"`
}

func (h *Handler) healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyHandler is the readiness probe: ready only while connected to NATS, so the
// Service sends questions and streams only to a gateway that can serve them.
func (h *Handler) readyHandler(w http.ResponseWriter, r *http.Request) {
	switch {
	case !h.natsClient.IsConfigured():
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready", "reason": errNATSNotConfigured})
	case !h.natsClient.Connected():
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready", "reason": "not connected to NATS"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

func (h *Handler) postDiscussion(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return
	}
	crew := scope.Crew

	var req postDiscussionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		httpError(w, http.StatusBadRequest, "message is required")
		return
	}

	if !h.natsClient.IsConfigured() {
		httpError(w, http.StatusServiceUnavailable, errNATSNotConfigured)
		return
	}

	// Use provided conversationId or generate one
	conversationID := req.ConversationID
	if conversationID == "" {
		conversationID = uuid.New().String()
	}

	// Publish to KUBEMOOT_REQUEST stream; the coordinator pulls and processes FIFO
	subject := scope.RequestSubject()
	reqBody, _ := json.Marshal(map[string]interface{}{
		"message":        req.Message,
		"conversationId": conversationID,
		"namespace":      scope.Namespace,
		"crew":           crew,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"use_rag":        true,
		"use_tools":      true,
	})

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := h.natsClient.Publish(ctx, subject, reqBody); err != nil {
		handlerLog.Error(err, "Failed to queue request to NATS", "crew", crew, "subject", subject)
		h.publishCoordinatorError(scope, conversationID, fmt.Sprintf("failed to queue request: %v", err))
		httpError(w, http.StatusServiceUnavailable, "failed to queue discussion request")
		return
	}

	requestedAt := h.requests.record(conversationID)
	handlerLog.Info("Queued discussion request", "crew", crew, "conversationId", conversationID, "subject", subject)

	// Return immediately; the client opens the SSE stream to follow progress.
	writeJSON(w, http.StatusOK, postDiscussionResponse{
		ConversationID: conversationID,
		RequestedAt:    requestedAt.UTC().Format(time.RFC3339Nano),
	})
}

func (h *Handler) streamDiscussion(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("conversationId")
	if conversationID == "" {
		httpError(w, http.StatusBadRequest, "conversationId is required")
		return
	}
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return
	}
	req, err := h.parseStreamRequest(r, conversationID)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !h.natsClient.IsConfigured() {
		httpError(w, http.StatusServiceUnavailable, errNATSNotConfigured)
		return
	}

	js, err := h.natsClient.JetStream()
	if err != nil {
		handlerLog.Error(err, "Failed to get JetStream")
		httpError(w, http.StatusServiceUnavailable, "NATS connection failed")
		return
	}

	emit, ok := sseEmitter(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(h.streamsEnd, cancel)()

	// Stream blocks until thread_close, the NATS subscription ends, the client leaves,
	// or the gateway shuts down.
	if err := streamDiscussion(ctx, js, scope, req, emit); err != nil && !errors.Is(err, context.Canceled) {
		handlerLog.Info("Stream ended before the thread closed", "crew", scope.Crew, "conversationId", conversationID, "error", err.Error())
	}
}

// sseEmitter sets the SSE response headers and returns a function that writes and
// flushes one event, or false after answering 500 when w cannot stream.
func sseEmitter(w http.ResponseWriter, r *http.Request) (func(SSEEvent), bool) {
	w.Header().Set(headerContentType, "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx buffering off

	// CORS for browser-based and external clients
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "streaming not supported")
		return nil, false
	}
	return func(event SSEEvent) {
		if writeSSE(w, event) == nil {
			flusher.Flush()
		}
	}, true
}

// writeSSE writes one event, with its id field when it has one.
func writeSSE(w io.Writer, event SSEEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", event.ID); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

// parseStreamRequest reads which turn a stream follows. The resume point is the
// standard Last-Event-ID header, or the lastEventId query parameter for clients that
// cannot set headers. The turn's request time is the since parameter the POST returned,
// or, without it, the time this gateway queued the conversation's latest request.
func (h *Handler) parseStreamRequest(r *http.Request, conversationID string) (streamRequest, error) {
	req := streamRequest{conversationID: conversationID}
	lastEventID := r.Header.Get("Last-Event-ID")
	if lastEventID == "" {
		lastEventID = r.URL.Query().Get("lastEventId")
	}
	resume, err := parseResumePoint(lastEventID)
	if err != nil {
		return req, err
	}
	req.resume = resume
	since := r.URL.Query().Get("since")
	if since == "" {
		req.notBefore = h.requests.notBefore(conversationID)
		return req, nil
	}
	t, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return req, fmt.Errorf("since %q is not an RFC 3339 time", since)
	}
	req.notBefore = t.Add(-clockSkew)
	return req, nil
}

// publishCoordinatorError publishes synthetic thread_start + thread_close messages
// to NATS so the SSE stream terminates immediately with an error instead of hanging.
// Best-effort: logs errors but does not propagate them.
func (h *Handler) publishCoordinatorError(scope crewscope.Scope, conversationID, errMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	threadID := "gateway-error-" + uuid.New().String()[:8]
	subject := scope.DiscussSubject(errorChannel, threadID)

	// thread_start lets the SSE consumer find the thread by conversationId, the
	// synthesis carries the error, and thread_close ends the stream.
	messages := []map[string]interface{}{
		{"messageType": "thread_start", "metadata": map[string]string{"conversationId": conversationID}},
		{"messageType": "synthesis", "content": fmt.Sprintf("The discussion could not be started: %s. Please try again.", errMsg)},
		{"messageType": "thread_close"},
	}
	for _, m := range messages {
		m["messageId"], m["threadId"], m["agentName"] = uuid.New().String(), threadID, agentNameGateway
		data, err := json.Marshal(m)
		if err == nil {
			err = h.natsClient.Publish(ctx, subject, data)
		}
		if err != nil {
			handlerLog.Info("Failed to publish the coordinator error", "messageType", m["messageType"], "error", err.Error())
			return
		}
	}

	handlerLog.Info("Published coordinator error to NATS",
		"namespace", scope.Namespace, "crew", scope.Crew, "conversationId", conversationID, "threadId", threadID, "error", errMsg)
}

func httpError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

// writeJSON writes v as the JSON response with the status code. An encoding failure
// means the client went away; there is no one left to tell.
func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set(headerContentType, mimeJSON)
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		handlerLog.V(1).Info("Could not write the response", "error", err.Error())
	}
}
