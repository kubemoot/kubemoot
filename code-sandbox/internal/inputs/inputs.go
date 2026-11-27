// Package inputs resolves the stored results a program asks for by key.
//
// A stored result lives at {namespace}/{crew}/{thread}/{agent}/{nnn}-{tool} under a workspace
// directory. The agent runtime names the calling discussion's thread in the tool call's _meta
// (workspacePrefix, "{namespace}/{crew}/{thread}/"); a program names a result by the short key
// {agent}/{nnn}-{tool} that the discussion's list of stored results shows. A key resolves inside the
// calling thread only; the thread comes from _meta and never from the key, so a program cannot name
// another discussion's result.
//
// The prefix rules match workspace-access's threadkey package, which reads the same _meta entry.
package inputs

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// MetaKey is the _meta entry that carries the thread's key prefix.
const MetaKey = "workspacePrefix"

// MaxInputs is the most results one program may name.
const MaxInputs = 32

const (
	prefixSegments = 3
	shortSegments  = 2
	maxPrefixLen   = 512
	prefixChars    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-/"
	example        = "k8s-config/001-resources_get"
)

// Input is one stored result a program reads, shown to the program as inputs/Name. File is the result,
// already opened and checked, so the copy reads what was checked; the caller closes it.
type Input struct {
	Name string
	File *os.File
}

// Close closes the files of inputs.
func Close(in []Input) {
	for _, i := range in {
		if i.File != nil {
			_ = i.File.Close()
		}
	}
}

// Resolver finds stored results under the workspace directories, tried in order.
type Resolver struct {
	Roots []string
}

// PrefixFromMeta is the thread prefix a call's _meta names, or empty when it has none or the value
// is not a well-formed prefix.
func PrefixFromMeta(meta map[string]any) string {
	raw, _ := meta[MetaKey].(string)
	if !validPrefix(raw) {
		return ""
	}
	return raw
}

// Resolve turns the keys a program named into files of the thread prefix. The first key that is
// refused fails the whole call, and the error says why.
func (r Resolver) Resolve(prefix string, keys []string) ([]Input, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if prefix == "" {
		return nil, fmt.Errorf("inputs: this call is not part of a discussion, so there are no stored results to read")
	}
	if len(keys) > MaxInputs {
		return nil, fmt.Errorf("inputs: %d keys given; name at most %d", len(keys), MaxInputs)
	}
	out := make([]Input, 0, len(keys))
	seen := map[string]bool{}
	for _, key := range keys {
		in, err := r.one(prefix, key)
		if err != nil {
			Close(out)
			return nil, err
		}
		if seen[in.Name] {
			Close([]Input{in})
			continue
		}
		seen[in.Name] = true
		out = append(out, in)
	}
	return out, nil
}

func (r Resolver) one(prefix, key string) (Input, error) {
	name, err := shortKey(prefix, key)
	if err != nil {
		return Input{}, err
	}
	for _, root := range r.Roots {
		f, err := OpenFile(root, strings.Split(prefix+name, "/"))
		switch {
		case err == nil:
			return Input{Name: name, File: f}, nil
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return Input{}, fmt.Errorf("inputs: %q is refused: %w", key, err)
		}
	}
	return Input{}, fmt.Errorf("inputs: no stored result %q in this discussion; keys look like %s", key, example)
}

// shortKey is the {agent}/{nnn}-{tool} form of a key, refusing a key of another thread.
func shortKey(prefix, key string) (string, error) {
	key = strings.TrimPrefix(key, prefix)
	parts := strings.Split(key, "/")
	switch {
	case !plainParts(parts):
		return "", fmt.Errorf("inputs: key %q is not valid; use the key as shown, like %s", key, example)
	case strings.ContainsAny(key, "*?["):
		return "", fmt.Errorf("inputs: key %q is a pattern; name each stored result by its key", key)
	case len(parts) != shortSegments:
		return "", fmt.Errorf("inputs: key %q is not a result of this discussion; keys look like %s", key, example)
	}
	return key, nil
}

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
