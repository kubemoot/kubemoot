package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	tierTestNS    = "crew-demo"
	tierTestCrew  = "demo"
	tierReadme    = "README.md"
	tierFirst     = "a-first"
	tierSecond    = "b-second"
	tierFirstFile = "a.adl"
	tierSmokeText = "DEFINE CONST TIERS AS \"smoke\"\n"
	tierSmokeWord = "smoke"
	tierThird     = "c-third"
)

// tierScenarios is a crew's scenario ConfigMap data: two standard, one of them also
// smoke, one smoke only, and one in neither tier.
func tierScenarios() map[string]string {
	return map[string]string{
		tierFirst + ".adl":  "DESCRIPTION a\nDEFINE CONST TIERS AS \"smoke standard\"\nDEFINE CONST QUESTION AS \"q\"\n",
		tierSecond + ".adl": "DESCRIPTION b\nDEFINE CONST TIERS AS \"standard\"\n",
		tierThird + ".adl":  "DESCRIPTION c\n" + tierSmokeText,
		"d-untagged.adl":    "DESCRIPTION d\nDEFINE CONST QUESTION AS \"q\"\n",
		tierReadme:          "# not a scenario\n",
		"notes.txt":         "not a scenario",
	}
}

func refsOf(scripts []kubemootv1alpha1.SuiteScript) []string {
	out := make([]string, 0, len(scripts))
	for _, s := range scripts {
		out = append(out, s.TestRef)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSelectTierPicksLabelledScenariosInNameOrder(t *testing.T) {
	cases := []struct {
		tier kubemootv1alpha1.FitnessTier
		want []string
	}{
		{kubemootv1alpha1.FitnessTierSmoke, []string{tierFirst, tierThird}},
		{kubemootv1alpha1.FitnessTierStandard, []string{tierFirst, tierSecond}},
		{kubemootv1alpha1.FitnessTierFull, []string{tierFirst, tierSecond, tierThird, "d-untagged"}},
	}
	for _, c := range cases {
		sel, err := selectTier(c.tier, tierScenarios())
		if err != nil {
			t.Fatalf("%s: %v", c.tier, err)
		}
		if got := refsOf(sel.scripts); !sameStrings(got, c.want) {
			t.Errorf("%s: scripts = %v, want %v", c.tier, got, c.want)
		}
		if sel.available != 4 {
			t.Errorf("%s: available = %d, want 4 (README.md and notes.txt are not scenarios)", c.tier, sel.available)
		}
		for _, s := range sel.scripts {
			if s.TestContent == "" || s.ConfigMapRef != "" {
				t.Errorf("%s: %s must carry its text inline", c.tier, s.TestRef)
			}
		}
	}
}

func TestSelectTierFullTakesEveryScenarioWhateverItsLabel(t *testing.T) {
	data := map[string]string{
		"x.adl": "DEFINE CONST TIERS AS \"nonsense\"\n",
		"y.md":  "# y\n\nWhat is up?\n",
	}
	sel, err := selectTier(kubemootv1alpha1.FitnessTierFull, data)
	if err != nil {
		t.Fatalf("full must ignore labels: %v", err)
	}
	if got := refsOf(sel.scripts); !sameStrings(got, []string{"x", "y"}) {
		t.Errorf("scripts = %v, want [x y]", got)
	}
}

func TestSelectTierRefusesWhatItCannotResolve(t *testing.T) {
	cases := []struct {
		name string
		tier kubemootv1alpha1.FitnessTier
		data map[string]string
		want string
	}{
		{"no scenarios", kubemootv1alpha1.FitnessTierFull, map[string]string{tierReadme: "x"}, "no scenario"},
		{"nil data", kubemootv1alpha1.FitnessTierSmoke, nil, "no scenario"},
		{"nothing carries the tier", kubemootv1alpha1.FitnessTierSmoke,
			map[string]string{tierFirstFile: "DEFINE CONST TIERS AS \"standard\"\n"}, "no scenario carries tier \"smoke\""},
		{"untagged scenarios", kubemootv1alpha1.FitnessTierStandard,
			map[string]string{tierFirstFile: "DESCRIPTION a\n"}, "no scenario carries tier"},
		{"label typo", kubemootv1alpha1.FitnessTierSmoke,
			map[string]string{tierFirstFile: "DEFINE CONST TIERS AS \"smok\"\n", "b.adl": tierSmokeText},
			"unknown tier \"smok\""},
		{"full is not a label", kubemootv1alpha1.FitnessTierStandard,
			map[string]string{tierFirstFile: "DEFINE CONST TIERS AS \"full\"\n"}, "unknown tier \"full\""},
		{"unknown tier name", kubemootv1alpha1.FitnessTier("weekly"),
			map[string]string{tierFirstFile: tierSmokeText}, "no scenario carries tier \"weekly\""},
		{"empty tier name", kubemootv1alpha1.FitnessTier(""),
			map[string]string{tierFirstFile: tierSmokeText}, "no scenario carries tier \"\""},
	}
	for _, c := range cases {
		_, err := selectTier(c.tier, c.data)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.want)
		}
	}
}

