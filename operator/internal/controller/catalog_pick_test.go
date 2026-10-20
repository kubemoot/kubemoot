package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// pilotPolicy is the pilot crew's policy after the migration: tool-calling is the
// one hard need, size follows each agent's qualityBias, and triage prefers small.
func pilotPolicy() *aiv1alpha1.CrewSchedulingPolicy {
	toolCalling := &metav1.LabelSelector{MatchLabels: map[string]string{selToolLabel: valueTrue}}
	return &aiv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "pilot-scheduling", Namespace: selNS},
		Spec: aiv1alpha1.CrewSchedulingPolicySpec{
			CrewRef:     selCrew,
			QualityBias: map[string]string{candReasoning: candBias, "default": "0.4"},
			Rules: []aiv1alpha1.SchedulingRule{
				{Phase: phaseMulling, Require: toolCalling},
				{Phase: phaseTriage, Require: toolCalling, Prefer: []aiv1alpha1.PreferenceTerm{
					{Weight: 100, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{aiv1alpha1.LabelSize: aiv1alpha1.SizeSmall}}},
				}},
			},
		},
	}
}

func pilotAgent(name string, capabilities ...string) *aiv1alpha1.Agent {
	return &aiv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: selNS, Labels: map[string]string{labelCrew: selCrew}},
		Spec:       aiv1alpha1.AgentSpec{Capabilities: capabilities},
	}
}

func catalogReconciler(t *testing.T, objs ...client.Object) (*AgentReconciler, client.Client) {
	t.Helper()
	scheme := gatewayScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&aiv1alpha1.Agent{}).Build()
	return &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}, cli
}

// A crew with no Models binds from the cluster catalog.
func TestPickModelFromTheCatalogWhenTheCrewShipsNoModels(t *testing.T) {
	r, _ := catalogReconciler(t,
		opServer(selOllama, aiv1alpha1.ServerTypeOllama, catEntryOf(selModel8, aiv1alpha1.SizeSmall, testToolCalling)),
		pilotPolicy(), pilotAgent(candAgent))
	pick, err := r.pickModel(context.Background(), pilotAgent(candAgent), phaseMulling)
	if err != nil {
		t.Fatal(err)
	}
	if pick.ModelID != selModel8 || pick.Server.Name != selOllama || pick.Server.Namespace != selOpNS {
		t.Errorf("pick = %s on %s/%s", pick.ModelID, pick.Server.Namespace, pick.Server.Name)
	}
	if pick.Endpoint != "http://ollama-gpu:11434" {
		t.Errorf("endpoint = %q", pick.Endpoint)
	}
}

// A crew that ships its own Models uses only those, even though the catalog has a
// model the crew would otherwise prefer; deleting them flips the crew to the catalog.
func TestPickModelCrewLocalModelsReplaceTheCatalog(t *testing.T) {
	own := candModel("own", candModel14, true, map[string]string{selToolLabel: valueTrue})
	own.Namespace = selNS
	ownServer := &aiv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: candServer, Namespace: selNS},
		Spec:       aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeOllama, Endpoint: testOllamaURL},
		Status:     aiv1alpha1.ModelServerStatus{Ready: true},
	}
	r, cli := catalogReconciler(t, &own, ownServer,
		opServer(selOllama, aiv1alpha1.ServerTypeOllama, catEntryOf("llama3.3:70b", aiv1alpha1.SizeLarge, testToolCalling)),
		pilotPolicy())
	pick, err := r.pickModel(context.Background(), pilotAgent(candAgent, candReasoning), phaseMulling)
	if err != nil || pick.ModelID != candModel14 {
		t.Fatalf("pick = %+v err %v, want the crew's own model", pick, err)
	}
	if err := cli.Delete(context.Background(), &own); err != nil {
		t.Fatal(err)
	}
	pick, err = r.pickModel(context.Background(), pilotAgent(candAgent, candReasoning), phaseMulling)
	if err != nil || pick.ModelID != "llama3.3:70b" {
		t.Fatalf("pick = %+v err %v, want the catalog's large model after the crew drops its Models", pick, err)
	}
}

