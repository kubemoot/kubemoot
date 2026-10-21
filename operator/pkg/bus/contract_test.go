/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package bus_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/bus/internal/schemaset"
)

// The contract tests. testdata/<schema>/ holds messages recorded from a running
// homelab (NATS discussion messages and KV values read from the bus, the gateway's
// conversation list) and stream events the gateway's translation made from the
// recorded messages. Long text is shortened and node fingerprints are removed; the
// fields and their types are as recorded.

const (
	schemaDir = "schema"
	lockFile  = "contract.lock.json"
	// dashboardTypes is the dashboard's generated copy of the contract.
	dashboardTypes = "../../../dashboard/src/lib/types/bus.ts"
	baseURL        = "https://kubemoot.test/bus/"
)

var update = flag.Bool("update", false, "rewrite "+lockFile+" from the schema")

func loadSet(t *testing.T) *schemaset.Set {
	t.Helper()
	set, err := schemaset.Load(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// strictSchemas compiles every schema so an object rejects any field its schema does
// not declare: a writer that sends an undeclared field fails the contract test, while
// readers stay free to ignore unknown fields. The published files are draft-07, which
// has no keyword for this; the strict copies drop $schema and are read as 2020-12,
// whose unevaluatedProperties sees through allOf. The only $ref with siblings is
// discussion-message.json's metadata, whose sibling goJSONSchema no validator reads.
func strictSchemas(t *testing.T, set *schemaset.Set) map[string]*jsonschema.Schema {
	t.Helper()
	parts := compositionParts(set)
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	for _, name := range set.Names() {
		doc := strict(set.Schema(name), !parts[name])
		delete(doc, "$schema")
		if err := c.AddResource(baseURL+name, reparse(t, doc)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	out := map[string]*jsonschema.Schema{}
	for _, name := range set.Names() {
		sch, err := c.Compile(baseURL + name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = sch
	}
	return out
}

// compositionParts names the files an allOf composition is made of. Their own top
// level must accept the other parts' fields, so the composition checks them instead.
func compositionParts(set *schemaset.Set) map[string]bool {
	parts := map[string]bool{}
	for _, name := range set.Names() {
		doc := set.Schema(name)
		if !schemaset.IsComposition(doc) {
			continue
		}
		for _, sub := range doc["allOf"].([]any) {
			if ref, ok := sub.(map[string]any)["$ref"].(string); ok {
				parts[ref] = true
			}
		}
	}
	return parts
}

// strict copies schema v, closing every object schema that declares properties.
// closeTop is false for the top level of a composition part.
func strict(v map[string]any, closeTop bool) map[string]any {
	out := schemaset.Rewrite(v, func(c schemaset.Child) map[string]any { return strict(c.Schema, true) })
	_, hasProps := v["properties"]
	_, hasAdditional := v["additionalProperties"]
	if closeTop && !hasAdditional && (hasProps || schemaset.IsComposition(v)) {
		out["unevaluatedProperties"] = false
	}
	return out
}

// reparse turns a decoded document into the validator's own representation.
func reparse(t *testing.T, doc any) any {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// recorded lists the recorded messages by schema file.
func recorded(t *testing.T) map[string][]string {
	t.Helper()
	dirs, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, d := range dirs {
		files, err := filepath.Glob(filepath.Join("testdata", d.Name(), "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("testdata/%s holds no messages (%v)", d.Name(), err)
		}
		out[d.Name()+".json"] = files
	}
	return out
}

func validate(t *testing.T, sch *jsonschema.Schema, data []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return sch.Validate(inst)
}

// unrecorded are the payload schemas with no recorded message yet: core NATS
// subjects no stream keeps, buckets that were empty when the messages were
// recorded, and the parts of the stream event. A new schema needs a recording or an
// entry here. agent-runtime's BusContractTest covers several of these.
var unrecorded = map[string]bool{
	"agent-activity.json": true, "artifact-ref.json": true, "artifact-reference.json": true,
	"chat-event.json": true, "crew-memory-fact.json": true, "discussion-metadata.json": true,
	"discussion-request.json": true, "error-response.json": true, "onboarding-deployed.json": true,
	"onboarding-docs-indexed.json": true, "onboarding-progress.json": true, "pinned-thread.json": true,
	"placement-call.json":          true,
	"post-discussion-request.json": true, "post-discussion-response.json": true,
	"model-lifecycle-reply.json": true, "model-lifecycle-request.json": true,
	"server-ticket.json": true, "scheduled-record.json": true, "signal-meta.json": true,
	"stream-event-base.json": true, "subcommittee.json": true,
}

func TestEverySchemaHasRecordingsOrIsListed(t *testing.T) {
	have := recorded(t)
	for _, name := range loadSet(t).Names() {
		_, ok := have[name]
		if ok == unrecorded[name] {
			t.Errorf("%s: recorded %t, listed as unrecorded %t", name, ok, unrecorded[name])
		}
	}
}

func TestRecordedMessagesMatchTheSchema(t *testing.T) {
	set := loadSet(t)
	schemas := strictSchemas(t, set)
	for name, files := range recorded(t) {
		sch, ok := schemas[name]
		if !ok {
			t.Errorf("testdata for %s, which is not a schema file", name)
			continue
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := validate(t, sch, data); err != nil {
				t.Errorf("%s does not match %s: %v", f, name, err)
			}
		}
	}
}

func TestTheSchemaRejectsWhatWritersMustNotSend(t *testing.T) {
	sch := strictSchemas(t, loadSet(t))["discussion-message.json"]
	valid := `{"messageId":"m","threadId":"t","agentName":"a","messageType":"agree","metadata":{"signal":"agree"}}`
	if err := validate(t, sch, []byte(valid)); err != nil {
		t.Fatalf("a valid message was refused: %v", err)
	}
	for name, msg := range map[string]string{
		"an undeclared field":          strings.Replace(valid, `"a",`, `"a","bogus":1,`, 1),
		"an undeclared metadata field": strings.Replace(valid, `{"signal"`, `{"bogus":1,"signal"`, 1),
		"no messageId":                 strings.Replace(valid, `"messageId":"m",`, ``, 1),
		"an unknown messageType":       strings.Replace(valid, `"messageType":"agree"`, `"messageType":"shrug"`, 1),
		"a number where text belongs":  strings.Replace(valid, `"agentName":"a"`, `"agentName":7`, 1),
	} {
		if err := validate(t, sch, []byte(msg)); err == nil {
			t.Errorf("%s was accepted: %s", name, msg)
		}
	}
}

// streamEvent is a stream event as the gateway composes it from the generated parts.
type streamEvent struct {
	bus.StreamEventBase
	*bus.SignalMeta
	*bus.Subcommittee
}

// goTypes are the generated types of the recorded messages.
var goTypes = map[string]func() any{
	"discussion-message.json":    func() any { return &bus.DiscussionMessage{} },
	"artifact-notice.json":       func() any { return &bus.ArtifactNotice{} },
	"agent-heartbeat.json":       func() any { return &bus.AgentHeartbeat{} },
	"server-state.json":          func() any { return &bus.ServerState{} },
	"model-demand.json":          func() any { return &bus.ModelDemand{} },
	"latency-samples.json":       func() any { return &bus.LatencySamples{} },
	"scheduler-performance.json": func() any { return &bus.SchedulerPerformance{} },
	"conversation-history.json":  func() any { return &bus.ConversationHistory{} },
	"crew-resumes.json":          func() any { return &bus.CrewResumes{} },
	"conversation-list.json":     func() any { return &bus.ConversationList{} },
	"stream-event.json":          func() any { return &streamEvent{} },
}

func TestRecordedMessagesDecodeIntoTheGeneratedTypes(t *testing.T) {
	for name, files := range recorded(t) {
		newValue, ok := goTypes[name]
		if !ok {
			t.Errorf("no generated type listed for %s", name)
			continue
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip(t, f, data, newValue())
		}
	}
}

// roundTrip decodes data into v, refusing fields v lacks, and checks that encoding v
// gives back every value data had that is not a zero value.
func roundTrip(t *testing.T, file string, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Errorf("%s: %v", file, err)
		return
	}
	again, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again, &got); err != nil {
		t.Fatal(err)
	}
	if want, got := withoutZeros(want), withoutZeros(got); !reflect.DeepEqual(want, got) {
		t.Errorf("%s: the generated type changed the message:\nwant %v\ngot  %v", file, want, got)
	}
}

// withoutZeros drops zero values (0, "", false, null, and empty lists and objects) at
// every level: generated types leave out an optional field that holds one.
func withoutZeros(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, sub := range t {
			if s := withoutZeros(sub); s != nil {
				out[k] = s
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(t) == 0 {
			return nil
		}
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = withoutZeros(item)
		}
		return out
	case string, float64, bool:
		if reflect.ValueOf(t).IsZero() {
			return nil
		}
		return t
	default:
		return t
	}
}

func TestGeneratedTypesAreCurrent(t *testing.T) {
	set := loadSet(t)
	want := "Schema digest: " + set.Digest
	code, err := os.ReadFile("zz_generated.bus.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(code, []byte(want)) {
		t.Errorf("zz_generated.bus.go is older than the schema; run go generate ./pkg/bus")
	}
	if bus.ContractVersion != set.Version {
		t.Errorf("ContractVersion %q, schema %q; run go generate ./pkg/bus", bus.ContractVersion, set.Version)
	}
	ts, err := os.ReadFile(dashboardTypes)
	if os.IsNotExist(err) {
		t.Skip("no dashboard in this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(ts, []byte(want)) {
		t.Errorf("%s is older than the schema; run npm run gen:bus in dashboard/", dashboardTypes)
	}
}

func TestEverySchemaFileIsPartOfTheContract(t *testing.T) {
	set := loadSet(t)
	data, err := os.ReadFile(filepath.Join(schemaDir, schemaset.AsyncAPIFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	referenced := map[string]bool{}
	for _, ref := range yamlRefs(doc) {
		referenced[strings.TrimPrefix(ref, "./")] = true
	}
	for _, name := range set.Names() {
		for _, ref := range schemaset.Refs(set.Schema(name)) {
			referenced[ref] = true
		}
	}
	for _, name := range set.Names() {
		if !referenced[name] {
			t.Errorf("%s is referenced neither by %s nor by another schema", name, schemaset.AsyncAPIFile)
		}
	}
}

// yamlRefs lists every $ref value in a decoded YAML document.
func yamlRefs(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			if ref, ok := sub.(string); ok && k == "$ref" {
				out = append(out, ref)
			}
			out = append(out, yamlRefs(sub)...)
		}
	case []any:
		for _, sub := range t {
			out = append(out, yamlRefs(sub)...)
		}
	}
	return out
}

// TestContractLockIsCurrent fails when the schema's surface or version differs from
// contract.lock.json. go test ./pkg/bus -run TestContractLockIsCurrent -update
// rewrites the lock, and refuses a breaking change without a new major version.
func TestContractLockIsCurrent(t *testing.T) {
	current := schemaset.LockOf(loadSet(t))
	data, err := os.ReadFile(lockFile)
	if os.IsNotExist(err) && *update {
		writeLock(t, current)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var locked schemaset.Lock
	if err := json.Unmarshal(data, &locked); err != nil {
		t.Fatal(err)
	}
	if err := schemaset.CheckUpdate(locked, current); err != nil {
		t.Fatal(err)
	}
	if schemaset.Equal(locked, current) {
		return
	}
	if !*update {
		breaking, additive := schemaset.Changes(locked.Surface, current.Surface)
		t.Fatalf("the schema changed since %s (breaking %v, additive %v); run "+
			"go test ./pkg/bus -run TestContractLockIsCurrent -update", lockFile, breaking, additive)
	}
	writeLock(t, current)
}

// writeLock writes lock to the lock file.
func writeLock(t *testing.T, lock schemaset.Lock) {
	t.Helper()
	out, err := schemaset.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockFile, out, 0o644); err != nil {
		t.Fatal(err)
	}
}
