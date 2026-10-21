/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mesh_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

const general = "general"

func valid() mesh.Contract {
	return mesh.Contract{
		ContractVersion: mesh.ContractVersion,
		Identity: mesh.Identity{Namespace: "homelab", Crew: "homelab-pilot", Agent: "notes-reader",
			Role: "tooler", Channels: []string{general}, Harness: "goose"},
		Bus:  mesh.Bus{NATSURL: "nats://nats:4222", Stream: bus.DiscussStream, Consumer: "sidecar-homelab-homelab-pilot-notes-reader"},
		Turn: mesh.Turn{URL: "http://127.0.0.1:9000", Concurrency: 1, DeadlineSeconds: 300},
		Models: mesh.Models{Aliases: map[string]mesh.Alias{
			"default": {Candidates: []string{"qwen3:14b"}, Fallback: &mesh.Endpoint{Server: "gpu1", Endpoint: "http://ollama:11434", Model: "qwen3:14b"}},
			"triage":  {Candidates: []string{"qwen3:4b"}},
		}},
		Placement: mesh.Placement{Policy: "algorithm", ServiceURL: "http://placement.kubemoot:8080"},
		Tools:     mesh.Tools{Gateway: "http://gw.homelab:8080", Enabled: []string{"fetch"}, NativeTools: mesh.NativeToolsNone},
	}
}

