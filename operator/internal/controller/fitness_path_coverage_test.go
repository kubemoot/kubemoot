package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

const (
	pcNS      = "crew-pc"
	pcCrew    = "pc"
	pcKey     = "crew-pc/s/r1/s0-i1.json"
	pcTooler  = "k8s-workloads"
	pcServer1 = "ollama-a"
	pcServer2 = "vllm-b"
	pcModel1  = "qwen3:8b"
	pcLead    = "lead"
	pcOllama  = "ollama"
	pcCoord   = "coordinator"
	pcTriage  = "triage"
	pcModels  = "models"
	pcThink   = "thinking"
	pcElse    = "elsewhere"
	pcPlain   = "plain"
	pcVLLM    = "vllm"
	pcSimple  = "simple"
)

// doneWithCalls is a transcript whose done event carries a receipt with the calls.
const doneWithCalls = `{"events":[{"type":"synthesis","content":"x"},{"type":"done","receipt":{"calls":[` +
	`{"agent":"coordinator","phase":"advisory","kind":"simple","model":"qwen3:8b","server":"ollama-a","policy":"algorithm","at":1},` +
	`{"agent":"k8s-workloads","phase":"triage","kind":"triage","model":"qwen3:8b","server":"ollama-a"},` +
	`{"agent":"k8s-workloads","phase":"evaluation","kind":"tool-turn","model":"gpt-x","server":"vllm-b"}]}}]}`

func pcContext() pathContext {
	return pathContext{
		thinking:  map[string]string{pcLead: thinkingOn, pcTooler: thinkingOff, pcPlain: thinkingDefault},
		providers: map[string]string{pcServer1: pcOllama, pcServer2: pcVLLM},
		coord:     pcLead,
		declared:  pathDeclared{providers: []string{pcOllama, pcVLLM}, thinking: []string{thinkingOff, thinkingOn}},
	}
}

func rowMap(rows []kubemootv1alpha1.PathCount) map[string]int32 {
	out := map[string]int32{}
	for _, r := range rows {
		out[r.Name] = r.Calls
	}
	return out
}

func TestTranscriptKeepsTheReceiptCalls(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{}
	ir, ok := iterationFromTranscript(suite, pcKey, []byte(doneWithCalls))
	if !ok {
		t.Fatal("transcript not read")
	}
	want := []fitnessscript.PlacedCall{
		{Agent: pcCoord, Phase: "advisory", Kind: pcSimple, Model: pcModel1, Server: pcServer1},
		{Agent: pcTooler, Phase: pcTriage, Kind: pcTriage, Model: pcModel1, Server: pcServer1},
		{Agent: pcTooler, Phase: "evaluation", Kind: "tool-turn", Model: "gpt-x", Server: pcServer2},
	}
	if !reflect.DeepEqual(ir.Calls, want) {
		t.Errorf("Calls = %+v, want %+v", ir.Calls, want)
	}
	older, _ := iterationFromTranscript(suite, pcKey, []byte(`{"events":[{"type":"done"}]}`))
	if len(older.Calls) != 0 {
		t.Errorf("a transcript from before the receipt was kept has Calls = %+v", older.Calls)
	}
}

func TestBuildPathCoverageCountsEachPathAndNamesTheGaps(t *testing.T) {
	ir, _ := iterationFromTranscript(&kubemootv1alpha1.CrewFitnessSuite{}, pcKey, []byte(doneWithCalls))
	cov := buildPathCoverage([]IterationResult{ir, ir}, pcContext())

	if cov.Calls != 6 || cov.Transcripts != 2 {
		t.Errorf("calls %d transcripts %d, want 6 and 2", cov.Calls, cov.Transcripts)
	}
	checks := []struct {
		what string
		got  []kubemootv1alpha1.PathCount
		want map[string]int32
	}{
		{"kinds", cov.Kinds, map[string]int32{pcSimple: 2, pcTriage: 2, "tool-turn": 2}},
		{pcModels, cov.Models, map[string]int32{pcModel1: 4, "gpt-x": 2}},
		{"providers", cov.Providers, map[string]int32{pcOllama: 4, pcVLLM: 2}},
		// the coordinator's role name resolves to its agent, which thinks; the tooler does not
		{pcThink, cov.Thinking, map[string]int32{thinkingOn: 2, thinkingOff: 4}},
	}
	for _, c := range checks {
		if got := rowMap(c.got); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v, want %v", c.what, got, c.want)
		}
	}
	gaps := strings.Join(cov.Gaps, "; ")
	for _, want := range []string{"no call in the selection phase", "no call in the synthesis phase"} {
		if !strings.Contains(gaps, want) {
			t.Errorf("gaps %q must say %q", gaps, want)
		}
	}
	for _, not := range []string{"no triage call", "no simple call", "provider", pcThink} {
		if strings.Contains(gaps, not) {
			t.Errorf("gaps %q must not say %q: that path ran", gaps, not)
		}
	}
	if cov.Kinds[0].Calls < cov.Kinds[len(cov.Kinds)-1].Calls {
		t.Errorf("rows must run most calls first: %+v", cov.Kinds)
	}
}

