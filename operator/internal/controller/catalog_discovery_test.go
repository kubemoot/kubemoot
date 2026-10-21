package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/internal/engine/ollama"
)

const (
	earlierEntry = "earlier-entry"
	showQwen14   = `{"details":{"family":"qwen3","parameter_size":"14.8B"},
 "model_info":{"general.architecture":"qwen3","general.parameter_count":14768307200,"qwen3.context_length":40960},
 "capabilities":["completion","tools","thinking"]}`
	showEmbed = `{"details":{"family":"nomic-bert","parameter_size":"137M"},"model_info":{"general.architecture":"nomic-bert","nomic-bert.embedding_length":768},"capabilities":["embedding"]}`
	tagsBody  = `{"models":[
 {"name":"qwen3:14b","digest":"d14","size":9000,"details":{"family":"qwen3","parameter_size":"14.8B"}},
 {"name":"gemma3:4b","digest":"d4","size":3000,"details":{"family":"gemma3","parameter_size":"4.3B"}},
 {"name":"nomic-embed-text","digest":"de","size":300,"details":{"family":"nomic-bert","parameter_size":"137M"}}]}`
)

// catalogOllama serves /api/tags and /api/show (qwen3:14b and the embedder answer; any
// other model's show fails) and counts the show calls.
func catalogOllama(t *testing.T, shows *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(tagsBody))
		case "/api/show":
			shows.Add(1)
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.Model {
			case candModel14:
				_, _ = w.Write([]byte(showQwen14))
			case mpEmbedModel:
				_, _ = w.Write([]byte(showEmbed))
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ollamaFactsByName runs ollamaFacts against the fake server and indexes the result.
func ollamaFactsByName(t *testing.T) map[string]catalogFacts {
	t.Helper()
	var shows atomic.Int32
	srv := catalogOllama(t, &shows)
	r := &ModelServerReconciler{}
	facts, err := r.ollamaFacts(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]catalogFacts{}
	for _, f := range facts {
		by[f.Name] = f
	}
	return by
}

func TestOllamaFactsReadParametersContextAndAbilitiesFromShow(t *testing.T) {
	q := ollamaFactsByName(t)[candModel14]
	if q.ParameterCount != 14768307200 || q.Source != aiv1alpha1.CatalogSourceOllamaShow || q.ContextLength != 40960 || !q.Present {
		t.Errorf("qwen3:14b facts = %+v", q)
	}
	if !slices.Equal(q.Capabilities, []string{testToolCalling, candReasoning}) {
		t.Errorf("abilities = %v", q.Capabilities)
	}
}

func TestOllamaFactsFallBackToTagsWhenShowFails(t *testing.T) {
	g := ollamaFactsByName(t)["gemma3:4b"]
	if g.ParameterCount != 4_300_000_000 || g.Source != aiv1alpha1.CatalogSourceOllamaTags || g.Family != "gemma3" || g.Capabilities != nil {
		t.Errorf("a failed /api/show must fall back to /api/tags: %+v", g)
	}
}

func TestOllamaFactsFlagTheEmbeddingModel(t *testing.T) {
	if e := ollamaFactsByName(t)[mpEmbedModel]; !e.Embedding {
		t.Errorf("the embedding model must be flagged: %+v", e)
	}
}

func TestOllamaFactsReadTheEmbeddingDimensions(t *testing.T) {
	facts := ollamaFactsByName(t)
	if e := facts[mpEmbedModel]; e.EmbeddingDimensions != 768 {
		t.Errorf("embedding dimensions = %d, want 768", e.EmbeddingDimensions)
	}
	list := make([]catalogFacts, 0, len(facts))
	for _, f := range facts {
		list = append(list, f)
	}
	got := buildEmbeddingCatalog(list)
	if len(got) != 1 || got[0].Name != mpEmbedModel || got[0].Dimensions != 768 {
		t.Errorf("embedding catalog = %+v, want only the embedder with 768 dimensions (chat models stay out)", got)
	}
}

func TestBuildEmbeddingCatalogSkipsAbsentAndOrdersByName(t *testing.T) {
	got := buildEmbeddingCatalog([]catalogFacts{
		{Name: "z-embed", Present: true, Embedding: true},
		{Name: "gone-embed", Present: false, Embedding: true},
		{Name: "a-embed", Present: true, Embedding: true, EmbeddingDimensions: 1024},
		{Name: "chat", Present: true},
	})
	if len(got) != 2 || got[0].Name != "a-embed" || got[0].Dimensions != 1024 || got[1].Name != "z-embed" {
		t.Errorf("embedding catalog = %+v", got)
	}
	if buildEmbeddingCatalog(nil) != nil {
		t.Error("no facts must give no embedding models")
	}
}

func TestOllamaFactsShowRunsOncePerDigest(t *testing.T) {
	var shows atomic.Int32
	srv := catalogOllama(t, &shows)
	r := &ModelServerReconciler{}
	for range 3 {
		if _, err := r.ollamaFacts(context.Background(), srv.Client(), srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	// qwen3:14b and the embedder are cached after one call each; gemma3:4b's show
	// fails every time and is retried.
	if got := shows.Load(); got != 2+3 {
		t.Errorf("show calls = %d, want 5 (2 cached models once, the failing one on each of 3 probes)", got)
	}
}

func TestOllamaFactsErrorsWhenTagsDoNotAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	r := &ModelServerReconciler{}
	if _, err := r.ollamaFacts(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Error("expected an error")
	}
}

func TestFactsFromOllamaParameterSources(t *testing.T) {
	tag := ollama.ModelInfo{Name: "m", Details: ollama.ModelDetails{ParameterSize: "7B", Family: "fam"}}
	// show lacks general.parameter_count but has parameter_size
	f := factsFromOllama(tag, ollama.ShowInfo{ParameterSize: "8.2B"}, true)
	if f.ParameterCount != 8_200_000_000 || f.Source != aiv1alpha1.CatalogSourceOllamaShow {
		t.Errorf("show parameter_size: %+v", f)
	}
	// show says nothing: tags
	f = factsFromOllama(tag, ollama.ShowInfo{}, true)
	if f.ParameterCount != 7_000_000_000 || f.Source != aiv1alpha1.CatalogSourceOllamaTags || f.Family != "fam" {
		t.Errorf("tags: %+v", f)
	}
	// nothing anywhere
	f = factsFromOllama(ollama.ModelInfo{Name: "m"}, ollama.ShowInfo{}, false)
	if f.ParameterCount != 0 || f.Source != aiv1alpha1.CatalogSourceNone {
		t.Errorf("none: %+v", f)
	}
}

func TestParseDeclaredModels(t *testing.T) {
	got := parseDeclaredModels(`[{"name":"qwen3:14b","parameters":"14B","capabilities":["tool-calling"]},{"name":""},{"name":"b"}]`)
	if len(got) != 2 || got[candModel14].Parameters != "14B" || !slices.Equal(got[candModel14].Capabilities, []string{testToolCalling}) {
		t.Errorf("declared = %+v", got)
	}
	for _, bad := range []string{"", "{broken", `{"name":"x"}`} {
		if got := parseDeclaredModels(bad); len(got) != 0 {
			t.Errorf("parseDeclaredModels(%q) = %+v, want none", bad, got)
		}
	}
}

// vllmCatalogFixture is a vllm server with a serviceRef whose Service carries the
// kubemoot-vllm chart's models annotation, and the fake servers behind its ports.
func vllmCatalogFixture(t *testing.T, annotation string) (*ModelServerReconciler, *aiv1alpha1.ModelServer, *http.Client) {
	t.Helper()
	srv14 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"qwen3:14b","max_model_len":16384}]}`)
	}))
	t.Cleanup(srv14.Close)
	svc := refService1(8000)
	if annotation != "" {
		svc.Annotations = map[string]string{AnnotationVLLMModels: annotation}
	}
	cli := fake.NewClientBuilder().WithScheme(refScheme(t)).WithObjects(svc).Build()
	p := refServer()
	p.Status.VLLMServers = []aiv1alpha1.VLLMServer{{Model: candModel14, Endpoint: fmt.Sprintf(refEndpoint, 8000)}}
	return &ModelServerReconciler{Client: cli}, p, servedRoots{8000: srv14}.client()
}

func TestVLLMFactsReadTheServiceAnnotationAndServerContext(t *testing.T) {
	r, p, httpClient := vllmCatalogFixture(t, `[{"name":"qwen3:14b","parameters":"14B","capabilities":["tool-calling","reasoning"]}]`)
	snap := engine.Snapshot{Available: []aiv1alpha1.AvailableModel{{Name: candModel14}}}
	facts := r.vllmFacts(context.Background(), httpClient, p, snap)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v", facts)
	}
	f := facts[0]
	if !f.Present || f.ParameterCount != 14_000_000_000 || f.Source != aiv1alpha1.CatalogSourceService || f.ContextLength != 16384 ||
		!slices.Equal(f.Capabilities, []string{testToolCalling, candReasoning}) {
		t.Errorf("facts = %+v", f)
	}
}

func TestVLLMFactsWithoutAnAnnotationHaveNoSize(t *testing.T) {
	r, p, httpClient := vllmCatalogFixture(t, "")
	facts := r.vllmFacts(context.Background(), httpClient, p, engine.Snapshot{})
	if len(facts) != 1 || facts[0].Present || facts[0].ParameterCount != 0 || facts[0].Source != aiv1alpha1.CatalogSourceNone {
		t.Errorf("facts = %+v, want a model that is not answering and has no size", facts)
	}
}

func TestVLLMFactsOfAServersServerDeclareNothing(t *testing.T) {
	r := &ModelServerReconciler{Client: fake.NewClientBuilder().WithScheme(refScheme(t)).Build()}
	p := &aiv1alpha1.ModelServer{Spec: aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeVLLM,
		Servers: []aiv1alpha1.VLLMServer{{Model: "m", Endpoint: "http://127.0.0.1:1"}}}}
	facts := r.vllmFacts(context.Background(), http.DefaultClient, p, engine.Snapshot{})
	if len(facts) != 1 || facts[0].Name != "m" || facts[0].ParameterCount != 0 {
		t.Errorf("facts = %+v", facts)
	}
}

func TestPublishCatalogMergesModelsInTheServersNamespaceOnly(t *testing.T) {
	scheme := refScheme(t)
	inCatalog := catModel("o-gemma", "gemma3:4b", func(m *aiv1alpha1.Model) { m.Spec.Parameters = "4B" })
	crewLocal := catModel("crew", candModel14, func(m *aiv1alpha1.Model) { m.Namespace = testPlacementNS; m.Spec.Disabled = true })
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inCatalog, crewLocal).Build()
	r := &ModelServerReconciler{Client: cli}
	p := catServerObj(aiv1alpha1.ServerTypeOllama)
	r.publishCatalog(context.Background(), p, []catalogFacts{
		{Name: candModel14, Present: true, ParameterCount: 14_800_000_000},
		{Name: "gemma3:4b", Present: true},
	})
	if len(p.Status.Models) != 2 {
		t.Fatalf("models = %+v, want both (a Model in another namespace must not disable the catalog entry)", p.Status.Models)
	}
	if e := catEntry(t, p.Status.Models, "gemma3:4b"); e.Size != aiv1alpha1.SizeSmall || e.ModelRef != "kubemoot/o-gemma" {
		t.Errorf("gemma entry = %+v", e)
	}
	if c := meta.FindStatusCondition(p.Status.Conditions, conditionTypeCatalogSizeKnown); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("condition = %+v", c)
	}
}

func TestPublishCatalogLeavesTheEarlierCatalogWhenModelsCannotBeListed(t *testing.T) {
	r := &ModelServerReconciler{Client: failingLister{}}
	p := catServerObj(aiv1alpha1.ServerTypeOllama)
	p.Status.Models = []aiv1alpha1.CatalogModel{{Name: earlierEntry}}
	r.publishCatalog(context.Background(), p, []catalogFacts{{Name: "newly-found"}})
	if len(p.Status.Models) != 1 || p.Status.Models[0].Name != earlierEntry {
		t.Errorf("models = %+v, want the earlier catalog", p.Status.Models)
	}
}

// failingLister is a client whose List always fails.
type failingLister struct{ client.Client }

func (failingLister) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return fmt.Errorf("boom")
}

func TestDiscoverOllamaCatalogKeepsTheEarlierCatalogWhenOllamaDoesNotAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	r := &ModelServerReconciler{Client: fake.NewClientBuilder().WithScheme(refScheme(t)).Build()}
	p := catServerObj(aiv1alpha1.ServerTypeOllama)
	p.Spec.Endpoint = srv.URL
	p.Status.Models = []aiv1alpha1.CatalogModel{{Name: earlierEntry}}
	r.discoverOllamaCatalog(context.Background(), p, srv.Client())
	if len(p.Status.Models) != 1 {
		t.Errorf("models = %+v", p.Status.Models)
	}
}

func TestModelToServerMapsToTheServerInTheModelsNamespace(t *testing.T) {
	m := catModel("m", "x", nil)
	reqs := modelToServer(context.Background(), m)
	if len(reqs) != 1 || reqs[0].Namespace != catNS || reqs[0].Name != catServer {
		t.Errorf("requests = %+v", reqs)
	}
	if got := modelToServer(context.Background(), &corev1.Pod{}); got != nil {
		t.Errorf("a non-Model maps to %+v", got)
	}
	if got := modelToServer(context.Background(), &aiv1alpha1.Model{}); got != nil {
		t.Errorf("a Model without serverRef maps to %+v", got)
	}
}

func TestWithCatalogModelsAddsPresentCatalogEntriesOnce(t *testing.T) {
	p := catServerObj(aiv1alpha1.ServerTypeOllama)
	p.Status.Models = []aiv1alpha1.CatalogModel{{Name: "a", Present: true}, {Name: "b", Present: true}, {Name: "absent-model", Present: false}}
	got := withCatalogModels([]string{"a"}, p)
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("bound = %v, want a, b (no duplicate, no absent model)", got)
	}
}
