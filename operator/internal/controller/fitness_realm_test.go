package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	realmNS      = "crew-realm"
	realmCrew    = "realm-crew"
	realmServer1 = "gpu-a"
	realmModelNS = "kubemoot"
	realmAgent   = "agent"
	realmOpVer   = "1.0.0"
	realmCrewVer = "0.4.0"
	realmEngine  = "vllm"
	realm2Gi     = "2Gi"
	realmAbc     = "abc"
)

func realmServerCR(name string, vramTotal int64, loaded ...string) *kubemootv1alpha1.ModelServer {
	s := &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: realmModelNS},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeVLLM},
		Status: kubemootv1alpha1.ModelServerStatus{
			Ready:      true,
			ServerInfo: &kubemootv1alpha1.ServerInfo{Version: "0.9.1"},
			Capacity: &kubemootv1alpha1.DiscoveredCapacity{
				VRAMTotalMiB: vramTotal, VRAMUsedMiB: 1000, MaxParallel: 4, ContextLength: 8192,
				GPUModel: "Some Vendor Device 9000",
			},
		},
	}
	for _, l := range loaded {
		s.Status.Capacity.LoadedModels = append(s.Status.Capacity.LoadedModels, kubemootv1alpha1.LoadedModel{Name: l})
	}
	return s
}

// bareServer is a model server that has reported nothing yet.
func bareServer(name string) *kubemootv1alpha1.ModelServer {
	return &kubemootv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: realmModelNS},
		Spec:       kubemootv1alpha1.ModelServerSpec{Type: kubemootv1alpha1.ServerTypeOllama},
	}
}

func realmModelCR(name, server, model, digest string, disabled bool) *kubemootv1alpha1.Model {
	return &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: realmModelNS},
		Spec:       kubemootv1alpha1.ModelSpec{ServerRef: server, Model: model, Quantization: "q4", ContextLength: 4096, Disabled: disabled},
		Status:     kubemootv1alpha1.ModelStatus{ModelInfo: &kubemootv1alpha1.ModelInfo{Digest: digest, ContextLength: 8192}},
	}
}

func realmPod(name, crew string, phase corev1.PodPhase, labels map[string]string, c corev1.Container) *corev1.Pod {
	l := map[string]string{labelCrew: crew}
	for k, v := range labels {
		l[k] = v
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: realmNS, Labels: l},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{c}},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func agentContainer(cpuReq, cpuLim, memReq, memLim string) corev1.Container {
	rl := func(c, m string) corev1.ResourceList {
		out := corev1.ResourceList{}
		if c != "" {
			out[corev1.ResourceCPU] = resource.MustParse(c)
		}
		if m != "" {
			out[corev1.ResourceMemory] = resource.MustParse(m)
		}
		return out
	}
	return corev1.Container{Name: realmAgent, Image: "agent-runtime:1",
		Resources: corev1.ResourceRequirements{Requests: rl(cpuReq, memReq), Limits: rl(cpuLim, memLim)}}
}

func realmSuite(name string) *kubemootv1alpha1.CrewFitnessSuite {
	return &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: realmNS},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: realmCrew, Iterations: 2, Scripts: scripts("a")},
	}
}

func TestBuildRealmSnapshotRecordsServersModelsAndCapability(t *testing.T) {
	in := realmInputs{
		suite:           realmSuite("self"),
		operatorVersion: realmOpVer,
		crewVersion:     realmCrewVer,
		servers:         []kubemootv1alpha1.ModelServer{*bareServer("z-server"), *realmServerCR(realmServer1, 32768, "m-loaded")},
		models: []kubemootv1alpha1.Model{
			*realmModelCR("m1", realmServer1, "m-loaded", "sha256:aaa", false),
			*realmModelCR("m2", realmServer1, "m-cold", "sha256:bbb", false),
			*realmModelCR("m3", realmServer1, "m-off", "sha256:ccc", true),
			*realmModelCR("m4", "other-server", "m-else", "sha256:ddd", false),
		},
		now: metav1.NewTime(time.Unix(1_700_000_000, 0)),
	}
	snap := buildRealmSnapshot(in)
	if snap.OperatorVersion != realmOpVer || snap.CrewVersion != realmCrewVer {
		t.Errorf("versions not recorded: %+v", snap)
	}
	if len(snap.ModelServers) != 2 || snap.ModelServers[0].Name != realmServer1 {
		t.Fatalf("servers must be sorted by name: %+v", snap.ModelServers)
	}
	assertServerFacts(t, snap.ModelServers[0])
	assertServerModels(t, snap.ModelServers[0].Models)
	if snap.ModelServers[1].Accelerator != nil {
		t.Errorf("a server with no capacity has no accelerator: %+v", snap.ModelServers[1].Accelerator)
	}
}

