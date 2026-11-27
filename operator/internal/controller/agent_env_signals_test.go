/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	signalTestAgree    = "agree"
	signalTestQuestion = "question"
	signalTestTooler   = "k8s-nodes"
)

func signalTestArchetype(name string, signals []kubemootv1alpha1.ArchetypeSignal,
	recheck *kubemootv1alpha1.ArchetypeRecheck) *kubemootv1alpha1.MootArchetype {
	return &kubemootv1alpha1.MootArchetype{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kubemootv1alpha1.MootArchetypeSpec{Signals: signals, Recheck: recheck},
	}
}

func signalTestDeclared() []kubemootv1alpha1.ArchetypeSignal {
	return []kubemootv1alpha1.ArchetypeSignal{
		{Name: signalTestAgree, Meaning: "your finding stands"},
		{Name: signalTestQuestion, Meaning: "ask the agent that made a finding to recheck it",
			Terminal: ptr.To(false), Addressed: true},
	}
}

// decodeSignals reads the KUBEMOOT_DISCUSS_SIGNALS value back into its entries.
func decodeSignals(t *testing.T, value string) []map[string]any {
	t.Helper()
	var entries []map[string]any
	if err := json.Unmarshal([]byte(value), &entries); err != nil {
		t.Fatalf("the signals variable is not a JSON list: %v (%q)", err, value)
	}
	return entries
}

func TestSignalEnvVars_DeclaredKindsBecomeOneJSONList(t *testing.T) {
	got := signalEnvVars(signalTestDeclared())
	if len(got) != 1 || got[0].Name != envDiscussSignals {
		t.Fatalf("want one %s variable, got %v", envDiscussSignals, got)
	}
	entries := decodeSignals(t, got[0].Value)
	if len(entries) != 2 {
		t.Fatalf("want two kinds, got %v", entries)
	}
	wantSignal(t, entries[0], signalTestAgree, true, false)
	wantSignal(t, entries[1], signalTestQuestion, false, true)
}

// wantSignal checks one decoded entry: its name, its flags, and that it carries a meaning.
func wantSignal(t *testing.T, entry map[string]any, name string, terminal, addressed bool) {
	t.Helper()
	if entry[signalFieldName] != name || entry[signalFieldTerminal] != terminal ||
		entry[signalFieldAddressed] != addressed || entry[signalFieldMeaning] == "" {
		t.Errorf("%s: want terminal=%v addressed=%v with a meaning, got %v", name, terminal, addressed, entry)
	}
}

func TestSignalEnvVars_NoneOrOnlyBareNamesSetsNothing(t *testing.T) {
	if got := signalEnvVars(nil); len(got) != 0 {
		t.Fatalf("no signals: want no variable, got %v", got)
	}
	bare := []kubemootv1alpha1.ArchetypeSignal{{Name: "agree"}, {Name: "concern"}}
	if got := signalEnvVars(bare); len(got) != 0 {
		t.Fatalf("names without a meaning are never offered: want no variable, got %v", got)
	}
}

func TestSignalEnvVars_BareNamesAreLeftOutOfAMixedList(t *testing.T) {
	mixed := append(signalTestDeclared(), kubemootv1alpha1.ArchetypeSignal{Name: "advisory"})
	got := signalEnvVars(mixed)
	if len(got) != 1 || len(decodeSignals(t, got[0].Value)) != 2 {
		t.Fatalf("want the two kinds with a meaning only, got %v", got)
	}
}

func TestRecheckEnvVars(t *testing.T) {
	if got := recheckEnvVars(nil); len(got) != 0 {
		t.Fatalf("no block: want no variable, got %v", got)
	}
	if got := recheckEnvVars(&kubemootv1alpha1.ArchetypeRecheck{}); len(got) != 0 {
		t.Fatalf("no maxRounds: want no variable, got %v", got)
	}
	for rounds, want := range map[int32]string{0: "0", 1: "1", 3: "3"} {
		got := recheckEnvVars(&kubemootv1alpha1.ArchetypeRecheck{MaxRounds: ptr.To(rounds)})
		if v, ok := envValue(got, envRecheckMaxRounds); !ok || v != want {
			t.Fatalf("maxRounds %d: want %s=%s, got %v", rounds, envRecheckMaxRounds, want, got)
		}
	}
}

func TestAgentSignalEnvVars_EveryAgentOfTheCrewGetsTheArchetypesSignals(t *testing.T) {
	r := reviewTestReconciler(t,
		signalTestArchetype(defaultArchetypeName, signalTestDeclared(), nil),
		signalTestArchetype(reviewTestPanel, nil, nil),
		reviewTestPolicyFor(reviewTestPanel),
	)
	tooler := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: signalTestTooler, Namespace: testCrewNamespace,
			Labels: map[string]string{labelCrew: reviewTestOtherCrew}},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: testRoleTooler},
	}
	if got := r.agentSignalEnvVars(context.Background(), tooler); len(got) != 1 {
		t.Fatalf("a tooler on the default archetype: want its signals, got %v", got)
	}
	if got := r.agentSignalEnvVars(context.Background(), reviewTestCoordinatorOf(testCrewName)); len(got) != 0 {
		t.Fatalf("a crew whose archetype declares no signals: want none, got %v", got)
	}
}

func TestAgentSignalEnvVars_MissingArchetypeSetsNothing(t *testing.T) {
	r := reviewTestReconciler(t)
	if got := r.agentSignalEnvVars(context.Background(), reviewTestCoordinatorOf(testCrewName)); len(got) != 0 {
		t.Fatalf("no archetype installed: want no variables, got %v", got)
	}
	if got := r.coordinatorRecheckEnvVars(context.Background(), reviewTestCoordinatorOf(testCrewName)); len(got) != 0 {
		t.Fatalf("no archetype installed: want no recheck variable, got %v", got)
	}
}

func TestCoordinatorRecheckEnvVars_FollowsTheCrewsArchetype(t *testing.T) {
	r := reviewTestReconciler(t,
		signalTestArchetype(defaultArchetypeName, nil, &kubemootv1alpha1.ArchetypeRecheck{MaxRounds: ptr.To(int32(2))}),
	)
	got := r.coordinatorRecheckEnvVars(context.Background(), reviewTestCoordinatorOf(testCrewName))
	if v, ok := envValue(got, envRecheckMaxRounds); !ok || v != "2" {
		t.Fatalf("want %s=2, got %v", envRecheckMaxRounds, got)
	}
}

func TestArchetypeSignal_DecodesTheEarlierBareNameForm(t *testing.T) {
	var spec kubemootv1alpha1.MootArchetypeSpec
	raw := `{"phases":[{"name":"evaluating"}],"signals":["agree",{"name":"question","meaning":"ask","terminal":false,"addressed":true}]}`
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("a stored archetype with bare names must decode: %v", err)
	}
	if len(spec.Signals) != 2 || spec.Signals[0].Name != "agree" || spec.Signals[0].Meaning != "" ||
		!spec.Signals[0].IsTerminal() {
		t.Fatalf("bare name: want {Name: agree}, terminal by default, got %+v", spec.Signals[0])
	}
	if q := spec.Signals[1]; q.Name != "question" || q.IsTerminal() || !q.Addressed {
		t.Fatalf("object form: want an addressed, non-terminal question, got %+v", q)
	}
	var bad kubemootv1alpha1.ArchetypeSignal
	if err := json.Unmarshal([]byte(`42`), &bad); err == nil {
		t.Fatal("a number is neither form: want an error")
	}
}
