/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	embedTestName   = "nomic"
	embedSecond     = "b-second"
	embedPinned     = "z-pinned"
	embedPinnedName = "pinned"
	embedTestModel  = "nomic-embed-text"
	embedTestDims   = 768
)

// embedOllama is an Ollama server that holds the embedding model once present is
// true, and counts the downloads asked of it.
type embedOllama struct {
	srv     *httptest.Server
	present atomic.Bool
	pulls   atomic.Int32
}

func newEmbedOllama(t *testing.T, present bool) *embedOllama {
	t.Helper()
	o := &embedOllama{}
	o.present.Store(present)
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testPathAPITags:
			if o.present.Load() {
				_, _ = w.Write([]byte(`{"models":[{"name":"` + embedTestModel + `:latest","digest":"sha256:e1"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/show":
			_, _ = w.Write([]byte(`{"details":{"family":"nomic-bert"},"model_info":{"general.architecture":"nomic-bert","nomic-bert.embedding_length":768}}`))
		case testPathAPIPull:
			o.pulls.Add(1)
			o.present.Store(true)
			emit(w, `{"status":"success"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(o.srv.Close)
	return o
}

// embedServer is a ready Ollama ModelServer whose catalog lists the embedding model
// when listed is true.
func embedServer(name, endpoint string, listed bool) *aiv1alpha1.ModelServer {
	s := &aiv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testKubemoot},
		Spec:       aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeOllama, Endpoint: endpoint},
		Status:     aiv1alpha1.ModelServerStatus{Ready: true},
	}
	if listed {
		s.Status.EmbeddingModels = []aiv1alpha1.EmbeddingCatalogModel{{Name: embedTestModel + ":latest", Dimensions: embedTestDims}}
	}
	return s
}

// embedModelFor is a catalog Model that asks for the embedding model on a server.
func embedModelFor(server string) *aiv1alpha1.Model {
	return &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: server + "-embed", Namespace: testKubemoot},
		Spec:       aiv1alpha1.ModelSpec{Model: embedTestModel, ServerRef: server},
	}
}

// unboundEmbedding is an EmbeddingModel that names the model and no server.
func unboundEmbedding() *aiv1alpha1.EmbeddingModel {
	return &aiv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: embedTestName, Namespace: testCrewNamespace, UID: "emb-bind", Finalizers: []string{embeddingModelFinalizer}},
		Spec:       aiv1alpha1.EmbeddingModelSpec{Model: embedTestModel, Dimensions: embedTestDims},
	}
}

type bindHarness struct {
	t *testing.T
	r *EmbeddingModelReconciler
}

func newBindHarness(t *testing.T, em *aiv1alpha1.EmbeddingModel, objs ...client.Object) *bindHarness {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).
		WithObjects(append(objs, em)...).WithStatusSubresource(em, &aiv1alpha1.ModelServer{}).Build()
	return &bindHarness{t: t, r: &EmbeddingModelReconciler{Client: cl, HTTPClient: http.DefaultClient}}
}

// reconcile runs one reconcile of the EmbeddingModel and returns it as stored.
func (h *bindHarness) reconcile() *aiv1alpha1.EmbeddingModel {
	h.t.Helper()
	req := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: testCrewNamespace, Name: embedTestName}}
	if _, err := h.r.Reconcile(context.Background(), req); err != nil {
		h.t.Fatalf("Reconcile: %v", err)
	}
	return h.get()
}

func (h *bindHarness) get() *aiv1alpha1.EmbeddingModel {
	h.t.Helper()
	got := &aiv1alpha1.EmbeddingModel{}
	if err := h.r.Get(context.Background(), client.ObjectKey{Namespace: testCrewNamespace, Name: embedTestName}, got); err != nil {
		h.t.Fatal(err)
	}
	return got
}

func (h *bindHarness) setServer(s *aiv1alpha1.ModelServer) {
	h.t.Helper()
	cur := &aiv1alpha1.ModelServer{}
	if err := h.r.Get(context.Background(), client.ObjectKeyFromObject(s), cur); err != nil {
		h.t.Fatal(err)
	}
	cur.Status = s.Status
	if err := h.r.Status().Update(context.Background(), cur); err != nil {
		h.t.Fatal(err)
	}
}