func assertServerFacts(t *testing.T, a kubemootv1alpha1.RealmModelServer) {
	t.Helper()
	if a.EngineVersion != "0.9.1" || a.Engine != realmEngine || !a.Ready {
		t.Errorf("engine facts missing: %+v", a)
	}
	if a.Accelerator == nil || a.Accelerator.MemoryMiB != 32768 || a.Accelerator.MaxParallel != 4 || a.Accelerator.ContextLength != 8192 {
		t.Errorf("accelerator capability wrong: %+v", a.Accelerator)
	}
}

func assertServerModels(t *testing.T, models []kubemootv1alpha1.RealmModel) {
	t.Helper()
	if len(models) != 2 || models[0].Name != "m-cold" || models[1].Name != "m-loaded" {
		t.Fatalf("models: want the two enabled models on this server, sorted: %+v", models)
	}
	if models[0].Resident || !models[1].Resident {
		t.Errorf("residency wrong (cold vs warm): %+v", models)
	}
	if models[1].Digest != "sha256:aaa" || models[1].ContextLength != 8192 || models[1].Quantization != "q4" {
		t.Errorf("model facts wrong (status context overrides spec): %+v", models[1])
	}
}

func TestRealmAcceleratorNamesNoDevice(t *testing.T) {
	acc := realmAccelerator(realmServerCR(realmServer1, 24576))
	if acc == nil {
		t.Fatal("expected capability")
	}
	// The record carries capability only: no field of RealmAccelerator can hold a
	// vendor or device name, and the device name the server reported must not leak.
	for _, v := range []string{"Vendor", "9000"} {
		if strings.Contains(strings.ToLower(fmt.Sprintf("%+v", *acc)), strings.ToLower(v)) {
			t.Errorf("accelerator record names a device: %s", fmt.Sprintf("%+v", *acc))
		}
	}
}

func TestRealmAcceleratorFallsBackToSchedulingMemory(t *testing.T) {
	s := &kubemootv1alpha1.ModelServer{Spec: kubemootv1alpha1.ModelServerSpec{
		Scheduling: &kubemootv1alpha1.ServerScheduling{MemoryMiB: 16384}}}
	if acc := realmAccelerator(s); acc == nil || acc.MemoryMiB != 16384 {
		t.Errorf("want 16384 MiB from spec.scheduling, got %+v", acc)
	}
	if realmAccelerator(&kubemootv1alpha1.ModelServer{}) != nil {
		t.Error("no capacity and no scheduling memory: no accelerator")
	}
}

func TestRealmAgentsGroupsCrewPodsOnly(t *testing.T) {
	same := agentContainer("500m", "2", "1Gi", realm2Gi)
	pods := []corev1.Pod{
		*realmPod("p1", realmCrew, corev1.PodRunning, nil, same),
		*realmPod("p2", realmCrew, corev1.PodRunning, nil, same),
		*realmPod("p3", realmCrew, corev1.PodRunning, nil, agentContainer("", "", "", "")),
		*realmPod("pending", realmCrew, corev1.PodPending, nil, same),
		*realmPod("harness", realmCrew, corev1.PodRunning, map[string]string{labelFitnessHarness: "true"}, same),
		*realmPod("other-crew", "someone-else", corev1.PodRunning, nil, same),
	}
	got := realmAgents(pods, realmCrew)
	if len(got) != 2 {
		t.Fatalf("want 2 distinct shapes, got %+v", got)
	}
	var shaped, bare kubemootv1alpha1.RealmAgentContainer
	for _, g := range got {
		if g.CPURequest == "" {
			bare = g
		} else {
			shaped = g
		}
	}
	if shaped.Pods != 2 || shaped.CPURequest != "500m" || shaped.CPULimit != "2" || shaped.MemoryRequest != "1Gi" || shaped.MemoryLimit != realm2Gi {
		t.Errorf("grouped shape wrong: %+v", shaped)
	}
	if bare.Pods != 1 {
		t.Errorf("unset resources form their own group: %+v", bare)
	}
}

