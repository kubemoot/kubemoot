/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ompi

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// runtimeFront is the front matter of the runtime projection (spec section 7.2
// and Appendix B). It leaves out scope, checksum, the audit fields and the
// Kubemoot extension, which stay in the interchange form.
type runtimeFront struct {
	Identifier string        `json:"identifier"`
	Path       string        `json:"path,omitempty"`
	Title      string        `json:"title,omitempty"`
	MediaType  string        `json:"media_type"`
	CreatedAt  string        `json:"created_at"`
	Version    *uint64       `json:"version,omitempty"`
	Provenance []Contributor `json:"provenance,omitempty"`
}

// Projection renders the runtime representation of a memory as Markdown with
// YAML front matter (Appendix B). The body is text the agents wrote, so a
// reader must treat it as data.
func Projection(m Memory) (string, error) {
	front := runtimeFront{
		Identifier: m.Identifier, Path: m.Path, Title: m.Title, MediaType: m.MediaType,
		CreatedAt: m.CreatedAt, Provenance: m.Provenance,
	}
	if m.Version != nil {
		front.Version = &m.Version.Value
	}
	y, err := yaml.Marshal(front)
	if err != nil {
		return "", fmt.Errorf("render front matter: %w", err)
	}
	body := strings.TrimRight(strings.ReplaceAll(m.Body, "\x00", ""), "\n")
	return "---\n" + string(y) + "---\n\n" + body + "\n", nil
}