// Adding a larger model to the cluster moves the reasoning agents to it while triage
// stays on the small one; removing it moves them back. The crew's objects never change.
func TestPickModelFollowsTheCatalogMulling(t *testing.T) {
	small := catEntryOf(selModel8, aiv1alpha1.SizeSmall, testToolCalling)
	large := catEntryOf(selModel30, aiv1alpha1.SizeLarge, testToolCalling, candReasoning)
	server := opServer(selOllama, aiv1alpha1.ServerTypeOllama, small)
	r, cli := catalogReconciler(t, server, pilotPolicy())
	agent := pilotAgent(candAgent, candReasoning)
	ctx := context.Background()

	pickOf := func(phase string) string {
		t.Helper()
		p, err := r.pickModel(ctx, agent, phase)
		if err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		return p.ModelID
	}
	if pickOf(phaseMulling) != selModel8 || pickOf(phaseTriage) != selModel8 {
		t.Fatal("with one small model both phases use it")
	}

	server.Status.Models = []aiv1alpha1.CatalogModel{small, large}
	if err := cli.Update(ctx, server); err != nil {
		t.Fatal(err)
	}
	if got := pickOf(phaseMulling); got != selModel30 {
		t.Errorf("mulling = %s, want the large model once the cluster has one", got)
	}
	if got := pickOf(phaseTriage); got != selModel8 {
		t.Errorf("triage = %s, want to stay on the small model", got)
	}

	server.Status.Models = []aiv1alpha1.CatalogModel{small}
	if err := cli.Update(ctx, server); err != nil {
		t.Fatal(err)
	}
	if got := pickOf(phaseMulling); got != selModel8 {
		t.Errorf("mulling = %s, want the small model again once the large one is gone", got)
	}
}

// An agent that declares reasoning keeps a reasoning model over an equal-sized one
// that cannot reason; an agent that declares nothing is not pushed.
func TestPickModelAbilityPreferenceIsSoft(t *testing.T) {
	plain := catEntryOf("a-plain", aiv1alpha1.SizeMedium, testToolCalling)
	thinker := catEntryOf("z-thinker", aiv1alpha1.SizeMedium, testToolCalling, candReasoning)
	r, _ := catalogReconciler(t, opServer(selOllama, aiv1alpha1.ServerTypeOllama, plain, thinker), pilotPolicy())
	pick, err := r.pickModel(context.Background(), pilotAgent(candAgent, candReasoning), phaseMulling)
	if err != nil || pick.ModelID != "z-thinker" {
		t.Fatalf("pick = %+v err %v, want the reasoning model", pick, err)
	}
	pick, err = r.pickModel(context.Background(), pilotAgent("plain"), phaseMulling)
	if err != nil || pick.ModelID != "a-plain" {
		t.Fatalf("pick = %+v err %v, want the first by name when nothing is declared", pick, err)
	}
	// With only the non-reasoning model the agent still schedules: abilities are not hard needs.
	r2, _ := catalogReconciler(t, opServer(selOllama, aiv1alpha1.ServerTypeOllama, plain), pilotPolicy())
	if pick, err := r2.pickModel(context.Background(), pilotAgent(candAgent, candReasoning), phaseMulling); err != nil || pick.ModelID != "a-plain" {
		t.Fatalf("pick = %+v err %v, want the only model", pick, err)
	}
}

// requireExpressions narrow the catalog; a model without the label fails a Gt.
func TestPickModelRequireExpressionsFilterByContext(t *testing.T) {
	short := catEntryOf("short", aiv1alpha1.SizeLarge, testToolCalling)
	short.Labels[selCtxKey] = "8192"
	long := catEntryOf("long", aiv1alpha1.SizeSmall, testToolCalling)
	long.Labels[selCtxKey] = "32768"
	unknown := catEntryOf("unknown-context", aiv1alpha1.SizeLarge, testToolCalling)
	policy := pilotPolicy()
	policy.Spec.Rules[0].RequireExpressions = []corev1.NodeSelectorRequirement{{Key: selCtxKey, Operator: corev1.NodeSelectorOpGt, Values: []string{"16383"}}}
	r, _ := catalogReconciler(t, opServer(selOllama, aiv1alpha1.ServerTypeOllama, short, long, unknown), policy)
	pick, err := r.pickModel(context.Background(), pilotAgent(candAgent), phaseMulling)
	if err != nil || pick.ModelID != "long" || len(pick.Candidates) != 1 {
		t.Fatalf("pick = %+v err %v, want only the model with enough context", pick, err)
	}
}