func boundCondition(t *testing.T, em *aiv1alpha1.EmbeddingModel) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(em.Status.Conditions, conditionTypeBound)
	if c == nil {
		t.Fatal("the EmbeddingModel has no Bound condition")
	}
	return c
}

func TestEmbeddingBinding_BindsToTheServerThatHasTheModel(t *testing.T) {
	empty := newEmbedOllama(t, false)
	has := newEmbedOllama(t, true)
	h := newBindHarness(t, unboundEmbedding(),
		embedServer("a-empty", empty.srv.URL, false), embedServer("b-has", has.srv.URL, true))

	em := h.reconcile()

	if em.Status.BoundServer != "b-has" || em.Status.Endpoint != has.srv.URL || !em.Status.Ready {
		t.Errorf("status = bound %q endpoint %q ready %v, want b-has, %s, ready", em.Status.BoundServer, em.Status.Endpoint, em.Status.Ready, has.srv.URL)
	}
	if c := boundCondition(t, em); c.Status != metav1.ConditionTrue || c.Reason != reasonBoundCatalog || !strings.Contains(c.Message, "b-has") {
		t.Errorf("Bound condition = %+v", c)
	}
	if em.Status.ModelInfo == nil || em.Status.ModelInfo.Dimensions != embedTestDims {
		t.Errorf("model info = %+v, want the dimensions the server reports", em.Status.ModelInfo)
	}
	if em.Spec.ServerRef != "" {
		t.Error("binding must not write the bound server into the spec")
	}
}

func TestEmbeddingBinding_NoServerNamesTheModelAndClearsTheEndpoint(t *testing.T) {
	empty := newEmbedOllama(t, false)
	em := unboundEmbedding()
	em.Status.Endpoint = "http://stale:11434"
	em.Status.BoundServer = "vanished"
	h := newBindHarness(t, em, embedServer("a-empty", empty.srv.URL, false))

	got := h.reconcile()

	if got.Status.Ready || got.Status.Endpoint != "" || got.Status.BoundServer != "" {
		t.Errorf("status = %+v, want not ready with no bound server and no endpoint", got.Status)
	}
	c := boundCondition(t, got)
	if c.Status != metav1.ConditionFalse || c.Reason != reasonNoServer || !strings.Contains(c.Message, embedTestModel) || !strings.Contains(c.Message, "kmctl model add") {
		t.Errorf("Bound condition = %+v, want NoServer naming the model and what to add", c)
	}
	if empty.pulls.Load() != 0 {
		t.Error("a server no Model asks to hold the model must not be asked to download it")
	}
}

func TestEmbeddingBinding_SkipsServersThatAreNotReady(t *testing.T) {
	has := newEmbedOllama(t, true)
	down := embedServer("a-down", has.srv.URL, true)
	down.Status.Ready = false
	h := newBindHarness(t, unboundEmbedding(), down)
	if got := h.reconcile(); got.Status.BoundServer != "" || boundCondition(t, got).Reason != reasonNoServer {
		t.Errorf("a server that is not ready must not be bound: %+v", got.Status)
	}
}

func TestEmbeddingBinding_RebindsWhenTheBoundServerStopsListingTheModel(t *testing.T) {
	first := newEmbedOllama(t, true)
	second := newEmbedOllama(t, true)
	h := newBindHarness(t, unboundEmbedding(),
		embedServer("a-first", first.srv.URL, true), embedServer(embedSecond, second.srv.URL, true))
	if got := h.reconcile(); got.Status.BoundServer != "a-first" {
		t.Fatalf("first binding = %q, want a-first (stable order)", got.Status.BoundServer)
	}

	h.setServer(embedServer("a-first", first.srv.URL, false))
	got := h.reconcile()

	if got.Status.BoundServer != embedSecond || got.Status.Endpoint != second.srv.URL || !got.Status.Ready {
		t.Errorf("after a-first dropped the model: bound %q endpoint %q ready %v, want b-second", got.Status.BoundServer, got.Status.Endpoint, got.Status.Ready)
	}
}