func TestBuildRealmSnapshotFlagsThrottledAgents(t *testing.T) {
	in := realmInputs{
		suite: realmSuite("self"),
		pods:  []corev1.Pod{*realmPod("p1", realmCrew, corev1.PodRunning, nil, agentContainer("1", "1", "", realm2Gi))},
	}
	notes := buildRealmSnapshot(in).ResourceNotes
	if len(notes) != 2 {
		t.Fatalf("want a pinned cpu note and an unset memory request note, got %v", notes)
	}
}

func TestRealmConcurrencyCountsOthersOnly(t *testing.T) {
	self := realmSuite("self")
	mk := func(ns, name string, phase kubemootv1alpha1.CrewFitnessSuitePhase) kubemootv1alpha1.CrewFitnessSuite {
		return kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{Phase: phase},
		}
	}
	suites := []kubemootv1alpha1.CrewFitnessSuite{
		mk(realmNS, "self", kubemootv1alpha1.CrewFitnessSuitePhaseRunning),
		mk("crew-b", "other", kubemootv1alpha1.CrewFitnessSuitePhaseRunning),
		mk("crew-b", "done", kubemootv1alpha1.CrewFitnessSuitePhaseCompleted),
		mk("crew-b", "paused", kubemootv1alpha1.CrewFitnessSuitePhasePaused),
	}
	run := func(owner string, phase kubemootv1alpha1.CrewFitnessPhase) kubemootv1alpha1.CrewFitness {
		cf := kubemootv1alpha1.CrewFitness{Status: kubemootv1alpha1.CrewFitnessStatus{Phase: phase}}
		if owner != "" {
			cf.Labels = map[string]string{suiteOwnerLabel: owner}
		}
		return cf
	}
	runs := []kubemootv1alpha1.CrewFitness{
		run("self", kubemootv1alpha1.CrewFitnessPhaseRunning), // this suite's own trial
		run("", kubemootv1alpha1.CrewFitnessPhaseRunning),     // standalone, in flight
		run("", kubemootv1alpha1.CrewFitnessPhasePassed),      // standalone, finished
	}
	names, standalone := realmConcurrency(self, suites, runs)
	if len(names) != 1 || names[0] != "crew-b/other" || standalone != 1 {
		t.Errorf("got %v and %d, want [crew-b/other] and 1", names, standalone)
	}
}

func TestRealmCaps(t *testing.T) {
	servers := make([]kubemootv1alpha1.ModelServer, 0, kubemootv1alpha1.MaxRealmServers+5)
	for i := range kubemootv1alpha1.MaxRealmServers + 5 {
		servers = append(servers, *realmServerCR("s"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26)), 0))
	}
	if got := realmServers(servers, nil); len(got) != kubemootv1alpha1.MaxRealmServers {
		t.Errorf("servers not capped: %d", len(got))
	}
	models := make([]kubemootv1alpha1.Model, 0, kubemootv1alpha1.MaxRealmModels+5)
	for i := range kubemootv1alpha1.MaxRealmModels + 5 {
		models = append(models, *realmModelCR("m", realmServer1, "model-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "", false))
	}
	s := realmServerCR(realmServer1, 0)
	s.Namespace = realmModelNS
	if got := realmModels(s, models); len(got) != kubemootv1alpha1.MaxRealmModels {
		t.Errorf("models not capped: %d", len(got))
	}
}

func realmClient(t *testing.T, objs ...client.Object) (client.WithWatch, *runtime.Scheme) {
	t.Helper()
	scheme := reapScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&kubemootv1alpha1.ModelServer{}).Build(), scheme
}

