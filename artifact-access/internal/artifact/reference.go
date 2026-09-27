// Package artifact defines the discussion artifact reference contract and the
// safe mapping from an object key to a local filesystem path.
//
// A producer (the agent-runtime tool-result path) writes a large tool result to
// the NATS Object Store and publishes a Reference on the discussion instead of
// the raw bytes. Consumers read the reference; the no-network sandbox's sidecar
// (this component) materializes the referenced object to a local file the
// sandbox can read. This package is the single source of truth for that
// contract so the producer (Java) and this consumer (Go) agree byte-for-byte.
package artifact

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// Reference is the descriptor that travels on the discussion in place of bulk
// data. preview/rows/bytes let a consumer often answer without fetching at all.
type Reference struct {
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	ContentType string `json:"contentType,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	Rows        int64  `json:"rows,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Agent       string `json:"agent,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// Message is the envelope carried in a discussion signal's content:
// {"artifact": { ...Reference... }}. A nil Artifact means the content was not an
// artifact reference (an ordinary inline contribution).
type Message struct {
	Artifact *Reference `json:"artifact,omitempty"`
}

// ErrNotReference indicates the JSON did not carry an artifact reference.
var ErrNotReference = errors.New("not an artifact reference")

// ErrUnsafeKey indicates a key that would escape the base directory.
var ErrUnsafeKey = errors.New("unsafe artifact key")

// ParseMessage extracts a Reference from a discussion signal's content. It
// returns ErrNotReference when the content is not a well-formed reference
// envelope (so callers can treat it as ordinary inline content).
func ParseMessage(content string) (Reference, error) {
	var m Message
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return Reference{}, ErrNotReference
	}
	if m.Artifact == nil || m.Artifact.Key == "" {
		return Reference{}, ErrNotReference
	}
	return *m.Artifact, nil
}

// Marshal renders a Reference as the envelope a producer publishes.
func Marshal(ref Reference) (string, error) {
	b, err := json.Marshal(Message{Artifact: &ref})
	return string(b), err
}

// LocalPath maps an object key to a path under baseDir, rejecting any key that
// would traverse outside it. Keys are of the form
// {namespace}/{crew}/{conversationId}/{threadId}/{agent}/{tool}-{seq} and never contain
// ".." or a leading "/"; such keys are rejected outright rather than silently
// rewritten, so a malformed/hostile key can never collide or escape.
func LocalPath(baseDir, key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\x00") {
		return "", ErrUnsafeKey
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return "", ErrUnsafeKey
		}
	}
	full := filepath.Join(baseDir, filepath.FromSlash(key))
	// Defense in depth: the result must stay within baseDir.
	rel, err := filepath.Rel(baseDir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeKey
	}
	return full, nil
}
