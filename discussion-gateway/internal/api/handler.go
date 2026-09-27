package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	natsclient "github.com/javajon/kubemoot/discussion-gateway/internal/nats"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	headerContentType    = "Content-Type"
	mimeJSON             = "application/json"
	errNATSNotConfigured = "NATS not configured"
	agentNameGateway     = "discussion-gateway"
)

var handlerLog = logf.Log.WithName("api-handler")

// Handler serves the Discussion API endpoints.
type Handler struct {
	natsClient *natsclient.Client
	requests   *requestLog
}

// NewHandler creates a new API handler.
func NewHandler(natsClient *natsclient.Client) *Handler {
	return &Handler{
		natsClient: natsClient,
		requests:   newRequestLog(time.Hour),
	}
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
}

func (h *Handler) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerContentType, mimeJSON)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) readyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerContentType, mimeJSON)

	if !h.natsClient.IsConfigured() {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "not ready", "reason": errNATSNotConfigured})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

func (h *Handler) postDiscussion(w http.ResponseWriter, r *http.Request) {
	crew := r.PathValue("crew")
	if crew == "" {
		httpError(w, http.StatusBadRequest, "crew is required")
		return
	}

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

	// Publish to KUBEMOOT_REQUEST stream — coordinator pulls and processes FIFO
	subject := fmt.Sprintf("kubemoot.request.%s", crew)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"message":        req.Message,
		"conversationId": conversationID,
		"crew":           crew,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"use_rag":        true,
		"use_tools":      true,
	})

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := h.natsClient.Publish(ctx, subject, reqBody); err != nil {
		handlerLog.Error(err, "Failed to queue request to NATS", "crew", crew, "subject", subject)
		h.publishCoordinatorError(crew, conversationID, fmt.Sprintf("failed to queue request: %v", err))
		httpError(w, http.StatusServiceUnavailable, "failed to queue discussion request")
		return
	}

	h.requests.record(conversationID)
	handlerLog.Info("Queued discussion request", "crew", crew, "conversationId", conversationID, "subject", subject)

	// Return immediately with conversationId — client opens SSE stream to follow progress
	w.Header().Set(headerContentType, mimeJSON)
	json.NewEncoder(w).Encode(postDiscussionResponse{
		ConversationID: conversationID,
	})
}

func (h *Handler) streamDiscussion(w http.ResponseWriter, r *http.Request) {
	crew := r.PathValue("crew")
	conversationID := r.PathValue("conversationId")
	if crew == "" || conversationID == "" {
		httpError(w, http.StatusBadRequest, "crew and conversationId are required")
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

	// Set SSE headers
	w.Header().Set(headerContentType, "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx buffering off

	// CORS for Tauri and external clients
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	emit := func(event SSEEvent) {
		data, err := json.Marshal(event)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Stream blocks until thread_close or context cancellation.
	// Uses conversationId to find the matching thread via thread_start metadata.
	notBefore := h.requests.notBefore(conversationID)
	if err := streamDiscussion(ctx, js, crew, conversationID, notBefore, emit); err != nil && err != context.Canceled {
		handlerLog.V(1).Info("Stream ended", "crew", crew, "conversationId", conversationID, "error", err)
	}
}

// publishCoordinatorError publishes synthetic thread_start + thread_close messages
// to NATS so the SSE stream terminates immediately with an error instead of hanging.
// Best-effort: logs errors but does not propagate them.
func (h *Handler) publishCoordinatorError(crew, conversationID, errMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	threadID := "gateway-error-" + uuid.New().String()[:8]
	subject := fmt.Sprintf("kubemoot.discuss.%s.%s", crew, threadID)

	// Publish thread_start so SSE consumer can find the thread by conversationId
	threadStart, _ := json.Marshal(map[string]interface{}{
		"messageType": "thread_start",
		"threadId":    threadID,
		"agentName":   agentNameGateway,
		"metadata": map[string]string{
			"conversationId": conversationID,
		},
	})
	if err := h.natsClient.Publish(ctx, subject, threadStart); err != nil {
		handlerLog.V(1).Info("Failed to publish coordinator error thread_start", "error", err)
		return
	}

	// Publish synthesis with error message
	synthesis, _ := json.Marshal(map[string]interface{}{
		"messageType": "synthesis",
		"threadId":    threadID,
		"agentName":   agentNameGateway,
		"content":     fmt.Sprintf("The discussion could not be started: %s. Please try again.", errMsg),
	})
	h.natsClient.Publish(ctx, subject, synthesis)

	// Publish thread_close so SSE stream terminates
	threadClose, _ := json.Marshal(map[string]interface{}{
		"messageType": "thread_close",
		"threadId":    threadID,
		"agentName":   agentNameGateway,
	})
	h.natsClient.Publish(ctx, subject, threadClose)

	handlerLog.Info("Published coordinator error to NATS",
		"crew", crew, "conversationId", conversationID, "threadId", threadID, "error", errMsg)
}

func httpError(w http.ResponseWriter, code int, message string) {
	w.Header().Set(headerContentType, mimeJSON)
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
