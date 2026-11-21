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

// The Object Store buckets that hold a discussion's workspace. Keys are
// <namespace>/<crew>/<threadId>/<agent>/<signal>-<uuid>. Writers store into workspaceBucket;
// artifactBucket is the bucket they used before, read second until its TTL drains it.
const (
	workspaceBucket = "kubemoot_workspace"
	artifactBucket  = "kubemoot_discussion_artifacts"
)

// readBuckets are the buckets a read tries, in order.
var readBuckets = []string{workspaceBucket, artifactBucket}

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

// openArtifact opens the artifact at key from the first bucket that has it, or returns
// errArtifactGone when no bucket or object is there.
func openArtifact(ctx context.Context, js jetstream.JetStream, key string) (jetstream.ObjectResult, error) {
	for _, bucket := range readBuckets {
		obj, err := openFrom(ctx, js, bucket, key)
		if errors.Is(err, errArtifactGone) {
			continue
		}
		return obj, err
	}
	return nil, errArtifactGone
}

// openFrom opens key from one bucket; a bucket or an object that is not there is errArtifactGone.
func openFrom(ctx context.Context, js jetstream.JetStream, bucket, key string) (jetstream.ObjectResult, error) {
	store, err := js.ObjectStore(ctx, bucket)
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

// getWorkspace answers GET /api/v1/discussions/{crew}/workspace?key=<key> with the stored
// result's bytes. A key outside the crew's namespace and crew is answered as not found, the
// same as a deleted one.
func (h *Handler) getWorkspace(w http.ResponseWriter, r *http.Request) {
	h.getStoredResult(w, r)
}

// getArtifactDeprecated answers the old route, GET /api/v1/discussions/{crew}/artifact, as
// getWorkspace does and says so: a Deprecation header and a Link to the new route on every
// answer, and one log line for the life of the process. The route is removed in the next release.
func (h *Handler) getArtifactDeprecated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", "</api/v1/discussions/"+r.PathValue("crew")+"/workspace>; rel=\"successor-version\"")
	h.deprecatedRoute.Do(func() {
		handlerLog.Info("The /artifact route is deprecated and is removed in the next release; use /workspace")
	})
	h.getStoredResult(w, r)
}

// getStoredResult serves a stored result; getWorkspace and getArtifactDeprecated both call it.
func (h *Handler) getStoredResult(w http.ResponseWriter, r *http.Request) {
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
	if !h.mayReadKey(w, r, js, scope, key) {
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

// mayReadKey lets the caller read a stored result when its thread belongs to a conversation
// they may read. A thread whose conversation cannot be found, because its messages are gone,
// belongs to no one a gateway that verifies callers can name, so it is not served.
func (h *Handler) mayReadKey(
	w http.ResponseWriter, r *http.Request, js jetstream.JetStream, scope crewscope.Scope, key string,
) bool {
	if !h.enforcing() {
		return true
	}
	thread := strings.Split(strings.TrimPrefix(key, scope.Namespace+"/"+scope.Crew+"/"), "/")[0]
	conversation, found, err := conversationOfThread(r.Context(), js, scope, thread)
	if err != nil {
		h.failedCheck(w, scope, err)
		return false
	}
	if !found {
		httpError(w, http.StatusNotFound, errArtifactGone.Error())
		return false
	}
	return h.mayRead(w, r, scope, conversation, opWorkspace)
}

// conversationOfThread is the conversation the thread's thread_start names.
func conversationOfThread(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, thread string,
) (string, bool, error) {
	conversation := ""
	err := readSubject(ctx, js, scope.DiscussSubject("*", thread), func(m discussMsg) {
		if m.data.MessageType == msgThreadStart && conversation == "" {
			conversation = conversationOf(m.data)
		}
	})
	return conversation, conversation != "", err
}