func TestAContractRoundTripsThroughYAML(t *testing.T) {
	data, err := valid().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := mesh.Parse(data)
	if err != nil {
		t.Fatalf("parse of the contract we just wrote: %v\n%s", err, data)
	}
	if got.Identity.Agent != "notes-reader" || got.Models.Aliases["default"].Fallback.Server != "gpu1" {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

func TestParseRefusesWhatTheSidecarCannotServe(t *testing.T) {
	cases := map[string]func(*mesh.Contract){
		"another contract version":    func(c *mesh.Contract) { c.ContractVersion = "3" },
		"a namespace with a dot":      func(c *mesh.Contract) { c.Identity.Namespace = "a.b" },
		"a missing crew":              func(c *mesh.Contract) { c.Identity.Crew = "" },
		"a wildcard agent":            func(c *mesh.Contract) { c.Identity.Agent = "*" },
		"a repeated channel":          func(c *mesh.Contract) { c.Identity.Channels = []string{"a", "a"} },
		"the broadcast channel":       func(c *mesh.Contract) { c.Identity.Channels = []string{"broadcast"} },
		"an empty candidate":          func(c *mesh.Contract) { c.Models.Aliases["triage"] = mesh.Alias{Candidates: []string{""}} },
		"a dotted alias name":         func(c *mesh.Contract) { c.Models.Aliases["a.b"] = mesh.Alias{Candidates: []string{"m"}} },
		"a channel with a space":      func(c *mesh.Contract) { c.Identity.Channels = []string{"a b"} },
		"no NATS URL":                 func(c *mesh.Contract) { c.Bus.NATSURL = "" },
		"a consumer without a stream": func(c *mesh.Contract) { c.Bus.Stream = "" },
		"a negative progress ack":     func(c *mesh.Contract) { c.Bus.ProgressAckSeconds = -1 },
		"a turn URL that is not http": func(c *mesh.Contract) { c.Turn.URL = "ftp://x" },
		"no concurrency":              func(c *mesh.Contract) { c.Turn.Concurrency = 0 },
		"no deadline":                 func(c *mesh.Contract) { c.Turn.DeadlineSeconds = 0 },
		"no default alias":            func(c *mesh.Contract) { delete(c.Models.Aliases, "default") },
		"an alias with no model":      func(c *mesh.Contract) { c.Models.Aliases["triage"] = mesh.Alias{} },
		"a half fallback": func(c *mesh.Contract) {
			c.Models.Aliases["default"] = mesh.Alias{Candidates: []string{"m"}, Fallback: &mesh.Endpoint{Server: "s"}}
		},
		"a bad placement URL":    func(c *mesh.Contract) { c.Placement.ServiceURL = "placement" },
		"a bad gateway URL":      func(c *mesh.Contract) { c.Tools.Gateway = "gw" },
		"an unknown nativeTools": func(c *mesh.Contract) { c.Tools.NativeTools = "some" },
		"a bad OTLP endpoint":    func(c *mesh.Contract) { c.Observability = &mesh.Observability{OTLPEndpoint: "collector"} },
	}
	for name, mutate := range cases {
		c := valid()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if _, err := c.Marshal(); err == nil {
			t.Errorf("%s was written", name)
		}
	}
}

func TestValidateReportsEveryProblemTogether(t *testing.T) {
	c := valid()
	c.Identity.Crew = ""
	c.Turn.Concurrency = 0
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "identity.crew") || !strings.Contains(err.Error(), "turn.concurrency") {
		t.Errorf("want both problems named, got %v", err)
	}
}

func TestParseRefusesUnknownFieldsAndGarbage(t *testing.T) {
	for name, in := range map[string]string{
		"an unknown field": "contractVersion: \"2\"\nbogus: 1\n",
		"not yaml":         "{{{",
		"empty":            "",
	} {
		if _, err := mesh.Parse([]byte(in)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestDefaults(t *testing.T) {
	if got := (mesh.Bus{}).ProgressAck(); got != mesh.DefaultProgressAck {
		t.Errorf("default progress ack = %v", got)
	}
	if got := (mesh.Bus{ProgressAckSeconds: 7}).ProgressAck(); got != 7*time.Second {
		t.Errorf("progress ack = %v", got)
	}
	if got := (mesh.Turn{DeadlineSeconds: 5}).Deadline(); got != 5*time.Second {
		t.Errorf("deadline = %v", got)
	}
	// The default progress ack must leave room for several beats inside the operator's AckWait.
	if mesh.DefaultProgressAck*3 >= mesh.ConsumerAckWait {
		t.Errorf("progress ack %v is too slow for a 120 s AckWait", mesh.DefaultProgressAck)
	}
}

func TestNamesFollowTheBusContract(t *testing.T) {
	id := valid().Identity
	for got, want := range map[string]string{
		id.DiscussSubject(general, "t1"):    "kubemoot.discuss.homelab.homelab-pilot.general.t1",
		id.DiscussWildcard():                "kubemoot.discuss.homelab.homelab-pilot.>",
		id.LifecycleSubject("ready"):        "kubemoot.lifecycle.homelab.homelab-pilot.ready.notes-reader",
		id.ArtifactSubject("t1"):            "kubemoot.artifacts.homelab.homelab-pilot.t1",
		id.ArtifactKey("t1", "agree", "u1"): "homelab/homelab-pilot/t1/notes-reader/agree-u1",
		id.HeartbeatKey():                   "homelab.notes-reader",
		id.ConsumerName():                   "sidecar-homelab-homelab-pilot-notes-reader",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if f := id.DiscussFilters(); !slices.Equal(f, []string{
		"kubemoot.discuss.homelab.homelab-pilot.broadcast.>", "kubemoot.discuss.homelab.homelab-pilot.general.>"}) {
		t.Errorf("filters = %v", f)
	}
	if !id.Listens("broadcast") || !id.Listens("general") || id.Listens("kubernetes") {
		t.Error("Listens must accept broadcast and declared channels only")
	}
}

func TestPermissionsScopeASidecarToItsOwnAgent(t *testing.T) {
	id := valid().Identity
	p := id.Permissions()
	for _, must := range []string{
		"kubemoot.discuss.homelab.homelab-pilot.>",
		"kubemoot.lifecycle.homelab.homelab-pilot.*.notes-reader",
		"$KV.kubemoot_agent_state.homelab.notes-reader",
	} {
		if !slices.Contains(p.Publish, must) {
			t.Errorf("publish lacks %q", must)
		}
	}
	// Nothing that reaches another agent's heartbeat, another crew's threads, or every lifecycle signal.
	for _, subject := range append(slices.Clone(p.Publish), p.Subscribe...) {
		if subject == ">" || strings.HasPrefix(subject, "kubemoot.>") || strings.Contains(subject, "lifecycle.>") ||
			strings.HasSuffix(subject, "kubemoot_agent_state.>") {
			t.Errorf("permission %q is wider than one agent", subject)
		}
	}
	other := id
	other.Agent = "other"
	if slices.Equal(p.Publish, other.Permissions().Publish) {
		t.Error("two agents got the same publish permissions")
	}
}

func TestTheContractVersionIsTheBusMajor(t *testing.T) {
	if major, _, _ := strings.Cut(bus.ContractVersion, "."); major != mesh.ContractVersion {
		t.Errorf("contract.yaml version %q, bus contract %q", mesh.ContractVersion, bus.ContractVersion)
	}
}

func TestSubjectsMatchTheOperatorsCrewScope(t *testing.T) {
	scope, err := crewscope.New("homelab", "homelab-pilot")
	if err != nil {
		t.Fatal(err)
	}
	id := valid().Identity
	if got, want := id.DiscussSubject(general, "t1"), scope.DiscussSubject("general", "t1"); got != want {
		t.Errorf("mesh %q, crewscope %q", got, want)
	}
	for _, bad := range []string{"", "a.b", "a*", "a>", "a b"} {
		_, err := crewscope.New(bad, "crew")
		if (mesh.ValidToken("x", bad) == nil) != (err == nil) {
			t.Errorf("token %q is judged differently", bad)
		}
	}
}

func TestEachChannelFiltersOnce(t *testing.T) {
	id := valid().Identity
	id.Channels = []string{general, general, "broadcast"}
	if f := id.DiscussFilters(); len(f) != 2 {
		t.Errorf("want broadcast and general once each, got %v", f)
	}
}

func TestTheAckPermissionIsThisAgentsConsumerOnly(t *testing.T) {
	p := valid().Identity.Permissions()
	if !slices.Contains(p.Publish, "$JS.ACK.KUBEMOOT_DISCUSS.sidecar-homelab-homelab-pilot-notes-reader.>") {
		t.Errorf("publish lacks the consumer-scoped ack: %v", p.Publish)
	}
	for _, s := range p.Publish {
		if s == "$JS.ACK.>" || s == "$JS.API.>" {
			t.Errorf("permission %q reaches other consumers", s)
		}
	}
	other := valid().Identity
	other.Crew = "other"
	for _, a := range p.Publish {
		for _, b := range other.Permissions().Publish {
			if a == b && strings.Contains(a, "homelab-pilot") {
				t.Errorf("a different crew shares %q", a)
			}
		}
	}
}
