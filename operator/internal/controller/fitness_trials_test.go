package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

type fakePurger struct {
	prefixes []string
	err      error
}

func (p *fakePurger) PurgeKVPrefix(_, prefix string) (int, error) {
	p.prefixes = append(p.prefixes, prefix)
	return 1, p.err
}

func trialSuite(iterations int32) *kubemootv1alpha1.CrewFitnessSuite {
	return &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: testBaseline, Namespace: testCrewTest},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			CrewRef: testCrewName, Iterations: iterations, Scripts: scripts("a"),
		},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: testDeadbeef},
	}
}

func trialReconciler(t *testing.T, p memoryPurger) (*CrewFitnessSuiteReconciler, client.Client) {
	t.Helper()
	scheme := reapScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	return &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme, memory: p}, cli
}

func TestTrialIsolationDefaultsToPerTrial(t *testing.T) {
	suite := trialSuite(2)
	if trialIsolation(suite) != kubemootv1alpha1.FitnessTrialIsolationPerTrial || !purgesPerTrial(suite) {
		t.Fatal("an unset trialIsolation must isolate each trial")
	}
}

func TestPurgesPerTrialMatrix(t *testing.T) {
	cases := []struct {
		name  string
		purge *bool
		iso   kubemootv1alpha1.FitnessTrialIsolation
		want  bool
	}{
		{"no fields set", nil, "", true},
		{"explicit per trial", ptr.To(true), kubemootv1alpha1.FitnessTrialIsolationPerTrial, true},
		{"per suite", nil, kubemootv1alpha1.FitnessTrialIsolationPerSuite, false},
		{"purge off", ptr.To(false), kubemootv1alpha1.FitnessTrialIsolationPerTrial, false},
	}
	for _, c := range cases {
		suite := trialSuite(2)
		suite.Spec.PurgeMemory, suite.Spec.TrialIsolation = c.purge, c.iso
		if got := purgesPerTrial(suite); got != c.want {
			t.Errorf("%s: purgesPerTrial = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEffectiveConcurrency(t *testing.T) {
	suite := trialSuite(2)
	suite.Spec.Concurrency = 4
	if got := effectiveConcurrency(suite); got != 1 {
		t.Errorf("isolated trials run one at a time, got %d", got)
	}
	suite.Spec.TrialIsolation = kubemootv1alpha1.FitnessTrialIsolationPerSuite
	if got := effectiveConcurrency(suite); got != 4 {
		t.Errorf("shared-memory suite keeps its concurrency, got %d", got)
	}
	suite.Spec.Concurrency = 0
	if got := effectiveConcurrency(suite); got != 1 {
		t.Errorf("unset concurrency is 1, got %d", got)
	}
}

func TestScheduleBatchPurgesBeforeEachTrialAndRunsOne(t *testing.T) {
	p := &fakePurger{}
	r, cli := trialReconciler(t, p)
	suite := trialSuite(3)
	suite.Spec.Concurrency = 3

	if err := r.scheduleBatch(context.Background(), suite, nil, 0); err != nil {
		t.Fatalf("scheduleBatch: %v", err)
	}
	var list kubemootv1alpha1.CrewFitnessList
	if err := cli.List(context.Background(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("one trial at a time: got %d children (err %v)", len(list.Items), err)
	}
	if len(p.prefixes) != 1 || !strings.Contains(p.prefixes[0], testCrewName) {
		t.Fatalf("expected one purge of the crew's memory before the trial, got %v", p.prefixes)
	}
	// A trial in flight blocks the next one, and no purge happens meanwhile.
	if err := r.scheduleBatch(context.Background(), suite, list.Items, 1); err != nil {
		t.Fatalf("scheduleBatch: %v", err)
	}
	if len(p.prefixes) != 1 {
		t.Errorf("must not purge while a trial is in flight, purges: %v", p.prefixes)
	}
	// Next trial after the first finished: purge again.
	done := list.Items
	done[0].Status.Phase = kubemootv1alpha1.CrewFitnessPhasePassed
	if err := r.scheduleBatch(context.Background(), suite, done, 0); err != nil {
		t.Fatalf("scheduleBatch: %v", err)
	}
	if len(p.prefixes) != 2 {
		t.Errorf("second trial must follow a second purge, got %d", len(p.prefixes))
	}
}

func TestScheduleBatchDoesNotCreateTrialWhenPurgeFails(t *testing.T) {
	p := &fakePurger{err: errors.New("bus down")}
	r, cli := trialReconciler(t, p)
	err := r.scheduleBatch(context.Background(), trialSuite(2), nil, 0)
	if err == nil || !strings.Contains(err.Error(), "bus down") {
		t.Fatalf("want the purge error, got %v", err)
	}
	var list kubemootv1alpha1.CrewFitnessList
	_ = cli.List(context.Background(), &list)
	if len(list.Items) != 0 {
		t.Fatalf("a trial must not start on unpurged memory, got %d", len(list.Items))
	}
}

func TestScheduleBatchPerSuiteNeverPurges(t *testing.T) {
	p := &fakePurger{}
	r, cli := trialReconciler(t, p)
	suite := trialSuite(3)
	suite.Spec.TrialIsolation = kubemootv1alpha1.FitnessTrialIsolationPerSuite
	suite.Spec.Concurrency = 2
	if err := r.scheduleBatch(context.Background(), suite, nil, 0); err != nil {
		t.Fatal(err)
	}
	var list kubemootv1alpha1.CrewFitnessList
	_ = cli.List(context.Background(), &list)
	if len(list.Items) != 2 || len(p.prefixes) != 0 {
		t.Errorf("PerSuite: want 2 trials and no purge, got %d trials, %v", len(list.Items), p.prefixes)
	}
}

func TestPurgeBeforeTrialWithoutBusIsSkipped(t *testing.T) {
	r, _ := trialReconciler(t, nil)
	if err := r.purgeBeforeTrial(trialSuite(2)); err != nil {
		t.Fatalf("no bus: the condition reports it, the suite still runs: %v", err)
	}
}

func TestTrialsIndependentCondition(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*kubemootv1alpha1.CrewFitnessSuite)
		purger memoryPurger
		status metav1.ConditionStatus
		reason string
	}{
		{"no fields set", func(*kubemootv1alpha1.CrewFitnessSuite) {}, &fakePurger{}, metav1.ConditionTrue, reasonPerTrialPurge},
		{"per suite", func(s *kubemootv1alpha1.CrewFitnessSuite) {
			s.Spec.TrialIsolation = kubemootv1alpha1.FitnessTrialIsolationPerSuite
		}, &fakePurger{}, metav1.ConditionFalse, reasonSharedMemory},
		{"purge off", func(s *kubemootv1alpha1.CrewFitnessSuite) { s.Spec.PurgeMemory = ptr.To(false) },
			&fakePurger{}, metav1.ConditionFalse, reasonPurgeDisabled},
		{"no bus", func(*kubemootv1alpha1.CrewFitnessSuite) {}, nil, metav1.ConditionFalse, reasonNoMemoryStore},
	}
	for _, c := range cases {
		r := &CrewFitnessSuiteReconciler{memory: c.purger}
		suite := trialSuite(2)
		c.mutate(suite)
		r.recordTrialIsolation(suite)
		got := meta.FindStatusCondition(suite.Status.Conditions, kubemootv1alpha1.ConditionTrialsIndependent)
		if got == nil || got.Status != c.status || got.Reason != c.reason {
			t.Errorf("%s: got %+v, want %s/%s", c.name, got, c.status, c.reason)
		}
	}
}

func TestTrialsIndependentConditionSaysConcurrencyIgnored(t *testing.T) {
	r := &CrewFitnessSuiteReconciler{memory: &fakePurger{}}
	suite := trialSuite(2)
	suite.Spec.Concurrency = 3
	got := r.trialsIndependentCondition(suite)
	if !strings.Contains(got.Message, "concurrency 3 does not apply") {
		t.Errorf("message must say the concurrency is not used: %q", got.Message)
	}
}

// staleCacheClient lists no CrewFitness from the cache, as an informer that has not
// yet seen a child created on the previous tick.
func staleCacheClient(cli client.WithWatch) client.Client {
	return interceptor.NewClient(cli, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*kubemootv1alpha1.CrewFitnessList); ok {
				return nil
			}
			return c.List(ctx, list, opts...)
		},
	})
}