func TestEmbeddingBinding_RebindsWhenTheBoundServerIsRemoved(t *testing.T) {
	first := newEmbedOllama(t, true)
	second := newEmbedOllama(t, true)
	gone := embedServer("a-first", first.srv.URL, true)
	h := newBindHarness(t, unboundEmbedding(), gone, embedServer(embedSecond, second.srv.URL, true))
	h.reconcile()
	if err := h.r.Delete(context.Background(), gone); err != nil {
		t.Fatal(err)
	}

	if got := h.reconcile(); got.Status.BoundServer != embedSecond || got.Status.Endpoint != second.srv.URL {
		t.Errorf("bound %q endpoint %q, want b-second", got.Status.BoundServer, got.Status.Endpoint)
	}
}

func TestEmbeddingBinding_StaysOnTheBoundServerWhenAnotherAlsoHasTheModel(t *testing.T) {
	first := newEmbedOllama(t, false)
	second := newEmbedOllama(t, true)
	h := newBindHarness(t, unboundEmbedding(),
		embedServer("a-first", first.srv.URL, false), embedServer(embedSecond, second.srv.URL, true))
	h.reconcile()

	first.present.Store(true)
	h.setServer(embedServer("a-first", first.srv.URL, true))

	if got := h.reconcile(); got.Status.BoundServer != embedSecond {
		t.Errorf("bound %q, want b-second to keep the model when a-first also gains it", got.Status.BoundServer)
	}
}

func TestEmbeddingBinding_ExplicitServerRefIsUnchanged(t *testing.T) {
	pinned := newEmbedOllama(t, true)
	other := newEmbedOllama(t, true)
	em := unboundEmbedding()
	em.Spec.ServerRef = embedPinned
	// z-pinned does not list the model in its catalog; a-other does and sorts first.
	h := newBindHarness(t, em,
		embedServer(embedPinned, pinned.srv.URL, false), embedServer("a-other", other.srv.URL, true))

	got := h.reconcile()

	if got.Status.BoundServer != embedPinned || got.Status.Endpoint != pinned.srv.URL || !got.Status.Ready {
		t.Errorf("bound %q endpoint %q ready %v, want the server spec.serverRef names", got.Status.BoundServer, got.Status.Endpoint, got.Status.Ready)
	}
	if c := boundCondition(t, got); c.Reason != reasonBoundRef || c.Status != metav1.ConditionTrue {
		t.Errorf("Bound condition = %+v, want ServerRef", c)
	}
}

func TestEmbeddingBinding_ExplicitServerRefPullsAsBefore(t *testing.T) {
	pinned := newEmbedOllama(t, false)
	em := unboundEmbedding()
	em.Spec.ServerRef = embedPinned
	h := newBindHarness(t, em, embedServer(embedPinned, pinned.srv.URL, false))

	h.reconcile()
	eventually(t, "the explicit server to be asked for the download", func() bool { return pinned.pulls.Load() == 1 })
}

func TestEmbeddingBinding_ExplicitServerRefMissingIsAnError(t *testing.T) {
	em := unboundEmbedding()
	em.Spec.ServerRef = "nowhere"
	h := newBindHarness(t, em)

	got := h.reconcile()

	if got.Status.State != "Error" || got.Status.Ready {
		t.Errorf("status = %+v, want Error", got.Status)
	}
	if c := boundCondition(t, got); c.Status != metav1.ConditionFalse || c.Reason != reasonServerGone {
		t.Errorf("Bound condition = %+v", c)
	}
}

func TestEmbeddingBinding_PullsOntoTheServerAModelDeclares(t *testing.T) {
	empty := newEmbedOllama(t, false)
	h := newBindHarness(t, unboundEmbedding(),
		embedServer("a-empty", empty.srv.URL, false), embedModelFor("a-empty"))

	got := h.reconcile()

	if got.Status.BoundServer != "a-empty" {
		t.Fatalf("bound %q, want a-empty, the server a Model asks to hold the model", got.Status.BoundServer)
	}
	eventually(t, "the declared server to be asked for the download", func() bool { return empty.pulls.Load() == 1 })
}

func TestEmbeddingBinding_PrefersAServerThatHasTheModelOverOneToDownloadOnto(t *testing.T) {
	declared := newEmbedOllama(t, false)
	has := newEmbedOllama(t, true)
	h := newBindHarness(t, unboundEmbedding(),
		embedServer("a-declared", declared.srv.URL, false), embedModelFor("a-declared"),
		embedServer("z-has", has.srv.URL, true))

	if got := h.reconcile(); got.Status.BoundServer != "z-has" {
		t.Errorf("bound %q, want z-has: present beats declared even when it sorts later", got.Status.BoundServer)
	}
	if declared.pulls.Load() != 0 {
		t.Error("nothing is downloaded while a server already has the model")
	}
}

