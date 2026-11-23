// Package registry reads the per-crew source registry the operator writes, resolves
// each source's connection, and swaps the whole set atomically when the file changes.
package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// The result counts a query may ask for, and the count it gets when it names none and the
// source sets no default.
const (
	MinTopK     = 1
	MaxTopK     = 100
	DefaultTopK = 5
)

// ClampTopK keeps a configured result count inside what a query accepts.
func ClampTopK(n int) int { return max(MinTopK, min(n, MaxTopK)) }

// nameRE is a source name that is safe in a URL path segment (a Kubernetes name).
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// VectorStore says where a source's vectors live.
type VectorStore struct {
	Type       string `json:"type"`
	Endpoint   string `json:"endpoint"`
	Collection string `json:"collection"`
	Dimensions int    `json:"dimensions"`
	// CredentialsDir is a mounted Secret directory holding the files "username" and
	// "password". Registry files carry no secrets.
	CredentialsDir string `json:"credentialsDir,omitempty"`
}

// Embedding says how to embed a question for a source: the OpenAI-compatible
// embeddings endpoint and the model the source's vectors were made with.
type Embedding struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

// Source is one RAGSource a crew's query service serves.
type Source struct {
	Name        string      `json:"name"`
	Namespace   string      `json:"namespace"`
	VectorStore VectorStore `json:"vectorStore"`
	Embedding   Embedding   `json:"embedding"`
	DefaultTopK int         `json:"defaultTopK,omitempty"`
}

// File is the registry file's content.
type File struct {
	Sources []Source `json:"sources"`
}

// Parse reads and validates a registry file. Unknown fields are an error, so a
// misspelled key cannot silently leave a default in place.
func Parse(data []byte) (*File, error) {
	var f File
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	seen := map[string]bool{}
	for i := range f.Sources {
		if err := validate(&f.Sources[i], seen); err != nil {
			return nil, err
		}
	}
	return &f, nil
}

func validate(s *Source, seen map[string]bool) error {
	switch {
	case !nameRE.MatchString(s.Name):
		return fmt.Errorf("registry: source name %q is not a valid name", s.Name)
	case seen[s.Name]:
		return fmt.Errorf("registry: source %q is listed twice", s.Name)
	case s.VectorStore.Collection == "":
		return fmt.Errorf("registry: source %q has no collection", s.Name)
	case s.Embedding.Endpoint == "" || s.Embedding.Model == "":
		return fmt.Errorf("registry: source %q needs an embedding endpoint and model", s.Name)
	case s.DefaultTopK < 0 || s.DefaultTopK > MaxTopK:
		return fmt.Errorf("registry: source %q defaultTopK must be 0 to %d", s.Name, MaxTopK)
	}
	if s.VectorStore.Type == "" {
		s.VectorStore.Type = "pgvector"
	}
	if s.VectorStore.Type != "pgvector" {
		return fmt.Errorf("registry: source %q has unsupported vector store %q", s.Name, s.VectorStore.Type)
	}
	seen[s.Name] = true
	return nil
}

// Table is the table the Java indexer writes a collection to.
func (s *Source) Table() string { return "data_" + s.VectorStore.Collection }

// TopK is the result count for a query that names none.
func (s *Source) TopK(fallback int) int {
	if s.DefaultTopK > 0 {
		return s.DefaultTopK
	}
	return fallback
}

// Entry is a source with its database connection resolved.
type Entry struct {
	Source
	Conn Conn
}

// Snapshot is one immutable generation of the registry. Readers hold a snapshot for a
// whole request, so a swap never changes a request in flight.
type Snapshot struct {
	// Version identifies the registry file's content (a short digest).
	Version string
	// Fingerprint covers the resolved entries, credentials included, so a rotated
	// Secret makes a new snapshot even when the file is unchanged.
	Fingerprint string
	Entries     map[string]*Entry
	order       []string
}

// Names lists the source names in file order.
func (s *Snapshot) Names() []string { return append([]string(nil), s.order...) }

// Lookup finds a source by name.
func (s *Snapshot) Lookup(name string) (*Entry, bool) {
	e, ok := s.Entries[name]
	return e, ok
}

// Build resolves a parsed file into a snapshot.
func Build(data []byte, env Env) (*Snapshot, error) {
	f, err := Parse(data)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Version: digest(data), Entries: map[string]*Entry{}}
	for _, src := range f.Sources {
		conn, err := ResolveConn(src.VectorStore, env)
		if err != nil {
			return nil, fmt.Errorf("registry: source %q: %w", src.Name, err)
		}
		snap.Entries[src.Name] = &Entry{Source: src, Conn: conn}
		snap.order = append(snap.order, src.Name)
	}
	snap.Fingerprint, err = fingerprint(snap)
	return snap, err
}

// fingerprint digests the resolved entries and their passwords (which stay out of the
// entries' JSON), in file order.
func fingerprint(s *Snapshot) (string, error) {
	h := sha256.New()
	for _, name := range s.order {
		e := s.Entries[name]
		b, err := json.Marshal(e)
		if err != nil {
			return "", errors.New("registry: fingerprint failed")
		}
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(e.Conn.Password))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}
