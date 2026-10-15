/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package bus holds the Go types of the Kubemoot bus contract: the messages agents
// and Kubemoot exchange over NATS, the key-value entries they share, and the events
// of the discussion gateway's stream. The contract is the AsyncAPI document and the
// JSON Schema files in schema/; the types in zz_generated.bus.go are generated from
// them, so change the schema and run go generate, never the generated file.
//
// The package holds only data types and no behavior, so it has no abstractions to
// offer its importers; it is the shared vocabulary of the contract, kept stable by
// the contract's versioning rather than by interfaces.
package bus

//go:generate go run ./internal/gen