func TestEmbeddingBinding_StaleCatalogDoesNotPullWithoutAModel(t *testing.T) {
	stale := newEmbedOllama(t, false) // the catalog still lists the model; the server no longer has it
	h := newBindHarness(t, unboundEmbedding(), embedServer("a-stale", stale.srv.URL, true))

	got := h.reconcile()

	if got.Status.Ready || got.Status.State != statePending || !strings.Contains(got.Status.Message, "a-stale") {
		t.Errorf("status = %+v, want Pending naming the server", got.Status)
	}
	if stale.pulls.Load() != 0 {
		t.Error("no Model asks for the download, so the operator must not pull")
	}
}

func TestEmbeddingBinding_DimensionMismatchIsNotBound(t *testing.T) {
	has := newEmbedOllama(t, true)
	big := embedServer("a-big", has.srv.URL, true)
	big.Status.EmbeddingModels[0].Dimensions = 1024
	h := newBindHarness(t, unboundEmbedding(), big)

	got := h.reconcile()

	c := boundCondition(t, got)
	if got.Status.BoundServer != "" || c.Reason != reasonNoServer || !strings.Contains(c.Message, "1024") {
		t.Errorf("bound %q, condition %+v, want NoServer naming the 1024-dimension server", got.Status.BoundServer, c)
	}
}

func TestEmbeddingCandidates_SkipsVLLMAndUnknownDimensionsStillBind(t *testing.T) {
	em := unboundEmbedding()
	vllm := embedServer("a-vllm", "http://v", true)
	vllm.Spec.Type = aiv1alpha1.ServerTypeVLLM
	unknown := embedServer("b-unknown", "http://u", true)
	unknown.Status.EmbeddingModels[0].Dimensions = 0
	cands, skipped := embeddingCandidates(em, []aiv1alpha1.ModelServer{*vllm, *unknown}, nil)
	if len(cands) != 1 || cands[0].server.Name != "b-unknown" || len(skipped) != 0 {
		t.Errorf("candidates = %+v skipped = %v, want only b-unknown", cands, skipped)
	}
}

func TestEmbeddingCandidates_OrderIsPresentThenBoundThenName(t *testing.T) {
	em := unboundEmbedding()
	em.Status.BoundServer = "m-bound"
	servers := []aiv1alpha1.ModelServer{
		*embedServer("z-last", "http://z", true), *embedServer("a-first", "http://a", true),
		*embedServer("m-bound", "http://m", true), *embedServer("0-declared", "http://d", false),
	}
	cands, _ := embeddingCandidates(em, servers, []aiv1alpha1.Model{*embedModelFor("0-declared")})
	order := make([]string, 0, len(cands))
	for _, c := range cands {
		order = append(order, c.server.Name)
	}
	if strings.Join(order, ",") != "m-bound,a-first,z-last,0-declared" {
		t.Errorf("order = %v, want the bound server, then names, then the declared one last", order)
	}
}

func TestEmbeddingCandidates_ModelNamesMatchWithOrWithoutLatest(t *testing.T) {
	em := unboundEmbedding()
	s := embedServer("a", "http://a", true) // lists nomic-embed-text:latest
	if c, _ := embeddingCandidates(em, []aiv1alpha1.ModelServer{*s}, nil); len(c) != 1 {
		t.Error("an EmbeddingModel with no tag must match the :latest tag")
	}
	em.Spec.Model = embedTestModel + ":v1.5"
	if c, _ := embeddingCandidates(em, []aiv1alpha1.ModelServer{*s}, nil); len(c) != 0 {
		t.Error("a different tag is a different model with different vectors")
	}
}

func TestEmbeddingCandidates_DisabledOrDeletingModelsDoNotDeclare(t *testing.T) {
	em := unboundEmbedding()
	s := embedServer("a", "http://a", false)
	disabled := embedModelFor("a")
	disabled.Spec.Disabled = true
	if c, _ := embeddingCandidates(em, []aiv1alpha1.ModelServer{*s}, []aiv1alpha1.Model{*disabled}); len(c) != 0 {
		t.Error("a disabled Model does not ask for a download")
	}
}

