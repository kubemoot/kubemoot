// Package threadkey resolves the short keys agents see for a thread's stored tool results.
//
// A stored result lives under the long object key {namespace}/{crew}/{thread}/{agent}/{nnn}-{tool}.
// Agents are shown and type the short form {agent}/{nnn}-{tool}, which cannot be mistyped the way a
// 36-character thread id can. The caller (the agent runtime) names the thread in the tool call's
// _meta; this package turns a short key into the long one inside that thread, lets a long key of
// the same thread through, and refuses a key of any other thread.
package threadkey

import (
	"fmt"
	"strings"
)

// MetaKey is the _meta entry that carries the thread's key prefix, "{namespace}/{crew}/{thread}/".
// It matches the key the agent runtime and the MCP gateway use.
const MetaKey = "workspacePrefix"

// prefixSegments is the number of parts in a thread prefix: namespace, crew and thread.
const prefixSegments = 3

// shortSegments is the number of parts in a short key: agent and call file.
const shortSegments = 2

// Scope is the thread a tool call runs in. The zero Scope has no thread, and keys pass through it
// unchanged.
type Scope struct {
	prefix string
}

// FromMeta reads the thread from a tool call's _meta. A missing entry, or one that is not a
// well-formed prefix, gives the zero Scope, so a bad value behaves like an absent one.
func FromMeta(meta map[string]any) Scope {
	raw, _ := meta[MetaKey].(string)
	if !validPrefix(raw) {
		return Scope{}
	}
	return Scope{prefix: raw}
}

// Prefix is the thread prefix, empty for the zero Scope.
func (s Scope) Prefix() string { return s.prefix }

// Resolve returns the object key to read for the key an agent gave.
//
//   - With no thread, or an empty key, the key is returned as given.
//   - A key under this thread's prefix is returned as given.
//   - A key with an empty, "." or ".." part is refused first, whatever it starts with.
//   - A short key, {agent}/{call file}, becomes the thread prefix plus the key. A * in the call
//     file stays, so patterns work with short keys.
//   - A longer key that is not under this thread's prefix belongs to another thread and is refused.
func (s Scope) Resolve(key string) (string, error) {
	if s.prefix == "" || key == "" {
		return key, nil
	}
	parts := strings.Split(key, "/")
	if !plainParts(parts) {
		return "", fmt.Errorf("key %q is not valid: use the key as shown, like %s", key, example)
	}
	if strings.HasPrefix(key, s.prefix) {
		return key, nil
	}
	switch {
	case len(parts) == shortSegments:
		return s.prefix + key, nil
	case len(parts) > shortSegments:
		return "", fmt.Errorf("key %q belongs to another thread; this thread's results are keyed like %s",
			key, example)
	}
	return key, nil
}

// ResolveAll resolves every value of a table-name to key map, returning a new map. The first key
// that is refused fails the whole call, and the error names the table.
func (s Scope) ResolveAll(artifacts map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(artifacts))
	for table, key := range artifacts {
		resolved, err := s.Resolve(key)
		if err != nil {
			return nil, fmt.Errorf("artifacts[%q]: %w", table, err)
		}
		out[table] = resolved
	}
	return out, nil
}

const example = "k8s-workloads/002-resources_list"

// maxPrefixLen and prefixChars match the MCP gateway, which forwards the entry only when it passes
// the same check (RequesterMeta.validWorkspacePrefix).
const (
	maxPrefixLen = 512
	prefixChars  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-/"
)

func validPrefix(p string) bool {
	if p == "" || len(p) > maxPrefixLen || !strings.HasSuffix(p, "/") {
		return false
	}
	for _, r := range p {
		if !strings.ContainsRune(prefixChars, r) {
			return false
		}
	}
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	return len(parts) == prefixSegments && plainParts(parts)
}

// plainParts is true when no part is empty, "." or "..".
func plainParts(parts []string) bool {
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
