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
	"errors"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/mesh"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	meshcontract "github.com/kubemoot/kubemoot/operator/pkg/mesh"
)

const (
	testHarnessImage = "ghcr.io/example/harness@sha256:abc"
	testGooseImage   = "reg/harness-goose:1.2.3"
	testAgentName    = "notes-reader"
	testToolFetch    = "fetch"
	testBadAgent     = "bad"
	testProvName     = "prov1"
	testProvEndpoint = "http://prov1:11434"
)

// harnessFacts compares named values with what is expected, so one test states many fields.
type harnessFacts map[string][2]any

func (f harnessFacts) check(t *testing.T) {
	t.Helper()
	for name, pair := range f {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
		}
	}
}

func sidecarAgent() *kubemootv1alpha1.Agent {
	return &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testAgentName, Namespace: testNS1, Labels: map[string]string{labelCrew: testCrewA}},
		Spec: kubemootv1alpha1.AgentSpec{
			DiscussRole: "tooler", DiscussChannels: []string{identityChannel}, TriageSummary: "Reads notes.",
			EnabledTools: []string{testToolFetch}, DisabledTools: []string{"shell_exec"},
			Harness: &kubemootv1alpha1.HarnessSpec{
				Name: identityHarnessName, Mode: kubemootv1alpha1.HarnessModeSidecar, Image: testHarnessImage,
				Env: []corev1.EnvVar{{Name: "GOOSE_MODE", Value: "headless"}},
			},
		},
	}
}

func harnessPick(model, server string) *modelPick {
	return &modelPick{
		ModelID: model, Endpoint: "http://" + server + ":11434",
		Server:     &kubemootv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: server}},
		Candidates: []modelCandidate{{Model: model, Score: 9}, {Model: "qwen3:4b", Score: 5}},
	}
}

func harnessReconciler(t *testing.T, objs ...client.Object) (*AgentReconciler, client.Client) {
	t.Helper()
	scheme := gatewayScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&kubemootv1alpha1.Agent{}).Build()
	return &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}, cli
}

