package controller

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	catNS       = "kubemoot"
	catProvider = "ollama-gpu"
	catRev      = "abcdef1"
)

func catProviderObj(t aiv1alpha1.ProviderType) *aiv1alpha1.ModelProvider {
	return &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: catProvider, Namespace: catNS},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: t},
	}
}

func catModel(name, model string, mutate func(*aiv1alpha1.Model)) *aiv1alpha1.Model {
	m := &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: catNS},
		Spec:       aiv1alpha1.ModelSpec{ProviderRef: catProvider, Model: model},
	}
	if mutate != nil {
		mutate(m)
	}
	return m
}

func catEntry(t *testing.T, entries []aiv1alpha1.CatalogModel, name string) aiv1alpha1.CatalogModel {
	t.Helper()
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no catalog entry %q in %+v", name, entries)
	return aiv1alpha1.CatalogModel{}
}

func TestBuildCatalogDiscoveredModelJoinsWithDerivedLabels(t *testing.T) {
	facts := []catalogFacts{{Name: candModel14, Present: true, Source: aiv1alpha1.CatalogSourceOllamaShow,
		ParameterCount: 14_800_000_000, ContextLength: 40960, Capabilities: []string{candReasoning, testToolCalling}, Family: candQwen}}
	got := buildCatalog(catProviderObj(aiv1alpha1.ProviderTypeOllama), facts, nil)
	if len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
	e := got[0]
	want := map[string]string{
		"kubemoot.ai/size": candMedium, "kubemoot.ai/context-tokens": "40960", "kubemoot.ai/family": "qwen3",
		"kubemoot.ai/engine": candProvider, "capability/tool-calling": valueTrue, "capability/reasoning": valueTrue,
	}
	for k, v := range want {
		if e.Labels[k] != v {
			t.Errorf("label %s = %q, want %q (labels %v)", k, e.Labels[k], v, e.Labels)
		}
	}
	if e.Size != candMedium || !e.Present || e.Source != aiv1alpha1.CatalogSourceOllamaShow || e.ModelRef != "" {
		t.Errorf("entry = %+v", e)
	}
}

func TestBuildCatalogModelObjectBeatsDiscoveryForEveryField(t *testing.T) {
	facts := []catalogFacts{{Name: candModel14, Present: true, Source: aiv1alpha1.CatalogSourceOllamaShow,
		ParameterCount: 14_800_000_000, ContextLength: 40960, Capabilities: []string{testToolCalling}, Family: candQwen}}
	m := catModel("o-qwen3-14b", candModel14, func(m *aiv1alpha1.Model) {
		m.Spec.Parameters = "33B"
		m.Spec.ContextLength = 8192
		m.Spec.Capabilities = []string{"Reasoning", " vision "}
		m.Labels = map[string]string{"kubemoot.ai/data-residency": "on-premises"}
	})
	got := buildCatalog(catProviderObj(aiv1alpha1.ProviderTypeOllama), facts, map[string]*aiv1alpha1.Model{candModel14: m})
	e := catEntry(t, got, candModel14)
	if e.Size != "large" || e.Source != aiv1alpha1.CatalogSourceModel || e.ContextLength != 8192 || e.ModelRef != "kubemoot/o-qwen3-14b" {
		t.Errorf("entry = %+v", e)
	}
	if !slices.Equal(e.Capabilities, []string{candReasoning, "vision"}) {
		t.Errorf("capabilities = %v, want the Model's only", e.Capabilities)
	}
	if e.Labels["capability/tool-calling"] != "" || e.Labels["capability/vision"] != valueTrue || e.Labels["kubemoot.ai/data-residency"] != "on-premises" {
		t.Errorf("labels = %v", e.Labels)
	}
}

func TestBuildCatalogSizeLabelOnModelOverridesDerivedClass(t *testing.T) {
	facts := []catalogFacts{{Name: "qwen3:30b", Present: true, ParameterCount: 30_500_000_000}}
	m := catModel("moe", "qwen3:30b", func(m *aiv1alpha1.Model) { m.Labels = map[string]string{aiv1alpha1.LabelSize: candMedium} })
	e := catEntry(t, buildCatalog(catProviderObj(aiv1alpha1.ProviderTypeOllama), facts, map[string]*aiv1alpha1.Model{"qwen3:30b": m}), "qwen3:30b")
	if e.Size != candMedium || e.Labels[aiv1alpha1.LabelSize] != candMedium {
		t.Errorf("entry = %+v, want the Model's size label to win", e)
	}
}

func TestBuildCatalogDisabledAndEmbeddingModelsStayOut(t *testing.T) {
	facts := []catalogFacts{
		{Name: "a", Present: true, ParameterCount: 1_000_000_000},
		{Name: "b", Present: true, ParameterCount: 1_000_000_000},
		{Name: mpEmbedModel, Present: true, Embedding: true},
	}
	off := catModel("off", "a", func(m *aiv1alpha1.Model) { m.Spec.Disabled = true })
	got := buildCatalog(catProviderObj(aiv1alpha1.ProviderTypeOllama), facts, map[string]*aiv1alpha1.Model{"a": off})
	if len(got) != 1 || got[0].Name != "b" {
		t.Errorf("entries = %+v, want only b", got)
	}
}

