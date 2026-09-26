/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"
)

func TestMatchesQueries(t *testing.T) {
	r := &MCPCatalogReconciler{}
	srv := OfficialRegistryServer{Name: "PostgreSQL MCP", Description: "Query a Postgres database"}
	if !r.matchesQueries(srv, []string{"postgres"}) {
		t.Error("should match on name, case-insensitively")
	}
	if !r.matchesQueries(srv, []string{"DATABASE"}) {
		t.Error("should match on description, case-insensitively")
	}
	if r.matchesQueries(srv, []string{"redis"}) {
		t.Error("should not match an unrelated query")
	}
}

func TestParseAgentDiscoveryResponse(t *testing.T) {
	r := &MCPCatalogReconciler{}
	// Object form (prefix text before the JSON is tolerated; JSON runs to the end).
	got, err := r.parseAgentDiscoveryResponse(`Here you go: {"servers":[{"name":"srv-a"}]}`)
	if err != nil || len(got) != 1 || got[0].Name != "srv-a" {
		t.Fatalf("object form: got %+v err %v", got, err)
	}
	// No JSON at all -> error.
	if _, err := r.parseAgentDiscoveryResponse("no json here"); err == nil {
		t.Error("expected an error when the response has no JSON")
	}
}