func TestWantedPullKeysUsesTheBoundServer(t *testing.T) {
	em := unboundEmbedding()
	em.Status.BoundServer = "a-bound"
	unbound := unboundEmbedding()
	unbound.Name = "unbound"
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(em, unbound).Build()

	wanted, err := wantedPullKeys(context.Background(), cl, &aiv1alpha1.Model{})
	if err != nil {
		t.Fatal(err)
	}
	if !wanted[pullKey("a-bound", embedTestModel)] || len(wanted) != 1 {
		t.Errorf("wanted = %v, want only the bound server's key (an unbound model wants nothing yet)", wanted)
	}
}

func TestBoundServerName(t *testing.T) {
	em := unboundEmbedding()
	if em.BoundServerName() != "" {
		t.Error("an unbound model has no server")
	}
	em.Status.BoundServer = "carried"
	if em.BoundServerName() != "carried" {
		t.Error("the bound server")
	}
	em.Spec.ServerRef = embedPinnedName
	if em.BoundServerName() != embedPinnedName {
		t.Error("spec.serverRef wins")
	}
}

func TestEnqueueUnboundEmbeddingModelsSkipsPinnedOnes(t *testing.T) {
	pinned := unboundEmbedding()
	pinned.Name = embedPinnedName
	pinned.Spec.ServerRef = "x"
	h := newBindHarness(t, unboundEmbedding(), pinned)

	reqs := h.r.enqueueUnboundEmbeddingModels(context.Background(), embedServer("s", "http://s", true))

	if len(reqs) != 1 || reqs[0].Name != embedTestName {
		t.Errorf("requests = %v, want only the model that picks its server from the catalog", reqs)
	}
}

func TestModelServerEmbeddingPredicate(t *testing.T) {
	p := modelServerEmbeddingPredicate()
	base := embedServer("s", "http://s", true)
	same := base.DeepCopy()
	if p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: same}) {
		t.Error("an update that changes nothing the binding reads must not enqueue")
	}
	for name, mutate := range map[string]func(*aiv1alpha1.ModelServer){
		"readiness":   func(s *aiv1alpha1.ModelServer) { s.Status.Ready = false },
		"endpoint":    func(s *aiv1alpha1.ModelServer) { s.Spec.Endpoint = "http://other" },
		"engine type": func(s *aiv1alpha1.ModelServer) { s.Spec.Type = aiv1alpha1.ServerTypeVLLM },
		"embeddings":  func(s *aiv1alpha1.ModelServer) { s.Status.EmbeddingModels = nil },
	} {
		changed := base.DeepCopy()
		mutate(changed)
		if !p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: changed}) {
			t.Errorf("a change of %s must enqueue", name)
		}
	}
	if !p.Create(event.CreateEvent{Object: base}) || !p.Delete(event.DeleteEvent{Object: base}) {
		t.Error("a ModelServer that appears or goes away must enqueue")
	}
}

func TestEmbeddingEndpointPredicate(t *testing.T) {
	p := embeddingEndpointPredicate()
	base := unboundEmbedding()
	if p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: base.DeepCopy()}) {
		t.Error("no change, no enqueue")
	}
	moved := base.DeepCopy()
	moved.Status.Endpoint = "http://elsewhere"
	if !p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: moved}) {
		t.Error("a new endpoint must enqueue the RAGSources that use it")
	}
	ready := base.DeepCopy()
	ready.Status.Ready = true
	if !p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: ready}) {
		t.Error("readiness must enqueue")
	}
}

func TestRAGSourcesOfEmbeddingModelMatchesByName(t *testing.T) {
	use := &aiv1alpha1.RAGSource{ObjectMeta: metav1.ObjectMeta{Name: "ragdocs", Namespace: testCrewNamespace}, Spec: aiv1alpha1.RAGSourceSpec{EmbeddingModelRef: embedTestName}}
	other := &aiv1alpha1.RAGSource{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: testCrewNamespace}, Spec: aiv1alpha1.RAGSourceSpec{EmbeddingModelRef: "different"}}
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(use, other).Build()
	r := &RAGSourceReconciler{Client: cl}

	reqs := r.ragSourcesOfEmbeddingModel(context.Background(), unboundEmbedding())

	if len(reqs) != 1 || reqs[0].Name != "ragdocs" {
		t.Errorf("requests = %v, want only the RAGSource that names the model", reqs)
	}
}
