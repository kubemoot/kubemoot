/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import "fmt"

// redacted is what a Credential prints and marshals as.
const redacted = "[redacted]"

// Credential is a bearer token a verifier accepted and a door keeps in memory for the
// length of a turn. The token is unexported and the type has no JSON tags, so a struct that
// holds one cannot print or marshal it by accident: every formatting verb and every
// encoder sees "[redacted]". Token is the one way out, for the call that must send it.
type Credential struct{ token string }

// NewCredential wraps a token.
func NewCredential(token string) Credential { return Credential{token: token} }

// Token returns the bearer token. Use it only to set the Authorization header of the one
// request it is for; never log it, trace it, publish it or store it.
func (c Credential) Token() string { return c.token }

// Empty reports whether there is no token.
func (c Credential) Empty() bool { return c.token == "" }

// String is redacted.
func (c Credential) String() string { return redacted }

// GoString is redacted.
func (c Credential) GoString() string { return redacted }

// Format is redacted for every verb, so %x or %q cannot reveal the bytes.
func (c Credential) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// MarshalJSON is redacted.
func (c Credential) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText is redacted.
func (c Credential) MarshalText() ([]byte, error) { return []byte(redacted), nil }
