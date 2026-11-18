package fitnessrealm

import (
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Memory quantities the fixtures repeat.
const (
	oneGi = "1Gi"
	twoGi = "2Gi"
)

func snapshot() *kubemootv1alpha1.RealmSnapshot {
	return &kubemootv1alpha1.RealmSnapshot{
		ObservedAt:      metav1.NewTime(time.Unix(1_700_000_000, 0)),
		OperatorVersion: "1.2.3",
		CrewVersion:     "0.4.0",
		TrialIsolation:  "PerTrial",
		ModelServers: []kubemootv1alpha1.RealmModelServer{
			{
				Name: "fast", Namespace: "kubemoot", Engine: "vllm", EngineVersion: "0.9",
				Accelerator: &kubemootv1alpha1.RealmAccelerator{MemoryMiB: 32768, MemoryUsedMiB: 100, MaxParallel: 4, ContextLength: 8192},
				Models: []kubemootv1alpha1.RealmModel{
					{Name: "m1", Digest: "sha256:aaa", Quantization: "q4", ContextLength: 8192, Resident: true},
					{Name: "m2", Digest: "sha256:bbb"},
				},
			},
			{Name: "slow", Namespace: "kubemoot", Engine: "ollama", EngineVersion: "0.12"},
		},
		Agents: []kubemootv1alpha1.RealmAgentContainer{
			{Container: "agent", Image: "agent-runtime:1", Pods: 3, CPURequest: "500m", CPULimit: "2", MemoryRequest: oneGi, MemoryLimit: twoGi},
		},
	}
}

func TestDigestsOfNilSnapshot(t *testing.T) {
	if DigestsOf(nil) != nil {
		t.Fatal("nil snapshot has no digests")
	}
}

func TestDigestsIgnoreOrderTimeResidencyUseAndConcurrency(t *testing.T) {
	a := snapshot()
	b := snapshot()
	slices.Reverse(b.ModelServers)
	slices.Reverse(b.ModelServers[1].Models)
	b.ObservedAt = metav1.NewTime(time.Unix(1_800_000_000, 0))
	for i := range b.ModelServers[1].Models {
		b.ModelServers[1].Models[i].Resident = false // m1 is not resident in b
	}
	b.ModelServers[1].Accelerator.MemoryUsedMiB = 9999
	b.ConcurrentSuites = []string{"ns/other"}
	b.ConcurrentRuns = 2
	b.Agents[0].Pods = 5
	if got, want := DigestsOf(b), DigestsOf(a); *got != *want {
		t.Fatalf("digests must not move with order, time, residency, memory in use, concurrency or pod count:\n got %+v\nwant %+v", got, want)
	}
	if got := DifferingParts(DigestsOf(a), DigestsOf(b)); len(got) != 0 {
		t.Fatalf("no differing parts expected, got %v", got)
	}
}

func TestDigestsMoveWithEachComparablePart(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*kubemootv1alpha1.RealmSnapshot)
		part   string
	}{
		{"engine version", func(s *kubemootv1alpha1.RealmSnapshot) { s.ModelServers[0].EngineVersion = "0.10" }, PartServers},
		{"accelerator memory", func(s *kubemootv1alpha1.RealmSnapshot) { s.ModelServers[0].Accelerator.MemoryMiB = 24576 }, PartServers},
		{"model digest", func(s *kubemootv1alpha1.RealmSnapshot) { s.ModelServers[0].Models[0].Digest = "sha256:ccc" }, PartModels},
		{"model quantization", func(s *kubemootv1alpha1.RealmSnapshot) { s.ModelServers[0].Models[1].Quantization = "q8" }, PartModels},
		{"operator version", func(s *kubemootv1alpha1.RealmSnapshot) { s.OperatorVersion = "1.2.4" }, PartVersions},
		{"agent image", func(s *kubemootv1alpha1.RealmSnapshot) { s.Agents[0].Image = "agent-runtime:2" }, PartVersions},
		{"memory limit", func(s *kubemootv1alpha1.RealmSnapshot) { s.Agents[0].MemoryLimit = "4Gi" }, PartResources},
		{"trial isolation", func(s *kubemootv1alpha1.RealmSnapshot) { s.TrialIsolation = "PerSuite" }, PartIsolation},
	}
	base := DigestsOf(snapshot())
	for _, c := range cases {
		s := snapshot()
		c.mutate(s)
		if got := DifferingParts(base, DigestsOf(s)); !slices.Equal(got, []string{c.part}) {
			t.Errorf("%s: differing parts = %v, want [%s]", c.name, got, c.part)
		}
	}
}

