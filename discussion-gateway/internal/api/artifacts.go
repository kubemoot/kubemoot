package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	"github.com/nats-io/nats.go/jetstream"
)

// artifactBucket is the Object Store bucket agents spill large contributions to. Keys
// are <namespace>/<crew>/<threadId>/<agent>/<signal>-<uuid>.
const artifactBucket = "kubemoot_discussion_artifacts"

// errArtifactGone means the artifact is not in the store: never written, or deleted
// with its thread.
var errArtifactGone = errors.New("artifact not found; a thread's artifacts are deleted after it closes")

// inScope reports whether key names an artifact of the crew in scope: it starts with
// <namespace>/<crew>/ and names a thread, an agent and an object below that.
func inScope(scope crewscope.Scope, key string) bool {
	rest, ok := strings.CutPrefix(key, scope.Namespace+"/"+scope.Crew+"/")
	if !ok || strings.Contains(key, "..") {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

// openArtifact opens the artifact at key, or returns errArtifactGone when the bucket or
// the object is not there.
func openArtifact(ctx context.Context, js jetstream.JetStream, key string) (jetstream.ObjectResult, error) {
	store, err := js.ObjectStore(ctx, artifactBucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, errArtifactGone
	}
	if err != nil {
		return nil, err
	}
	obj, err := store.Get(ctx, key)
	if errors.Is(err, jetstream.ErrObjectNotFound) {
		return nil, errArtifactGone
	}
	return obj, err
}

// getArtifact answers GET /api/v1/discussions/{crew}/artifact?key=<key> with the
// artifact's bytes. A key outside the crew's namespace and crew is answered as not
// found, the same as a deleted one.
func (h *Handler) getArtifact(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		httpError(w, http.StatusBadRequest, "key is required")
		return
	}
	if !inScope(scope, key) {
		httpError(w, http.StatusNotFound, errArtifactGone.Error())
		return
	}
	js, ok := h.jetStreamFor(w)
	if !ok {
		return
	}
	obj, err := openArtifact(r.Context(), js, key)
	if errors.Is(err, errArtifactGone) {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		handlerLog.Error(err, "Failed to read an artifact", "crew", scope.Crew, "key", key)
		httpError(w, http.StatusServiceUnavailable, "could not read the artifact store")
		return
	}
	defer func() { _ = obj.Close() }()
	writeArtifact(w, obj)
}

// writeArtifact streams the artifact as text with its size.
func writeArtifact(w http.ResponseWriter, obj jetstream.ObjectResult) {
	w.Header().Set(headerContentType, "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if info, err := obj.Info(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatUint(info.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, obj); err != nil {
		handlerLog.V(1).Info("Could not send the artifact", keyError, err.Error())
	}
}