// A hard size need the cluster cannot meet is unschedulable, and the message names
// the missing label and the models whose size is unknown.
func TestPickModelZeroMatchMessageNamesTheMissingSize(t *testing.T) {
	policy := pilotPolicy()
	policy.Spec.Rules[0].Require = &metav1.LabelSelector{MatchLabels: map[string]string{aiv1alpha1.LabelSize: aiv1alpha1.SizeLarge}}
	r, _ := catalogReconciler(t,
		opServer(selOllama, aiv1alpha1.ServerTypeOllama, catEntryOf(selModel8, aiv1alpha1.SizeSmall), catEntryOf("mystery", "")), policy)
	_, err := r.pickModel(context.Background(), pilotAgent(candAgent), phaseMulling)
	if err == nil {
		t.Fatal("want unschedulable")
	}
	for _, want := range []string{"kubemoot.ai/size=large", "2 available", "1 model(s) have no known size"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err.Error(), want)
		}
	}
}

// A model calibration found does not fit the server is not a candidate.
func TestPickModelSkipsModelsThatDoNotFit(t *testing.T) {
	p := opServer(selOllama, aiv1alpha1.ServerTypeOllama, catEntryOf("big", aiv1alpha1.SizeLarge, testToolCalling), catEntryOf("fits", aiv1alpha1.SizeSmall, testToolCalling))
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Models: []aiv1alpha1.ModelCalibration{{Model: "big", Fits: false}}}
	r, _ := catalogReconciler(t, p, pilotPolicy())
	pick, err := r.pickModel(context.Background(), pilotAgent(candAgent, candReasoning), phaseMulling)
	if err != nil || pick.ModelID != "fits" {
		t.Fatalf("pick = %+v err %v", pick, err)
	}
}

// ---- portability ------------------------------------------------------------

// crewObjectVersions snapshots the resourceVersion of every object in the crew's
// namespace that the crew author owns.
func crewObjectVersions(t *testing.T, cli client.Client) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	ns := client.InNamespace(selNS)

	policies := &aiv1alpha1.CrewSchedulingPolicyList{}
	prompts := &aiv1alpha1.PromptModuleList{}
	models := &aiv1alpha1.ModelList{}
	secrets := &corev1.SecretList{}
	for _, l := range []client.ObjectList{policies, prompts, models, secrets} {
		if err := cli.List(ctx, l, ns); err != nil {
			t.Fatal(err)
		}
	}
	for i := range policies.Items {
		out["csp/"+policies.Items[i].Name] = policies.Items[i].ResourceVersion
	}
	for i := range prompts.Items {
		out["prompt/"+prompts.Items[i].Name] = prompts.Items[i].ResourceVersion
	}
	for i := range secrets.Items {
		out["secret/"+secrets.Items[i].Name] = secrets.Items[i].ResourceVersion
	}
	if len(models.Items) != 0 {
		t.Fatalf("the crew namespace must hold no Models, found %d", len(models.Items))
	}
	return out
}

// mustGet reads an object or fails the test.
func mustGet(t *testing.T, cli client.Client, key types.NamespacedName, obj client.Object) {
	t.Helper()
	if err := cli.Get(context.Background(), key, obj); err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
}

// requireSameVersions fails for every object whose resourceVersion differs between
// two snapshots, or that appeared or vanished.
func requireSameVersions(t *testing.T, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed: resourceVersion %s -> %s", k, v, after[k])
		}
	}
	if len(after) != len(before) {
		t.Errorf("objects before %v, after %v", before, after)
	}
}

// requireBoundTo fails unless the agent's Deployment records both phases on server.
func requireBoundTo(t *testing.T, cli client.Client, server string) {
	t.Helper()
	dep := &appsv1.Deployment{}
	mustGet(t, cli, types.NamespacedName{Name: candAgent, Namespace: selNS}, dep)
	if dep.Labels[labelMullingServer] != server || dep.Labels[labelTriageServer] != server {
		t.Errorf("bound mulling=%q triage=%q, want %s", dep.Labels[labelMullingServer], dep.Labels[labelTriageServer], server)
	}
}