func TestDifferingPartsUnknownWhenARealmIsMissing(t *testing.T) {
	d := DigestsOf(snapshot())
	if got := DifferingParts(nil, d); !slices.Equal(got, []string{"unknown"}) {
		t.Errorf("got %v", got)
	}
	if got := DifferingParts(d, nil); !slices.Equal(got, []string{"unknown"}) {
		t.Errorf("got %v", got)
	}
}

func TestDigestLength(t *testing.T) {
	if n := len(DigestsOf(snapshot()).Servers); n != kubemootv1alpha1.RealmDigestChars {
		t.Errorf("digest length = %d, want %d", n, kubemootv1alpha1.RealmDigestChars)
	}
}

func TestDifferencesNameWhatChanged(t *testing.T) {
	a, b := snapshot(), snapshot()
	b.ModelServers[0].Models[0].Digest = "sha256:new"
	b.ModelServers[0].Models[1].Resident = true
	b.ConcurrentSuites = []string{"ns/other"}
	got := strings.Join(Differences(a, b, 0), "\n")
	for _, want := range []string{"model changed or removed: kubemoot/fast m1", "model new or changed: kubemoot/fast m1", "residency new or changed: kubemoot/fast m2 resident", "concurrent suites: [] -> [ns/other]"} {
		if !strings.Contains(got, want) {
			t.Errorf("differences missing %q in:\n%s", want, got)
		}
	}
	if Differences(nil, b, 0) != nil || Differences(a, nil, 0) != nil {
		t.Error("a missing snapshot yields no differences")
	}
}

func TestDriftLeavesOutResidencyAndHonorsLimit(t *testing.T) {
	a, b := snapshot(), snapshot()
	b.ModelServers[0].Models[1].Resident = true // models loading during a run is expected
	if got := Drift(a, b, 10); len(got) != 0 {
		t.Fatalf("residency is not drift, got %v", got)
	}
	b.ModelServers[0].EngineVersion = "0.10"
	b.OperatorVersion = "9"
	b.ConcurrentRuns = 1
	if got := Drift(a, b, 0); len(got) < 4 {
		t.Fatalf("expected server, version and concurrent changes, got %v", got)
	}
	if got := Drift(a, b, 2); len(got) != 2 {
		t.Fatalf("limit not applied: %v", got)
	}
}

func TestResourceNotes(t *testing.T) {
	agents := []kubemootv1alpha1.RealmAgentContainer{
		{Container: "ok", CPURequest: "500m", CPULimit: "2", MemoryRequest: oneGi, MemoryLimit: twoGi},
		{Container: "pinned", CPURequest: "1", CPULimit: "1", MemoryRequest: oneGi, MemoryLimit: twoGi},
		{Container: "bare"},
		{Container: "no-limit", CPURequest: "1", MemoryRequest: oneGi},
	}
	got := ResourceNotes(agents)
	want := []string{
		"bare: cpu request unset, so no guaranteed allocation",
		"bare: memory request unset, so no guaranteed allocation",
		"pinned: cpu limit equals the request 1, so no headroom above the guaranteed allocation",
	}
	if !slices.Equal(got, want) {
		t.Errorf("notes = %v, want %v", got, want)
	}
	if len(ResourceNotes(nil)) != 0 {
		t.Error("no containers, no notes")
	}
}