func TestScenarioTiersReadsSpacesAndCommasInAnyCase(t *testing.T) {
	got := scenarioTiers("DEFINE CONST TIERS AS \"Smoke, standard  SMOKE\"\n")
	if !sameStrings(got, []string{tierSmokeWord, "standard", tierSmokeWord}) {
		t.Errorf("tiers = %v", got)
	}
	if got := scenarioTiers("DESCRIPTION none\n"); len(got) != 0 {
		t.Errorf("a scenario with no TIERS = %v, want none", got)
	}
}

func TestScenarioTestRefOnlyAcceptsScenarioKeys(t *testing.T) {
	for key, want := range map[string]string{tierFirstFile: "a", "b-c.md": "b-c"} {
		if got, ok := scenarioTestRef(key); !ok || got != want {
			t.Errorf("%s = %q %v, want %q", key, got, ok, want)
		}
	}
	for _, key := range []string{"README.md", "readme.md", ".adl", "x.txt", "x", "x.adl.bak"} {
		if got, ok := scenarioTestRef(key); ok {
			t.Errorf("%q must not be a scenario, got %q", key, got)
		}
	}
}

func tierSuite(tier kubemootv1alpha1.FitnessTier, scripts ...kubemootv1alpha1.SuiteScript) *kubemootv1alpha1.CrewFitnessSuite {
	return &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: tierTestNS},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			CrewRef: tierTestCrew, Iterations: 2, Tier: tier, Scripts: scripts, SkipToolSmoke: true,
		},
	}
}

func tierReconciler(t *testing.T, suite *kubemootv1alpha1.CrewFitnessSuite, objs ...client.Object) *CrewFitnessSuiteReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, suite)...).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitnessSuite{}).Build()
	return &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme}
}

func scenarioConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: tierTestCrew + "-fitness", Namespace: tierTestNS},
		Data:       tierScenarios(),
	}
}

func reload(t *testing.T, r *CrewFitnessSuiteReconciler) *kubemootv1alpha1.CrewFitnessSuite {
	t.Helper()
	got := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: tierTestNS, Name: "s"}, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestStartSuiteResolvesTierIntoScriptsAndStatus(t *testing.T) {
	suite := tierSuite(kubemootv1alpha1.FitnessTierSmoke)
	r := tierReconciler(t, suite, scenarioConfigMap())

	if _, err := r.startSuite(context.Background(), suite); err != nil {
		t.Fatalf("startSuite: %v", err)
	}
	got := reload(t, r)
	if got.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseRunning {
		t.Fatalf("phase = %q (error %q), want Running", got.Status.Phase, got.Status.Error)
	}
	if refs := refsOf(got.Spec.Scripts); !sameStrings(refs, []string{tierFirst, tierThird}) {
		t.Errorf("spec.scripts = %v, want [a-first c-third]", refs)
	}
	tier := got.Status.Tier
	if tier == nil || tier.Name != kubemootv1alpha1.FitnessTierSmoke || tier.Source != "demo-fitness" ||
		tier.Available != 4 || !sameStrings(tier.Scenarios, []string{tierFirst, tierThird}) {
		t.Errorf("status.tier = %+v", tier)
	}
	if got.Status.IterationsTotal != 4 {
		t.Errorf("iterationsTotal = %d, want 2 scenarios x 2 iterations", got.Status.IterationsTotal)
	}
}

func TestStartSuiteTierErrorsEndTheSuiteWithAReason(t *testing.T) {
	cases := []struct {
		name  string
		suite *kubemootv1alpha1.CrewFitnessSuite
		objs  []client.Object
		want  string
	}{
		{"no ConfigMap", tierSuite(kubemootv1alpha1.FitnessTierSmoke), nil, "ConfigMap demo-fitness not found"},
		{"scripts and tier together", tierSuite(kubemootv1alpha1.FitnessTierSmoke,
			kubemootv1alpha1.SuiteScript{TestRef: "x", TestContent: "y"}), []client.Object{scenarioConfigMap()}, "remove spec.scripts or spec.tier"},
		{"unknown tier", tierSuite(kubemootv1alpha1.FitnessTier("weekly")), []client.Object{scenarioConfigMap()}, "no scenario carries tier \"weekly\""},
	}
	for _, c := range cases {
		r := tierReconciler(t, c.suite, c.objs...)
		if _, err := r.startSuite(context.Background(), c.suite); err != nil {
			t.Fatalf("%s: startSuite: %v", c.name, err)
		}
		got := reload(t, r)
		if got.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseError || !strings.Contains(got.Status.Error, c.want) {
			t.Errorf("%s: phase %q error %q, want Error containing %q", c.name, got.Status.Phase, got.Status.Error, c.want)
		}
		if got.Status.Tier != nil && c.name != "scripts and tier together" {
			t.Errorf("%s: status.tier must stay empty when nothing resolved: %+v", c.name, got.Status.Tier)
		}
	}
}

