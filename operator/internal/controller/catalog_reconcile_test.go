package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// An Ollama provider publishes its catalog on reconcile: the models Ollama has,
// sized from /api/show, merged with the Model object next to the provider, with the
// embedding model left out and a disabled model removed.
func TestModelProviderReconcilePublishesTheCatalog(t *testing.T) {
	var shows atomic.Int32
	inner := catalogOllama(t, &shows)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_, _ = w.Write([]byte(`{"version":"0.40.5"}`))
			return
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	scheme := refScheme(t)
	p := catProviderObj(aiv1alpha1.ProviderTypeOllama)
	p.Spec.Endpoint = srv.URL
	off := catModel("o-gemma", "gemma3:4b", func(m *aiv1alpha1.Model) { m.Spec.Disabled = true })
	extra := catModel("o-qwen", candModel14, func(m *aiv1alpha1.Model) { m.Labels = map[string]string{"kubemoot.ai/license": "apache-2.0"} })
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p, off, extra).WithStatusSubresource(p, off, extra).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache(), HTTPClient: srv.Client()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace}}); err != nil {
		t.Fatal(err)
	}
	got := &aiv1alpha1.ModelProvider{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: p.Name, Namespace: p.Namespace}, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Models) != 1 {
		t.Fatalf("catalog = %+v, want only qwen3:14b", got.Status.Models)
	}
	e := got.Status.Models[0]
	if e.Name != candModel14 || e.Size != candMedium || e.Labels["kubemoot.ai/license"] != "apache-2.0" || e.ModelRef != "kubemoot/o-qwen" {
		t.Errorf("entry = %+v", e)
	}
}

// A disabled Model reports Disabled, is not Ready, and starts no pull.
func TestModelReconcileDisabledIsNotReadyAndPullsNothing(t *testing.T) {
	var pulls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testPathAPIPull {
			pulls.Add(1)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	scheme := modelTestScheme(t)
	p := catProviderObj(aiv1alpha1.ProviderTypeOllama)
	p.Spec.Endpoint = srv.URL
	p.Status.Ready = true
	m := catModel("off", "qwen3:8b", func(m *aiv1alpha1.Model) { m.Spec.Disabled = true })
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p, m).WithStatusSubresource(p, m).Build()
	r := &ModelReconciler{Client: cli, HTTPClient: srv.Client()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: m.Name, Namespace: m.Namespace}}); err != nil {
		t.Fatal(err)
	}
	got := &aiv1alpha1.Model{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: m.Name, Namespace: m.Namespace}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.State != stateDisabled || got.Status.Ready || len(got.Finalizers) != 0 || pulls.Load() != 0 {
		t.Errorf("state=%q ready=%v finalizers=%v pulls=%d", got.Status.State, got.Status.Ready, got.Finalizers, pulls.Load())
	}
}

// A disabled Model does not count as wanting its tag, so it never keeps a pull or a
// downloaded file alive.
func TestWantedPullKeysSkipsDisabledModels(t *testing.T) {
	on := catModel("on", "a", nil)
	off := catModel("off", "b", func(m *aiv1alpha1.Model) { m.Spec.Disabled = true })
	cli := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(on, off).Build()
	wanted, err := wantedPullKeys(context.Background(), cli, &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "self", Namespace: catNS}})
	if err != nil {
		t.Fatal(err)
	}
	if !wanted[pullKey(catProvider, "a")] || wanted[pullKey(catProvider, "b")] {
		t.Errorf("wanted = %v", wanted)
	}
}

// Calibration measures the catalog's present models as well as the Models bound by
// an object, and not a disabled Model.
func TestBoundModelsIncludesTheCatalogAndSkipsDisabled(t *testing.T) {
	p := catProviderObj(aiv1alpha1.ProviderTypeOllama)
	p.Status.Models = []aiv1alpha1.CatalogModel{{Name: "found", Present: true}, {Name: "bound", Present: true}}
	bound := catModel("bound", "bound", nil)
	off := catModel("off", "off", func(m *aiv1alpha1.Model) { m.Spec.Disabled = true })
	cli := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(bound, off).Build()
	r := &ModelProviderReconciler{Client: cli}
	got, err := r.boundModels(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "bound" || got[1] != "found" {
		t.Errorf("bound models = %v", got)
	}
}