func TestBuildPathCoverageSaysWhenAProviderOrThinkingWasNotExercised(t *testing.T) {
	calls := []fitnessscript.PlacedCall{
		{Agent: pcTooler, Phase: pcTriage, Kind: pcTriage, Model: pcModel1, Server: pcServer1},
	}
	cov := buildPathCoverage([]IterationResult{{Calls: calls}}, pcContext())
	gaps := strings.Join(cov.Gaps, "; ")
	for _, want := range []string{"no call on provider vllm", "no call with thinking on", "no tool-turn call", "no simple call"} {
		if !strings.Contains(gaps, want) {
			t.Errorf("gaps %q must say %q", gaps, want)
		}
	}
	if strings.Contains(gaps, "thinking off") || strings.Contains(gaps, "provider ollama") {
		t.Errorf("gaps %q name a path that ran", gaps)
	}
}

func TestBuildPathCoverageWithNoCallsSaysSoInsteadOfListingEveryPathMissing(t *testing.T) {
	cov := buildPathCoverage([]IterationResult{{Scenario: "a"}}, pcContext())
	if cov.Calls != 0 || len(cov.Kinds) != 0 || len(cov.Gaps) != 1 || !strings.Contains(cov.Gaps[0], "no placed model call") {
		t.Errorf("coverage = %+v", cov)
	}
}

func TestBuildPathCoverageCountsUnknownServersAgentsAndBlankFields(t *testing.T) {
	calls := []fitnessscript.PlacedCall{{Agent: "stranger", Server: pcElse}}
	cov := buildPathCoverage([]IterationResult{{Calls: calls}}, pcContext())
	for what, rows := range map[string][]kubemootv1alpha1.PathCount{
		"kinds": cov.Kinds, "phases": cov.Phases, pcModels: cov.Models, "providers": cov.Providers, pcThink: cov.Thinking,
	} {
		if len(rows) != 1 || rows[0].Name != "unknown" || rows[0].Calls != 1 {
			t.Errorf("%s = %+v, want one unknown row", what, rows)
		}
	}
}

func TestThinkingSettingNamesTheThreeStates(t *testing.T) {
	for _, c := range []struct {
		in   *bool
		want string
	}{{nil, thinkingDefault}, {ptr.To(true), thinkingOn}, {ptr.To(false), thinkingOff}} {
		if got := thinkingSetting(c.in); got != c.want {
			t.Errorf("thinkingSetting = %q, want %q", got, c.want)
		}
	}
}

func pcReconciler(t *testing.T, objs ...runtime.Object) *CrewFitnessSuiteReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
	return &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme}
}

func pcAgent(name string, think *bool) *kubemootv1alpha1.Agent {
	return &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pcNS, Labels: map[string]string{labelCrew: pcCrew}},
		Spec:       kubemootv1alpha1.AgentSpec{Think: think},
	}
}

func TestLoadPathContextJoinsAgentsCrewAndRealm(t *testing.T) {
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: pcCrew, Namespace: pcNS},
		Status: kubemootv1alpha1.CrewStatus{CoordinatorRef: pcLead}}
	other := pcAgent(pcElse, ptr.To(true))
	other.Labels[labelCrew] = "other-crew"
	r := pcReconciler(t, crew, pcAgent(pcLead, ptr.To(true)), pcAgent(pcTooler, ptr.To(false)), pcAgent(pcPlain, nil), other)
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: pcNS},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: pcCrew},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{Realm: &kubemootv1alpha1.FitnessRealm{Start: &kubemootv1alpha1.RealmSnapshot{
			ModelServers: []kubemootv1alpha1.RealmModelServer{
				{Name: pcServer1, Engine: pcOllama}, {Name: pcServer2, Engine: pcVLLM}, {Name: "ollama-c", Engine: pcOllama},
			}}}},
	}
	pc := r.loadPathContext(context.Background(), suite)

	if pc.coord != pcLead || pc.thinking[pcLead] != thinkingOn || pc.thinking[pcTooler] != thinkingOff || pc.thinking[pcPlain] != thinkingDefault {
		t.Errorf("thinking %v coord %q", pc.thinking, pc.coord)
	}
	if _, ok := pc.thinking[pcElse]; ok {
		t.Error("an agent of another crew must not be joined")
	}
	if !reflect.DeepEqual(pc.declared.thinking, []string{thinkingOff, thinkingOn}) {
		t.Errorf("declared thinking = %v (default is not a setting an agent declares)", pc.declared.thinking)
	}
	if !reflect.DeepEqual(pc.declared.providers, []string{pcOllama, pcVLLM}) || pc.providers[pcServer2] != pcVLLM {
		t.Errorf("providers %v declared %v", pc.providers, pc.declared.providers)
	}
}

func TestRecordTranscriptStatusSetsInventedCountsAndPathCoverageFromOneRead(t *testing.T) {
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: pcCrew, Namespace: pcNS}}
	r := pcReconciler(t, crew, pcAgent(pcTooler, ptr.To(false)))
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: pcNS},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: pcCrew, Iterations: 1,
			Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "a"}}},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "r1"},
	}
	suite.Status.Scenarios = scenarioResults(suite.Spec.Scripts, nil)
	r.artifacts = fakeStore{objs: map[string][]byte{pcKey: []byte(doneWithCalls)}}

	r.recordTranscriptStatus(context.Background(), suite)
	cov := suite.Status.PathCoverage
	if cov == nil || cov.Calls != 3 || cov.Transcripts != 1 {
		t.Fatalf("pathCoverage = %+v", cov)
	}

	// A store that cannot list keeps the table it has instead of blanking it.
	r.artifacts = listFailStore{}
	r.recordTranscriptStatus(context.Background(), suite)
	if suite.Status.PathCoverage != cov {
		t.Error("a failed listing must leave status.pathCoverage as it was")
	}
}
