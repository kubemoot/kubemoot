package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine/ollama"
)

// Two Models naming one model: a disabled one speaks for it whatever the list order,
// so a duplicate never re-admits a model its owner disabled; otherwise the older wins.
func TestCatalogModelsPicksDeterministicallyAmongDuplicates(t *testing.T) {
	p := catServerObj(aiv1alpha1.ServerTypeOllama)
	old := catModel("a-old", candModel14, func(m *aiv1alpha1.Model) { m.CreationTimestamp = metav1.NewTime(time.Unix(100, 0)) })
	newer := catModel("b-new", candModel14, func(m *aiv1alpha1.Model) { m.CreationTimestamp = metav1.NewTime(time.Unix(200, 0)) })
	off := catModel("c-off", candModel14, func(m *aiv1alpha1.Model) {
		m.Spec.Disabled = true
		m.CreationTimestamp = metav1.NewTime(time.Unix(300, 0))
	})
	for _, order := range [][]aiv1alpha1.Model{{*old, *newer, *off}, {*off, *newer, *old}, {*newer, *off, *old}} {
		if got := catalogModels(p, order)[candModel14]; got.Name != "c-off" {
			t.Errorf("chosen %q, want the disabled Model whatever the order", got.Name)
		}
	}
	for _, order := range [][]aiv1alpha1.Model{{*old, *newer}, {*newer, *old}} {
		if got := catalogModels(p, order)[candModel14]; got.Name != "a-old" {
			t.Errorf("chosen %q, want the older Model", got.Name)
		}
	}
	same := metav1.NewTime(time.Unix(100, 0))
	a := catModel("a", "x", func(m *aiv1alpha1.Model) { m.CreationTimestamp = same })
	b := catModel("b", "x", func(m *aiv1alpha1.Model) { m.CreationTimestamp = same })
	if got := catalogModels(p, []aiv1alpha1.Model{*b, *a})["x"]; got.Name != "a" {
		t.Errorf("chosen %q, want the lower name on a tie", got.Name)
	}
}

// A kubemoot.ai/size label that is not a class is ignored; the derived class stands.
func TestBuildCatalogIgnoresAnInvalidSizeOverride(t *testing.T) {
	facts := []catalogFacts{{Name: "m", Present: true, ParameterCount: 8_000_000_000}}
	m := catModel("m", "m", func(m *aiv1alpha1.Model) { m.Labels = map[string]string{aiv1alpha1.LabelSize: "huge"} })
	e := catEntry(t, buildCatalog(catServerObj(aiv1alpha1.ServerTypeOllama), facts, map[string]*aiv1alpha1.Model{"m": m}), "m")
	if e.Size != aiv1alpha1.SizeSmall || e.Labels[aiv1alpha1.LabelSize] != aiv1alpha1.SizeSmall {
		t.Errorf("entry = %+v, want the derived small", e)
	}
}

// The show cache keeps what is live, drops a model the server no longer lists, and
// never caches an answer for a model without a digest.
func TestShowCachePrunesAndSkipsEmptyDigests(t *testing.T) {
	var shows atomic.Int32
	srv := catalogOllama(t, &shows)
	r := &ModelServerReconciler{}
	r.shows.Store(srv.URL+"|removed:1b|old", struct{}{})
	r.shows.Store("http://other|keep:1b|d", struct{}{})
	if _, err := r.ollamaFacts(context.Background(), srv.Client(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.shows.Load(srv.URL + "|removed:1b|old"); ok {
		t.Error("a model the server no longer lists must be forgotten")
	}
	if _, ok := r.shows.Load("http://other|keep:1b|d"); !ok {
		t.Error("another endpoint's cache must be left alone")
	}
	if _, ok := r.shows.Load(srv.URL + "|" + candModel14 + "|d14"); !ok {
		t.Error("a live model must stay cached")
	}
	r2 := &ModelServerReconciler{}
	info, ok := r2.showModel(context.Background(), srv.Client(), srv.URL, ollama.ModelInfo{Name: candModel14})
	if !ok || info.ParameterCount == 0 {
		t.Fatalf("show = %+v %v", info, ok)
	}
	if _, cached := r2.shows.Load(srv.URL + "|" + candModel14 + "|"); cached {
		t.Error("an answer for a model without a digest must not be cached")
	}
}

// A server's context window is read once per endpoint, however many names it serves.
func TestServedContextsAsksEachEndpointOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"id":"a","max_model_len":4096},{"id":"alias","max_model_len":4096}]}`))
	}))
	defer srv.Close()
	got := servedContexts(context.Background(), srv.Client(), []aiv1alpha1.VLLMServer{
		{Model: "a", Endpoint: srv.URL}, {Model: "alias", Endpoint: srv.URL},
	})
	if calls.Load() != 1 || got["a"] != 4096 || got["alias"] != 4096 {
		t.Errorf("calls=%d contexts=%v", calls.Load(), got)
	}
}

// A Model that already holds the finalizer and then becomes disabled reports
// Disabled and keeps the finalizer, so deleting it still releases the model file
// as before.
func TestModelBecomingDisabledKeepsItsFinalizer(t *testing.T) {
	scheme := modelTestScheme(t)
	p := catServerObj(aiv1alpha1.ServerTypeOllama)
	p.Status.Ready = true
	m := catModel("m", "qwen3:8b", func(m *aiv1alpha1.Model) {
		m.Spec.Disabled = true
		m.Finalizers = []string{modelFinalizer}
		m.Status = aiv1alpha1.ModelStatus{State: stateAvailable, Ready: true}
	})
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p, m).WithStatusSubresource(p, m).Build()
	r := &ModelReconciler{Client: cli}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: m.Name, Namespace: m.Namespace}}); err != nil {
		t.Fatal(err)
	}
	got := &aiv1alpha1.Model{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: m.Name, Namespace: m.Namespace}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.State != stateDisabled || got.Status.Ready || len(got.Finalizers) != 1 {
		t.Errorf("state=%q ready=%v finalizers=%v", got.Status.State, got.Status.Ready, got.Finalizers)
	}
}

// Calibration measures a crew's own Model (any namespace) but not one being deleted;
// the catalog lists only the server's namespace.
func TestBoundModelsKeepsCrewLocalModelsThatTheCatalogLeavesOut(t *testing.T) {
	p := catServerObj(aiv1alpha1.ServerTypeOllama)
	crew := catModel("crew", "crew-model", func(m *aiv1alpha1.Model) { m.Namespace = testPlacementNS })
	now := metav1.Now()
	going := catModel("going", "going-model", func(m *aiv1alpha1.Model) { m.DeletionTimestamp = &now; m.Finalizers = []string{"x"} })
	cli := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(crew, going).Build()
	r := &ModelServerReconciler{Client: cli}
	got, err := r.boundModels(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "crew-model" {
		t.Errorf("bound = %v", got)
	}
	if cat := catalogModels(p, []aiv1alpha1.Model{*crew}); len(cat) != 0 {
		t.Errorf("a crew's Model must not be in the catalog: %v", cat)
	}
}