func TestAdvanceRunningDoesNotPurgeWhileATrialRunsUnseenByTheCache(t *testing.T) {
	p := &fakePurger{}
	scheme := reapScheme(t)
	suite := trialSuite(3)
	suite.Spec.Concurrency = 1
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseRunning
	suite.Status.IterationsTotal = 3
	live := childCR(iterationCRName(testDeadbeef, 0, 1), testBaseline)
	live.Labels[suiteIterationIndexLabel] = "1"
	live.Labels[suiteScriptIndexLabel] = "0"
	live.Status.Phase = kubemootv1alpha1.CrewFitnessPhaseRunning
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(suite, live).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitnessSuite{}).Build()
	r := &CrewFitnessSuiteReconciler{Client: staleCacheClient(base), APIReader: base, Scheme: scheme, memory: p}

	if _, err := r.advanceRunning(context.Background(), suite); err != nil {
		t.Fatalf("advanceRunning: %v", err)
	}
	if len(p.prefixes) != 0 {
		t.Fatalf("crew memory was cleared while trial 1 was running: %v", p.prefixes)
	}
	var list kubemootv1alpha1.CrewFitnessList
	_ = base.List(context.Background(), &list)
	if len(list.Items) != 1 {
		t.Fatalf("no second trial may start while one is in flight, got %d", len(list.Items))
	}
}

func TestAdvanceRunningRecordsAFailedClearOnTheCondition(t *testing.T) {
	p := &fakePurger{err: errors.New("bus down")}
	scheme := reapScheme(t)
	suite := trialSuite(2)
	suite.Status.Phase = kubemootv1alpha1.CrewFitnessSuitePhaseRunning
	suite.Status.IterationsTotal = 2
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(suite).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitnessSuite{}).Build()
	r := &CrewFitnessSuiteReconciler{Client: base, APIReader: base, Scheme: scheme, memory: p}

	if _, err := r.advanceRunning(context.Background(), suite); err == nil {
		t.Fatal("a failed clear must be retried")
	}
	got := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(suite), got); err != nil {
		t.Fatal(err)
	}
	c := meta.FindStatusCondition(got.Status.Conditions, kubemootv1alpha1.ConditionTrialsIndependent)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != reasonPurgeFailed || !strings.Contains(c.Message, "bus down") {
		t.Fatalf("condition = %+v", c)
	}

	p.err = nil // the bus recovers
	if _, err := r.advanceRunning(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	c = meta.FindStatusCondition(got.Status.Conditions, kubemootv1alpha1.ConditionTrialsIndependent)
	if c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("the condition must recover, got %+v", c)
	}
}