// Portability: switching the server behind a catalog model from Ollama to vLLM
// (same model name, different engine and endpoint) re-binds every agent and changes
// nothing the crew author owns in the crew's namespace.
func TestPortabilitySwappingTheServerBehindACatalogModelLeavesTheCrewUntouched(t *testing.T) {
	ctx := context.Background()
	entry := catEntryOf(candModel14, aiv1alpha1.SizeMedium, testToolCalling, candReasoning)
	ollamaP := opServer(selOllama, aiv1alpha1.ServerTypeOllama, entry)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "crew-secret", Namespace: selNS}}
	prompt := &aiv1alpha1.PromptModule{ObjectMeta: metav1.ObjectMeta{Name: "pilot-system", Namespace: selNS}}
	r, cli := catalogReconciler(t, ollamaP, pilotPolicy(), secret, prompt, pilotAgent(candAgent, candReasoning))
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: candAgent, Namespace: selNS}}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	before := crewObjectVersions(t, cli)
	agentBefore := &aiv1alpha1.Agent{}
	mustGet(t, cli, req.NamespacedName, agentBefore)
	requireBoundTo(t, cli, selOllama)

	// The cluster owner swaps the engine: the Ollama server goes, a vLLM server
	// serving the same model name comes. Nothing in the crew's namespace is touched.
	if err := cli.Delete(ctx, ollamaP); err != nil {
		t.Fatal(err)
	}
	if err := cli.Create(ctx, opServer(selVLLM, aiv1alpha1.ServerTypeVLLM, entry)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	requireSameVersions(t, before, crewObjectVersions(t, cli))
	agentAfter := &aiv1alpha1.Agent{}
	mustGet(t, cli, req.NamespacedName, agentAfter)
	if !reflect.DeepEqual(agentAfter.Spec, agentBefore.Spec) || !reflect.DeepEqual(agentAfter.Labels, agentBefore.Labels) {
		t.Errorf("the Agent definition changed: %+v", agentAfter.Spec)
	}
	requireBoundTo(t, cli, selVLLM)
}

// ---- the policy conditions through the reconciler -----------------------------

// policyCondition reads one condition of the policy.
func policyCondition(t *testing.T, cli client.Client, key types.NamespacedName, typ string) *metav1.Condition {
	t.Helper()
	got := &aiv1alpha1.CrewSchedulingPolicy{}
	mustGet(t, cli, key, got)
	return meta.FindStatusCondition(got.Status.Conditions, typ)
}

func TestPolicyReconcileRecordsCatalogModeAndLegacyLatencyClass(t *testing.T) {
	ctx := context.Background()
	scheme := gatewayScheme(t)
	policy := pilotPolicy()
	policy.Spec.Rules[1].Prefer[0].Selector = &metav1.LabelSelector{MatchLabels: map[string]string{latencyClassLabel: candLow}}
	archetype := &aiv1alpha1.MootArchetype{ObjectMeta: metav1.ObjectMeta{Name: defaultArchetypeName}, Spec: aiv1alpha1.MootArchetypeSpec{
		Phases: []aiv1alpha1.ArchetypePhase{{Name: phaseMulling}, {Name: phaseTriage}},
	}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, archetype).WithStatusSubresource(policy).Build()
	r := &CrewSchedulingPolicyReconciler{Client: cli, Scheme: scheme}
	key := types.NamespacedName{Name: policy.Name, Namespace: selNS}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, cli, key, conditionTypeCatalogMode, metav1.ConditionTrue, catalogModeCluster)
	requireCondition(t, cli, key, conditionTypeLegacyLatencyClass, metav1.ConditionTrue, "LatencyClassSelected")

	own := candModel("own", candModel14, true, nil)
	own.Namespace = selNS
	if err := cli.Create(ctx, &own); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	requireCondition(t, cli, key, conditionTypeCatalogMode, metav1.ConditionFalse, catalogModeCrewLocal)
	requireCondition(t, cli, key, conditionTypeLegacyLatencyClass, metav1.ConditionFalse, "NotUsed")
}

// requireCondition fails unless the policy has the condition with the status and reason.
func requireCondition(t *testing.T, cli client.Client, key types.NamespacedName, typ string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	c := policyCondition(t, cli, key, typ)
	if c == nil || c.Status != status || c.Reason != reason {
		t.Errorf("%s = %+v, want %s %s", typ, c, status, reason)
	}
}