func buildSidecarPod(t *testing.T, agent *kubemootv1alpha1.Agent) *appsv1.Deployment {
	t.Helper()
	r, _ := harnessReconciler(t)
	mulling, triage := harnessPick(testModelID, "rig0"), harnessPick("qwen3:4b", "rig1")
	if !usesSidecar(agent) {
		return r.buildDeployment(context.Background(), agent, mulling, triage, "notes-reader-policy", "h1")
	}
	d, err := r.buildSidecarDeployment(context.Background(), agent, mulling, triage, "notes-reader-policy", "h1")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func envNames(c corev1.Container) []string {
	names := make([]string, 0, len(c.Env))
	for _, e := range c.Env {
		names = append(names, e.Name)
	}
	return names
}

func TestSidecarModeBuildsTheHarnessBesideANativeSidecar(t *testing.T) {
	d := buildSidecarPod(t, sidecarAgent())
	spec := d.Spec.Template.Spec
	if len(spec.Containers) != 1 || len(spec.InitContainers) != 1 {
		t.Fatalf("want one harness container and the sidecar, got %+v / %+v", spec.Containers, spec.InitContainers)
	}
	sc := spec.InitContainers[0]
	harnessFacts{
		"harness name":      {spec.Containers[0].Name, harnessContainerName},
		"harness image":     {spec.Containers[0].Image, testHarnessImage},
		"sidecar name":      {sc.Name, mesh.SidecarContainerName},
		"sidecar image":     {sc.Image, FallbackAgentSidecarImage},
		"sidecar mounts":    {len(sc.VolumeMounts), 4},
		"token":             {*spec.AutomountServiceAccountToken, false},
		"pod label":         {d.Spec.Template.Labels[labelHarnessMode], "sidecar"},
		"deploy label":      {d.Labels[labelHarnessMode], "sidecar"},
		"native tools":      {d.Annotations[annoNativeTools], coreTestNone},
		"prompt hash":       {d.Spec.Template.Annotations[promptHashAnnotation], "h1"},
		"has contract hash": {d.Spec.Template.Annotations[contractHashAnnotation] != "", true},
	}.check(t)
}

func TestSidecarModeHarnessSeesNoBusNoPolicyNoCredentials(t *testing.T) {
	h := buildSidecarPod(t, sidecarAgent()).Spec.Template.Spec.Containers[0]
	for _, name := range envNames(h) {
		if strings.Contains(name, "NATS") || strings.HasPrefix(name, "KUBEMOOT_MODEL_ENDPOINT") || name == "KUBEMOOT_SYSTEM_PROMPT_FILE" ||
			name == "KUBEMOOT_GATEWAY_ENDPOINT" {
			t.Errorf("the harness got %s, a legacy runtime setting", name)
		}
	}
	want := []string{"GOOSE_MODE", mesh.EnvModelURL, mesh.EnvToolsURL, mesh.EnvTurnPort, mesh.EnvNativeTools}
	if !reflect.DeepEqual(envNames(h), want) {
		t.Errorf("harness env = %v, want %v", envNames(h), want)
	}
	if len(h.VolumeMounts) != 0 {
		t.Errorf("the harness mounts %+v", h.VolumeMounts)
	}
}

func TestHarnessImageFallsBackToTheDeploymentImage(t *testing.T) {
	agent := sidecarAgent()
	agent.Spec.Harness.Image = ""
	agent.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{Image: "legacy/image:1"}
	if got := buildSidecarPod(t, agent).Spec.Template.Spec.Containers[0].Image; got != "legacy/image:1" {
		t.Errorf("harness image = %q", got)
	}
}

func TestHarnessCommandArgsAndResourcesPassThrough(t *testing.T) {
	agent := sidecarAgent()
	agent.Spec.Harness.Command = []string{"/shim"}
	agent.Spec.Harness.Args = []string{"--serve"}
	agent.Spec.Harness.NativeTools = "declared"
	h := buildSidecarPod(t, agent).Spec.Template.Spec.Containers[0]
	if !reflect.DeepEqual(h.Command, []string{"/shim"}) || !reflect.DeepEqual(h.Args, []string{"--serve"}) {
		t.Errorf("command %v args %v", h.Command, h.Args)
	}
	var native string
	for _, e := range h.Env {
		if e.Name == mesh.EnvNativeTools {
			native = e.Value
		}
	}
	if native != "declared" {
		t.Errorf("native tools env = %q", native)
	}
}

// A legacy agent builds exactly as before: no sidecar, no mesh label, the runtime's own
// environment and policy mount. An explicit legacy harness block builds the same pod as none.
func TestLegacyModeIsUnchanged(t *testing.T) {
	plain := sidecarAgent()
	plain.Spec.Harness = nil
	explicit := plain.DeepCopy()
	explicit.Spec.Harness = &kubemootv1alpha1.HarnessSpec{Mode: kubemootv1alpha1.HarnessModeLegacy, Port: 9100, NativeTools: string(meshcontract.NativeToolsNone)}

	a, b := buildSidecarPod(t, plain), buildSidecarPod(t, explicit)
	if computeDeploymentHash(a) != computeDeploymentHash(b) || !reflect.DeepEqual(a.Spec, b.Spec) {
		t.Error("an explicit legacy harness block changed the pod")
	}
	spec := a.Spec.Template.Spec
	if len(spec.InitContainers) != 0 || spec.AutomountServiceAccountToken != nil {
		t.Errorf("a legacy pod got mesh parts: %+v", spec)
	}
	if _, ok := a.Spec.Template.Labels[labelHarnessMode]; ok {
		t.Error("a legacy pod carries the mesh label")
	}
	names := envNames(spec.Containers[0])
	for _, must := range []string{"KUBEMOOT_NATS_URL", "KUBEMOOT_SYSTEM_PROMPT_FILE", "KUBEMOOT_AGENT_NAME"} {
		if !containsString(names, must) {
			t.Errorf("legacy env lacks %s", must)
		}
	}
	if len(spec.Containers[0].VolumeMounts) == 0 || spec.Containers[0].Name != harnessContainerName {
		t.Errorf("legacy mounts = %+v", spec.Containers[0].VolumeMounts)
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestTheContractRoundTripsAndCarriesTheAgent(t *testing.T) {
	r, _ := harnessReconciler(t)
	r.ConfigCache.Update(placementServiceConfig(true, "http://placement.kubemoot:8080"))
	data, err := r.sidecarContractData(context.Background(), sidecarAgent(), harnessPick(testModelID, "rig0"), harnessPick("qwen3:4b", "rig1"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := meshcontract.Parse([]byte(data))
	if err != nil {
		t.Fatalf("the operator wrote a contract the sidecar refuses: %v\n%s", err, data)
	}
	id, def := c.Identity, c.Models.Aliases[meshcontract.DefaultAlias]
	harnessFacts{
		"identity": {id, meshcontract.Identity{Namespace: testNS1, Crew: testCrewA, Agent: testAgentName, Role: "tooler",
			Channels: []string{identityChannel}, TriageSummary: "Reads notes.", Harness: identityHarnessName}},
		"stream":          {c.Bus.Stream, bus.DiscussStream},
		"consumer":        {c.Bus.Consumer, id.ConsumerName()},
		"nats url set":    {c.Bus.NATSURL != "", true},
		"credentials dir": {c.Bus.CredentialsDir, meshcontract.NATSCredentialsDir},
		"http port":       {c.HTTPPort, int(meshcontract.DefaultHTTPPort)},
		"default models":  {def.Candidates, []string{testModelID, "qwen3:4b"}},
		"default floor":   {*def.Fallback, meshcontract.Endpoint{Server: "rig0", Endpoint: "http://rig0:11434", Model: testModelID}},
		"triage server":   {c.Models.Aliases["triage"].Fallback.Server, "rig1"},
		"turn":            {c.Turn, meshcontract.Turn{URL: "http://127.0.0.1:9000", Concurrency: 1, DeadlineSeconds: turnDeadlineSeconds}},
		"placement":       {c.Placement, meshcontract.Placement{Policy: defaultPlacementPolicy, ServiceURL: "http://placement.kubemoot:8080"}},
		"tools enabled":   {c.Tools.Enabled, []string{testToolFetch}},
		"tools disabled":  {c.Tools.Disabled, []string{"shell_exec"}},
		"native tools":    {c.Tools.NativeTools, meshcontract.NativeToolsNone},
		"system prompt":   {c.Policy.SystemPromptFile, "/etc/kubemoot/policy/system.txt"},
		"no otlp":         {c.Observability == nil, true},
	}.check(t)
}

func TestThePlacementServiceIsLeftOutWhenTheOperatorHasNone(t *testing.T) {
	r, _ := harnessReconciler(t)
	data, err := r.sidecarContractData(context.Background(), sidecarAgent(), harnessPick(testModelID, "rig0"), harnessPick(testModelID, "rig0"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := meshcontract.Parse([]byte(data))
	if c.Placement.ServiceURL != "" {
		t.Errorf("service URL = %q", c.Placement.ServiceURL)
	}
}

func TestAChangeToTheContractRollsThePod(t *testing.T) {
	before := buildSidecarPod(t, sidecarAgent())
	changed := sidecarAgent()
	changed.Spec.EnabledTools = []string{testToolFetch, "time_get_current_time"}
	after := buildSidecarPod(t, changed)
	if before.Spec.Template.Annotations[contractHashAnnotation] == after.Spec.Template.Annotations[contractHashAnnotation] {
		t.Error("the contract hash did not change")
	}
	if computeDeploymentHash(before) == computeDeploymentHash(after) {
		t.Error("the Deployment hash did not change, so the pod would not roll")
	}
}

func TestASidecarAgentThatCannotBeBuiltIsRefusedWithReasons(t *testing.T) {
	cases := map[string]func(*kubemootv1alpha1.Agent){
		"no crew label": func(a *kubemootv1alpha1.Agent) { a.Labels = nil },
		"no image":      func(a *kubemootv1alpha1.Agent) { a.Spec.Harness.Image, a.Spec.Harness.Name = "", "no-built-in-image" },
		"the same port": func(a *kubemootv1alpha1.Agent) { a.Spec.Harness.Port = 8080 },
		"the deployment's port": func(a *kubemootv1alpha1.Agent) {
			a.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{Image: "x", Port: 9000}
		},
		"a dotted agent name":    func(a *kubemootv1alpha1.Agent) { a.Name = "a.b" },
		"a dotted channel token": func(a *kubemootv1alpha1.Agent) { a.Spec.DiscussChannels = []string{"a.b"} },
	}
	r, _ := harnessReconciler(t)
	for name, mutate := range cases {
		a := sidecarAgent()
		mutate(a)
		if _, err := r.sidecarContractData(context.Background(), a, harnessPick(testModelID, "rig0"), harnessPick(testModelID, "rig0")); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func schedulableFixtures() (*kubemootv1alpha1.ModelServer, *kubemootv1alpha1.Model) {
	server := &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: testProvName, Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeOllama, Endpoint: testProvEndpoint},
		Status:     kubemootv1alpha1.ModelServerStatus{Ready: true},
	}
	model := &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: testNS1},
		Spec:       kubemootv1alpha1.ModelSpec{Model: testModelID, ServerRef: testProvName},
		Status:     kubemootv1alpha1.ModelStatus{Ready: true},
	}
	return server, model
}

func reconcileByName(t *testing.T, r *AgentReconciler, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNS1}}); err != nil {
			t.Fatalf("reconcile %s: %v", name, err)
		}
	}
}

func TestReconcileWritesTheContractOwnedByItsAgent(t *testing.T) {
	server, model := schedulableFixtures()
	r, cli := harnessReconciler(t, sidecarAgent(), server, model)
	reconcileByName(t, r, testAgentName)
	cm := &corev1.ConfigMap{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: testAgentName + "-contract", Namespace: testNS1}, cm); err != nil {
		t.Fatalf("no contract ConfigMap: %v", err)
	}
	_, parseErr := meshcontract.Parse([]byte(cm.Data[meshcontract.ContractFile]))
	deps := &appsv1.DeploymentList{}
	listErr := cli.List(context.Background(), deps, client.InNamespace(testNS1))
	harnessFacts{
		"parse error":  {parseErr, error(nil)},
		"list error":   {listErr, error(nil)},
		"owner":        {cm.OwnerReferences[0].Name, testAgentName},
		"deployments":  {len(deps.Items), 1},
		"sidecar init": {deps.Items[0].Spec.Template.Spec.InitContainers[0].Name, mesh.SidecarContainerName},
	}.check(t)
}