func TestRecordRealmStartAndEnd(t *testing.T) {
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: realmCrew, Namespace: realmNS, Labels: map[string]string{crewVersionLabel: realmCrewVer}}}
	cli, scheme := realmClient(t, crew, realmServerCR(realmServer1, 32768, "m1"),
		realmModelCR("m1", realmServer1, "m1", "sha256:aaa", false),
		realmPod("p1", realmCrew, corev1.PodRunning, nil, agentContainer("500m", "2", "1Gi", realm2Gi)))
	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme, OperatorVersion: realmOpVer}
	suite := realmSuite("s")

	r.recordRealmEnd(context.Background(), suite)
	if suite.Status.Realm != nil {
		t.Fatal("an end without a start must not invent a realm")
	}

	r.recordRealmStart(context.Background(), suite)
	realm := suite.Status.Realm
	if realm == nil || realm.Start == nil || realm.Digests == nil || realm.End != nil {
		t.Fatalf("start not recorded: %+v", realm)
	}
	if realm.Start.CrewVersion != realmCrewVer || realm.Start.OperatorVersion != realmOpVer || len(realm.Start.ModelServers) != 1 {
		t.Errorf("start content wrong: %+v", realm.Start)
	}

	upgradeEngine(t, cli, "0.10.0") // the system moves during the run
	r.recordRealmEnd(context.Background(), suite)
	assertEndShowsUpgrade(t, realm)
	first := realm.End.ObservedAt
	time.Sleep(10 * time.Millisecond)
	r.recordRealmEnd(context.Background(), suite)
	if !realm.End.ObservedAt.Equal(&first) {
		t.Error("the end snapshot is recorded once")
	}
}

func assertEndShowsUpgrade(t *testing.T, realm *kubemootv1alpha1.FitnessRealm) {
	t.Helper()
	drift := strings.Join(realm.Drift, "\n")
	if realm.End == nil || !strings.Contains(drift, "0.10.0") || !strings.Contains(drift, "0.9.1") {
		t.Fatalf("drift must show the engine upgrade, both versions: %+v", realm.Drift)
	}
}

func upgradeEngine(t *testing.T, cli client.Client, version string) {
	t.Helper()
	srv := &kubemootv1alpha1.ModelServer{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: realmModelNS, Name: realmServer1}, srv); err != nil {
		t.Fatal(err)
	}
	srv.Status.ServerInfo.Version = version
	if err := cli.Status().Update(context.Background(), srv); err != nil {
		t.Fatal(err)
	}
}

func TestRecordRealmStartFailureLeavesRealmAbsentThenRetries(t *testing.T) {
	cli, scheme := realmClient(t)
	failing := true
	wrapped := interceptor.NewClient(cli, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if failing {
				return errors.New("apiserver unavailable")
			}
			return c.List(ctx, list, opts...)
		},
	})
	r := &CrewFitnessSuiteReconciler{Client: wrapped, Scheme: scheme}
	suite := realmSuite("s")

	r.recordRealmStart(context.Background(), suite)
	if suite.Status.Realm != nil {
		t.Fatal("a snapshot with a part missing would raise false warnings: record nothing")
	}
	r.retryRealmStart(context.Background(), suite)
	if suite.Status.Realm != nil {
		t.Fatal("still failing")
	}
	failing = false
	r.retryRealmStart(context.Background(), suite)
	if suite.Status.Realm == nil || suite.Status.Realm.Start == nil {
		t.Fatal("retry must record the start once the read works")
	}
}

func TestRetryRealmStartStopsAfterAnIterationFinished(t *testing.T) {
	cli, scheme := realmClient(t)
	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme}
	suite := realmSuite("s")
	suite.Status.IterationsCompleted = 1
	r.retryRealmStart(context.Background(), suite)
	if suite.Status.Realm != nil {
		t.Fatal("a start taken after trials ran would not be the realm the run began on")
	}
}

func TestCopyRealm(t *testing.T) {
	from := kubemootv1alpha1.RejudgeSource{Suite: "src", RunID: "r1"}
	if copyRealm(nil, from) != nil || copyRealm(&kubemootv1alpha1.CrewFitnessSuite{}, from) != nil {
		t.Fatal("a source without a realm gives no realm")
	}
	src := &kubemootv1alpha1.CrewFitnessSuite{Status: kubemootv1alpha1.CrewFitnessSuiteStatus{
		Realm: &kubemootv1alpha1.FitnessRealm{Digests: &kubemootv1alpha1.RealmDigests{Servers: realmAbc}},
	}}
	got := copyRealm(src, from)
	if got.Digests.Servers != realmAbc || got.CopiedFrom == nil || *got.CopiedFrom != from {
		t.Errorf("copy wrong: %+v", got)
	}
	if src.Status.Realm.CopiedFrom != nil {
		t.Error("the source's own realm must not change")
	}
	got.Digests.Servers = "changed"
	if src.Status.Realm.Digests.Servers != realmAbc {
		t.Error("the copy must not share memory with the source")
	}
}