func TestBuildCatalogModelObjectWithoutDiscoveryIsListedNotPresent(t *testing.T) {
	m := catModel("wanted", "llama3.3:70b", func(m *aiv1alpha1.Model) { m.Spec.Parameters = "70B" })
	got := buildCatalog(catProviderObj(aiv1alpha1.ProviderTypeOllama), nil, map[string]*aiv1alpha1.Model{"llama3.3:70b": m})
	e := catEntry(t, got, "llama3.3:70b")
	if e.Present || e.Size != "large" || e.Source != aiv1alpha1.CatalogSourceModel {
		t.Errorf("entry = %+v", e)
	}
}

func TestBuildCatalogUnknownSizeHasNoSizeLabelAndIsFlagged(t *testing.T) {
	p := catProviderObj(aiv1alpha1.ProviderTypeVLLM)
	p.Status.Models = buildCatalog(p, []catalogFacts{{Name: "mystery", Present: true}, {Name: "known", Present: true, ParameterCount: 8_000_000_000}}, nil)
	e := catEntry(t, p.Status.Models, "mystery")
	if _, has := e.Labels[aiv1alpha1.LabelSize]; has || e.Size != "" || e.Source != aiv1alpha1.CatalogSourceNone {
		t.Errorf("entry = %+v, want no size", e)
	}
	setCatalogSizeCondition(p)
	c := meta.FindStatusCondition(p.Status.Conditions, conditionTypeCatalogSizeKnown)
	if c == nil || c.Status != metav1.ConditionFalse || !strings.Contains(c.Message, "mystery") || strings.Contains(c.Message, "known,") {
		t.Errorf("condition = %+v", c)
	}
	p.Status.Models = p.Status.Models[:0]
	p.Status.Models = buildCatalog(p, []catalogFacts{{Name: "known", Present: true, ParameterCount: 8_000_000_000}}, nil)
	setCatalogSizeCondition(p)
	if c := meta.FindStatusCondition(p.Status.Conditions, conditionTypeCatalogSizeKnown); c.Status != metav1.ConditionTrue {
		t.Errorf("condition = %+v, want True once every size is known", c)
	}
}

func TestBuildCatalogIsSortedByName(t *testing.T) {
	facts := []catalogFacts{{Name: "z", Present: true}, {Name: "a", Present: true}, {Name: "m", Present: true}}
	names := make([]string, 0, 3)
	for _, e := range buildCatalog(catProviderObj(aiv1alpha1.ProviderTypeOllama), facts, nil) {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"a", "m", "z"}) {
		t.Errorf("names = %v", names)
	}
}

func TestCatalogModelsSelectsByNamespaceProviderAndDeletion(t *testing.T) {
	p := catProviderObj(aiv1alpha1.ProviderTypeOllama)
	other := catModel("crew-local", "x", func(m *aiv1alpha1.Model) { m.Namespace = testPlacementNS })
	elsewhere := catModel("other-provider", "y", func(m *aiv1alpha1.Model) { m.Spec.ProviderRef = "vllm" })
	deleting := catModel("going", "z", func(m *aiv1alpha1.Model) { now := metav1.Now(); m.DeletionTimestamp = &now })
	mine := catModel("mine", "qwen3:8b", nil)
	got := catalogModels(p, []aiv1alpha1.Model{*other, *elsewhere, *deleting, *mine})
	if len(got) != 1 || got["qwen3:8b"] == nil {
		t.Errorf("catalog models = %v, want only mine", got)
	}
}

func TestCatalogModelNameFollowsTheEngine(t *testing.T) {
	m := catModel("m", "served-name", func(m *aiv1alpha1.Model) {
		m.Spec.Source = &aiv1alpha1.ModelSource{Ollama: &aiv1alpha1.OllamaSource{Tag: "served-name"}}
	})
	if got := catalogModelName(catProviderObj(aiv1alpha1.ProviderTypeVLLM), m); got != "served-name" {
		t.Errorf("vllm name = %q", got)
	}
	hf := catModel("hf", "served", func(m *aiv1alpha1.Model) {
		m.Spec.Source = &aiv1alpha1.ModelSource{HuggingFace: &aiv1alpha1.HuggingFaceSource{Repo: "a/b", Revision: catRev}}
	})
	if got := catalogModelName(catProviderObj(aiv1alpha1.ProviderTypeOllama), hf); got != "" {
		t.Errorf("an ollama provider has nothing to name for a huggingFace source, got %q", got)
	}
}

func TestNormalizeAbilities(t *testing.T) {
	got := normalizeAbilities([]string{"Tool-Calling", testToolCalling, "", " reasoning "})
	if !slices.Equal(got, []string{candReasoning, testToolCalling}) {
		t.Errorf("abilities = %v", got)
	}
	if normalizeAbilities(nil) != nil {
		t.Error("nil in, nil out")
	}
}
