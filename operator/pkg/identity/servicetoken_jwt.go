/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// The bounds on a token the shape check reads before any call goes out.
const (
	maxTokenLength   = 8192
	maxSubjectLength = 256
	jwtParts         = 3
)

// tokenClaims is the payload of a token, read without verifying a signature the door
// cannot verify: the service that issued the token checks it, and the payload is trusted
// only after that service accepted this exact token.
type tokenClaims struct {
	payload map[string]any
}

// parseTokenClaims reads the payload of a JWT. It reports false for anything that is not
// three dot separated parts with a JSON object payload, or is larger than maxTokenLength.
func parseTokenClaims(token string) (tokenClaims, bool) {
	if len(token) > maxTokenLength {
		return tokenClaims{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != jwtParts || parts[1] == "" {
		return tokenClaims{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return tokenClaims{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil || payload == nil {
		return tokenClaims{}, false
	}
	return tokenClaims{payload: payload}, true
}

// String returns the string at a dotted path such as "upstream_claims.sub". It reports
// false when any step is missing or is not an object, or the end is not a non-empty string
// of at most maxSubjectLength bytes.
func (c tokenClaims) String(path string) (string, bool) {
	var cur any = c.payload
	for _, step := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = obj[step]; !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	if !ok || s == "" || len(s) > maxSubjectLength {
		return "", false
	}
	return s, true
}

// Expiry is the token's exp claim. A token with none, or one that is not a number, has no
// expiry and the door refuses it: a token that never expires is not cached or trusted.
func (c tokenClaims) Expiry() (time.Time, bool) {
	n, ok := c.payload["exp"].(json.Number)
	if !ok {
		return time.Time{}, false
	}
	secs, err := n.Float64()
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(secs), 0), true
}

// Audiences are the token's aud claim, one string or a list of strings.
func (c tokenClaims) Audiences() []string {
	switch aud := c.payload["aud"].(type) {
	case string:
		return []string{aud}
	case []any:
		out := make([]string, 0, len(aud))
		for _, a := range aud {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// isKubernetes reports whether the payload is a Kubernetes service account token or names a
// Kubernetes subject. Such a token is never sent to an outside service, whatever the
// configured claim path or audiences.
func (c tokenClaims) isKubernetes() bool {
	if _, ok := c.payload["kubernetes.io"]; ok {
		return true
	}
	sub, _ := c.payload["sub"].(string)
	return strings.HasPrefix(sub, "system:")
}

// TokenExpiry is the exp claim of a JWT bearer token, read without verifying a signature:
// use it only on a token a verifier already accepted, to bound how long it is kept. It
// reports false for a token that is not a JWT or has no usable exp.
func TokenExpiry(token string) (time.Time, bool) {
	claims, ok := parseTokenClaims(token)
	if !ok {
		return time.Time{}, false
	}
	return claims.Expiry()
}