func TestRealmRows(t *testing.T) {
	if rows := realmRows(nil); len(rows) != 1 || !strings.Contains(rows[0].detail, "did not record") {
		t.Errorf("no realm must say so: %+v", rows)
	}
	snap := buildRealmSnapshot(realmInputs{
		suite:   realmSuite("s"),
		servers: []kubemootv1alpha1.ModelServer{*realmServerCR(realmServer1, 32768, "m1")},
		models:  []kubemootv1alpha1.Model{*realmModelCR("m1", realmServer1, "m1", "sha256:aaa", false)},
		pods:    []corev1.Pod{*realmPod("p", realmCrew, corev1.PodRunning, nil, agentContainer("", "", "", ""))},
		now:     metav1.NewTime(time.Unix(1_700_000_000, 0)),
	})
	rows := realmRows(&kubemootv1alpha1.FitnessRealm{
		Start: snap, End: snap, Drift: []string{"server new or changed: x"},
		CopiedFrom: &kubemootv1alpha1.RejudgeSource{Suite: "src", RunID: "r1"},
		Digests:    &kubemootv1alpha1.RealmDigests{Servers: "s1", Models: "m1", Versions: "v1", Resources: "r1"},
	})
	text := make([]string, 0, len(rows))
	for _, r := range rows {
		text = append(text, r.section+"|"+r.item+"|"+r.detail)
	}
	all := strings.Join(text, "\n")
	for _, want := range []string{"realm|copied from|src run r1", "digest|servers|s1", "drift|start to end|server new", "start|model server kubemoot/gpu-a", "end|model m1|on gpu-a; resident; digest sha256:aaa", "resource note"} {
		if !strings.Contains(all, want) {
			t.Errorf("rows missing %q in:\n%s", want, all)
		}
	}
}

func TestReportCarriesRealm(t *testing.T) {
	suite := realmSuite("s")
	suite.Status = kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "r1", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted}
	results := []IterationResult{{Scenario: "a", Iteration: 1, StartedAt: time.Unix(1_700_000_000, 0), Phase: kubemootv1alpha1.CrewFitnessPhasePassed}}

	read := func() (overview, realm [][]string) {
		b, err := BuildFitnessSuiteXLSX(suite, results)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		f, err := excelize.OpenReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		overview, _ = f.GetRows(sheetOverview)
		realm, _ = f.GetRows(sheetRealm)
		return overview, realm
	}

	overview, realm := read()
	if !hasRow(overview, "Realm", "not recorded") || len(realm) != 2 {
		t.Errorf("a run without a realm says so: overview %v realm %v", overview, realm)
	}

	snap := buildRealmSnapshot(realmInputs{suite: suite, servers: []kubemootv1alpha1.ModelServer{*realmServerCR(realmServer1, 32768)},
		now: metav1.NewTime(time.Unix(1_700_000_000, 0))})
	suite.Status.Realm = &kubemootv1alpha1.FitnessRealm{Start: snap, End: snap,
		Digests: &kubemootv1alpha1.RealmDigests{Servers: "s1", Models: "m1", Versions: "v1", Resources: "r1"},
		Drift:   []string{"version new or changed: operator=2"}}
	overview, realm = read()
	if !hasRow(overview, "Realm digests", "servers s1") || !hasRow(overview, "Realm drift", "1 change(s)") {
		t.Errorf("overview must carry the digests and the drift: %v", overview)
	}
	if realm[0][0] != "section" || len(realm) < 6 {
		t.Errorf("realm sheet too short: %v", realm)
	}
}

func hasRow(rows [][]string, label, contains string) bool {
	for _, r := range rows {
		if len(r) >= 2 && r[0] == label && strings.Contains(r[1], contains) {
			return true
		}
	}
	return false
}