func TestReconcileRefusesAnInvalidAgentInItsStatus(t *testing.T) {
	bad := sidecarAgent()
	bad.Name, bad.Labels = testBadAgent, nil
	server, model := schedulableFixtures()
	r, cli := harnessReconciler(t, bad, server, model)
	reconcileByName(t, r, testBadAgent)
	got := &kubemootv1alpha1.Agent{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: testBadAgent, Namespace: testNS1}, got); err != nil {
		t.Fatal(err)
	}
	deps := &appsv1.DeploymentList{}
	_ = cli.List(context.Background(), deps, client.InNamespace(testNS1))
	harnessFacts{
		jsonKeyPhase:  {got.Status.Phase, phaseError},
		"ready":       {got.Status.Ready, false},
		"names label": {strings.Contains(got.Status.Message, labelCrew), true},
		"deployments": {len(deps.Items), 0},
	}.check(t)
}

func TestTheServiceTargetsTheSidecarsPort(t *testing.T) {
	r, cli := harnessReconciler(t)
	agent := sidecarAgent()
	if err := r.ensureService(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	svc := &corev1.Service{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, svc); err != nil {
		t.Fatal(err)
	}
	if svc.Spec.Ports[0].Port != 8080 || svc.Spec.Ports[0].TargetPort.IntValue() != 8080 {
		t.Errorf("service ports = %+v", svc.Spec.Ports)
	}
	if sidecarHTTPPort(agent) != 8080 || harnessPort(agent.Spec.Harness) != 9000 {
		t.Error("the sidecar and the harness need their default ports 8080 and 9000")
	}
}

type fakeEnsurer struct {
	stream, consumer string
	filters          []string
	err              error
}

func (f *fakeEnsurer) EnsureConsumer(stream, consumer string, filters []string) error {
	f.stream, f.consumer, f.filters = stream, consumer, filters
	return f.err
}

func TestTheSidecarsDurableConsumerIsEnsuredByName(t *testing.T) {
	f := &fakeEnsurer{}
	ensureSidecarConsumer(context.Background(), f, sidecarAgent())
	if f.stream != "KUBEMOOT_DISCUSS" || f.consumer != "sidecar-ns1-crew-a-notes-reader" {
		t.Errorf("ensured %s/%s", f.stream, f.consumer)
	}
	want := []string{"kubemoot.discuss.ns1.crew-a.broadcast.>", "kubemoot.discuss.ns1.crew-a.general.>"}
	if !reflect.DeepEqual(f.filters, want) {
		t.Errorf("filters = %v, want %v", f.filters, want)
	}
}

func TestAConsumerFailureNeverFailsTheReconcile(t *testing.T) {
	f := &fakeEnsurer{err: errors.New("nats down")}
	ensureSidecarConsumer(context.Background(), f, sidecarAgent()) // must not panic or return
	r, _ := harnessReconciler(t)
	stop, err := r.reconcileHarness(context.Background(), sidecarAgent(), harnessPick(testModelID, "rig0"), harnessPick(testModelID, "rig0"))
	if stop || err != nil {
		t.Errorf("no publisher is a no-op, got stop=%v err=%v", stop, err)
	}
}

func TestAMeshPatchFailureFailsTheReconcile_andMarksTheAgentError(t *testing.T) {
	agent := sidecarAgent()
	r, cli := harnessReconciler(t, agent)
	orig := applySidecar
	applySidecar = func(*corev1.PodTemplateSpec, string, mesh.SidecarPatch) error {
		return errors.New("no harness container")
	}
	t.Cleanup(func() { applySidecar = orig })

	err := r.ensureDeployment(context.Background(), agent, harnessPick(testModelID, "rig0"), harnessPick(testModelID, "rig0"), "policy")
	if err == nil || !strings.Contains(err.Error(), "no harness container") {
		t.Fatalf("ensureDeployment err = %v, want the mesh patch failure", err)
	}
	got := &kubemootv1alpha1.Agent{}
	if gerr := cli.Get(context.Background(), client.ObjectKeyFromObject(agent), got); gerr != nil {
		t.Fatal(gerr)
	}
	if got.Status.Phase != "Error" || !strings.Contains(got.Status.Message, "Moot Mesh") {
		t.Errorf("status = %q %q, want Error naming the mesh", got.Status.Phase, got.Status.Message)
	}
	if derr := cli.Get(context.Background(), client.ObjectKeyFromObject(agent), &appsv1.Deployment{}); derr == nil {
		t.Error("a Deployment was created despite the failure")
	}
}

func TestHarnessImageDefaultsByHarnessName(t *testing.T) {
	r, _ := harnessReconciler(t)
	r.ConfigCache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Images: kubemootv1alpha1.ImageConfig{HarnessGoose: testGooseImage},
	}})
	agentWith := func(h kubemootv1alpha1.HarnessSpec, dep *kubemootv1alpha1.DeploymentConfig) *kubemootv1alpha1.Agent {
		a := sidecarAgent()
		a.Spec.Harness, a.Spec.Deployment = &h, dep
		return a
	}
	sidecar := kubemootv1alpha1.HarnessModeSidecar
	cases := map[string]struct {
		agent *kubemootv1alpha1.Agent
		want  string
	}{
		"goose takes the Kubemoot image":     {agentWith(kubemootv1alpha1.HarnessSpec{Name: HarnessNameGoose, Mode: sidecar}, nil), testGooseImage},
		"its own image wins":                 {agentWith(kubemootv1alpha1.HarnessSpec{Name: HarnessNameGoose, Mode: sidecar, Image: testHarnessImage}, nil), testHarnessImage},
		"the deployment image wins":          {agentWith(kubemootv1alpha1.HarnessSpec{Name: HarnessNameGoose, Mode: sidecar}, &kubemootv1alpha1.DeploymentConfig{Image: "dep/img:1"}), "dep/img:1"},
		"another harness has no default":     {agentWith(kubemootv1alpha1.HarnessSpec{Name: "opencode", Mode: sidecar}, nil), ""},
		"an empty deployment image is unset": {agentWith(kubemootv1alpha1.HarnessSpec{Name: HarnessNameGoose, Mode: sidecar}, &kubemootv1alpha1.DeploymentConfig{}), testGooseImage},
	}
	for name, c := range cases {
		if got := r.harnessImage(c.agent); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if err := validateSidecarAgent(cases["another harness has no default"].agent, ""); err == nil {
		t.Error("a harness with no image anywhere was accepted")
	}
	noCache := &AgentReconciler{}
	if got := noCache.harnessImage(cases["goose takes the Kubemoot image"].agent); got != FallbackHarnessGooseImage {
		t.Errorf("no cache: %q", got)
	}
}

func TestAGooseAgentWithoutAnImageRunsTheKubemootBuiltOne(t *testing.T) {
	a := sidecarAgent()
	a.Spec.Harness.Name, a.Spec.Harness.Image = HarnessNameGoose, ""
	d := buildSidecarPod(t, a)
	if got := d.Spec.Template.Spec.Containers[0].Image; got != FallbackHarnessGooseImage {
		t.Fatalf("harness image = %q", got)
	}
}