func TestResolvedTierSurvivesAChangeToTheCrewScenarios(t *testing.T) {
	suite := tierSuite(kubemootv1alpha1.FitnessTierStandard)
	cm := scenarioConfigMap()
	r := tierReconciler(t, suite, cm)
	if _, stop, err := r.resolveSuiteTier(context.Background(), suite); stop || err != nil {
		t.Fatalf("first resolve: stop=%v err=%v", stop, err)
	}
	// The crew chart is upgraded and drops every scenario; the suite keeps its text.
	cm.Data = map[string]string{"z.adl": "DEFINE CONST TIERS AS \"standard\"\n"}
	if err := r.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	got := reload(t, r)
	if _, stop, err := r.resolveSuiteTier(context.Background(), got); stop || err != nil {
		t.Fatalf("second resolve: stop=%v err=%v", stop, err)
	}
	if refs := refsOf(reload(t, r).Spec.Scripts); !sameStrings(refs, []string{tierFirst, tierSecond}) {
		t.Errorf("spec.scripts = %v, want the scenarios resolved at the start", refs)
	}
}

func TestResolveTierRecoversWhenOnlyTheStatusWasWritten(t *testing.T) {
	suite := tierSuite(kubemootv1alpha1.FitnessTierSmoke)
	suite.Status.Tier = &kubemootv1alpha1.SuiteTierStatus{Name: kubemootv1alpha1.FitnessTierSmoke, Source: "demo-fitness"}
	r := tierReconciler(t, suite, scenarioConfigMap())
	// status.tier is set and spec.scripts is empty: a crash between the two writes.
	if _, stop, err := r.resolveSuiteTier(context.Background(), suite); stop || err != nil {
		t.Fatalf("resolve: stop=%v err=%v", stop, err)
	}
	if refs := refsOf(reload(t, r).Spec.Scripts); len(refs) != 2 {
		t.Errorf("spec.scripts = %v, want the two smoke scenarios", refs)
	}
}

func TestSelectTierRefusesMoreScenariosThanASuiteCanList(t *testing.T) {
	data := map[string]string{}
	for i := 0; i <= kubemootv1alpha1.MaxStatusScenarios; i++ {
		data[fmt.Sprintf("s%04d.adl", i)] = "DESCRIPTION x\n"
	}
	if _, err := selectTier(kubemootv1alpha1.FitnessTierFull, data); err == nil || !strings.Contains(err.Error(), "at most 300") {
		t.Errorf("err = %v, want a refusal naming the cap", err)
	}
}

func TestTierStatusCutsTheListAtTheStatusCap(t *testing.T) {
	sel := tierSelection{available: 400}
	for i := 0; i < 400; i++ {
		sel.scripts = append(sel.scripts, kubemootv1alpha1.SuiteScript{TestRef: fmt.Sprintf("s%d", i)})
	}
	st := tierStatus(kubemootv1alpha1.FitnessTierFull, "c", sel)
	if len(st.Scenarios) != kubemootv1alpha1.MaxStatusScenarios || st.Available != 400 {
		t.Errorf("scenarios %d available %d", len(st.Scenarios), st.Available)
	}
}

func TestConfigMapReaderPrefersTheAPIReader(t *testing.T) {
	cached := &CrewFitnessSuiteReconciler{}
	live := fake.NewClientBuilder().Build()
	if cached.configMapReader() != client.Reader(cached.Client) {
		t.Error("without an APIReader the cached client is used")
	}
	cached.APIReader = live
	if cached.configMapReader() != client.Reader(live) {
		t.Error("an APIReader is used when set")
	}
}

type failingGet struct{ client.Reader }

func (failingGet) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("api down")
}

func TestResolveTierReturnsAReadErrorForARetryAndEndsNothing(t *testing.T) {
	suite := tierSuite(kubemootv1alpha1.FitnessTierSmoke)
	r := tierReconciler(t, suite)
	r.APIReader = failingGet{}
	if _, stop, err := r.resolveSuiteTier(context.Background(), suite); !stop || err == nil {
		t.Fatalf("stop=%v err=%v, want a stop with the error", stop, err)
	}
	if got := reload(t, r); got.Status.Phase == kubemootv1alpha1.CrewFitnessSuitePhaseError || got.Status.Tier != nil {
		t.Errorf("a transient read failure must not end the suite: %+v", got.Status)
	}
}

func TestResolveTierIsANoopWithoutATier(t *testing.T) {
	suite := tierSuite("")
	r := tierReconciler(t, suite)
	if _, stop, err := r.resolveSuiteTier(context.Background(), suite); stop || err != nil || suite.Status.Tier != nil {
		t.Errorf("stop=%v err=%v tier=%v", stop, err, suite.Status.Tier)
	}
}
