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
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	sourceTestRepo     = "Qwen/Qwen3-14B-AWQ"
	sourceTestRevision = "31c69efc"
	sourceTestTag      = "qwen3:14b"
)

func ollamaSource() *aiv1alpha1.ModelSource {
	return &aiv1alpha1.ModelSource{Ollama: &aiv1alpha1.OllamaSource{Tag: sourceTestTag}}
}

func hfSource(revision string) *aiv1alpha1.ModelSource {
	return &aiv1alpha1.ModelSource{HuggingFace: &aiv1alpha1.HuggingFaceSource{
		Repo: sourceTestRepo, Revision: revision, Quantization: "awq",
	}}
}

var _ = Describe("Model spec.source admission", func() {
	newModel := func(name string, src *aiv1alpha1.ModelSource) *aiv1alpha1.Model {
		return &aiv1alpha1.Model{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
			Spec: aiv1alpha1.ModelSpec{
				ProviderRef: "adm-provider", Model: "qwen3:14b", Source: src,
			},
		}
	}
	accepts := func(name string, src *aiv1alpha1.ModelSource) {
		m := newModel(name, src)
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
		Expect(k8sClient.Delete(ctx, m)).To(Succeed())
	}

	It("accepts an absent source", func() { accepts("src-none", nil) })
	It("accepts an ollama source", func() { accepts("src-ollama", ollamaSource()) })
	It("accepts a huggingFace source with a short sha", func() { accepts("src-hf-short", hfSource("deadbee")) })
	It("accepts a huggingFace source with a full sha", func() {
		accepts("src-hf-full", hfSource("31c69efc0123456789abcdef0123456789abcdef"))
	})

	It("rejects a source with neither field", func() {
		err := k8sClient.Create(ctx, newModel("src-zero", &aiv1alpha1.ModelSource{}))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of ollama or huggingFace"))
	})

	It("rejects a source with both fields", func() {
		both := hfSource(sourceTestRevision)
		both.Ollama = &aiv1alpha1.OllamaSource{Tag: sourceTestTag}
		err := k8sClient.Create(ctx, newModel("src-two", both))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of ollama or huggingFace"))
	})

	for name, revision := range map[string]string{
		"too-short":  "dead12",
		"too-long":   "31c69efc0123456789abcdef0123456789abcdef0",
		"not-hex":    "main",
		"branch-ish": "v1.0.0",
	} {
		It("rejects a huggingFace revision that is "+name, func() {
			err := k8sClient.Create(ctx, newModel("src-rev-"+name, hfSource(revision)))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("revision"))
		})
	}

	It("accepts an ollama tag equal to spec.model", func() { accepts("src-tag-equal", ollamaSource()) })

	It("rejects an ollama tag that differs from spec.model", func() {
		m := newModel("src-tag-differs", ollamaSource())
		m.Spec.Model = testModelID
		err := k8sClient.Create(ctx, m)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("source.ollama.tag must equal spec.model"))
	})

	Describe("changing source after creation", func() {
		update := func(name string, before, after *aiv1alpha1.ModelSource) error {
			m := newModel(name, before)
			Expect(k8sClient.Create(ctx, m)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, m) })
			m.Spec.Source = after
			return k8sClient.Update(ctx, m)
		}
		rejected := func(err error) {
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.source is immutable"))
		}

		It("accepts adding an ollama source (tag equal to spec.model) to a Model without one", func() {
			Expect(update("src-add-ollama", nil, ollamaSource())).To(Succeed())
		})
		It("rejects adding an ollama source whose tag differs from spec.model", func() {
			m := newModel("src-add-ollama-differs", nil)
			m.Spec.Model = testModelID
			Expect(k8sClient.Create(ctx, m)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, m) })
			m.Spec.Source = ollamaSource()
			err := k8sClient.Update(ctx, m)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("source.ollama.tag must equal spec.model"))
		})
		It("rejects changing spec.model while an ollama source is set", func() {
			m := newModel("src-model-change-ollama", ollamaSource())
			Expect(k8sClient.Create(ctx, m)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, m) })
			m.Spec.Model = testModelID
			err := k8sClient.Update(ctx, m)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("source.ollama.tag must equal spec.model"))
		})
		It("accepts changing spec.model on a Model without a source", func() {
			m := newModel("src-model-change-none", nil)
			Expect(k8sClient.Create(ctx, m)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, m) })
			m.Spec.Model = testModelID
			Expect(k8sClient.Update(ctx, m)).To(Succeed())
		})
		It("rejects adding a huggingFace source to a Model without one", func() {
			rejected(update("src-add-hf", nil, hfSource(sourceTestRevision)))
		})
		It("rejects changing an ollama source to huggingFace", func() {
			rejected(update("src-ollama-to-hf", ollamaSource(), hfSource(sourceTestRevision)))
		})
		It("rejects removing an ollama source", func() {
			rejected(update("src-remove-ollama", ollamaSource(), nil))
		})
		It("rejects changing a huggingFace revision", func() {
			rejected(update("src-hf-revision", hfSource(sourceTestRevision), hfSource("deadbee")))
		})
	})

	It("rejects a huggingFace source without a repo", func() {
		src := hfSource(sourceTestRevision)
		src.HuggingFace.Repo = ""
		Expect(k8sClient.Create(ctx, newModel("src-norepo", src))).NotTo(Succeed())
	})
})

func TestOllamaTag_SelectsSourceTagElseModel(t *testing.T) {
	cases := map[string]struct {
		src  *aiv1alpha1.ModelSource
		want string
	}{
		"absent source uses spec.model": {nil, testModelID},
		"ollama source uses its tag":    {ollamaSource(), sourceTestTag},
		"huggingFace has no tag":        {hfSource(sourceTestRevision), ""},
		"empty source uses spec.model":  {&aiv1alpha1.ModelSource{}, testModelID},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := &aiv1alpha1.Model{Spec: aiv1alpha1.ModelSpec{Model: testModelID, Source: tc.src}}
			if got := m.OllamaTag(); got != tc.want {
				t.Errorf("OllamaTag() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSourceMismatch(t *testing.T) {
	const vllm = aiv1alpha1.ProviderType("vllm")
	cases := map[string]struct {
		src      *aiv1alpha1.ModelSource
		provider aiv1alpha1.ProviderType
		mismatch bool
	}{
		"absent on ollama":        {nil, aiv1alpha1.ProviderTypeOllama, false},
		"ollama on ollama":        {ollamaSource(), aiv1alpha1.ProviderTypeOllama, false},
		"huggingFace on ollama":   {hfSource(sourceTestRevision), aiv1alpha1.ProviderTypeOllama, true},
		"ollama on a vllm engine": {ollamaSource(), vllm, true},
		"huggingFace on vllm":     {hfSource(sourceTestRevision), vllm, false},
		"absent on vllm":          {nil, vllm, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := &aiv1alpha1.Model{Spec: aiv1alpha1.ModelSpec{Model: testModelID, Source: tc.src}}
			msg := sourceMismatch(m, tc.provider)
			if (msg != "") != tc.mismatch {
				t.Errorf("sourceMismatch = %q, want mismatch=%v", msg, tc.mismatch)
			}
		})
	}
}

// reconcileWithSource reconciles one Model carrying src against a Ready ollama
// provider and returns the Model as stored afterwards, with the fake server.
func reconcileWithSource(t *testing.T, src *aiv1alpha1.ModelSource) (*aiv1alpha1.Model, *fakeOllama) {
	t.Helper()
	f := newFakeOllama(t, func(w http.ResponseWriter, _ *http.Request) { emit(w, `{"status":"success"}`) })
	model, provider := absentOllamaModel(f.srv.URL)
	model.Spec.Source = src
	provider.Status.Ready = true
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).
		WithObjects(model, provider).WithStatusSubresource(model, provider).Build()
	r := &ModelReconciler{Client: cl, HTTPClient: f.srv.Client()}

	key := types.NamespacedName{Name: model.Name, Namespace: model.Namespace}
	// The first pass of a fitting Model only adds the finalizer; the second reports status.
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	got := &aiv1alpha1.Model{}
	if err := cl.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get model: %v", err)
	}
	return got, f
}

// A huggingFace source on an ollama provider is reported on the Model and nothing
// is pulled, no finalizer is added, and the operator does not fall back to spec.model.
func TestReconcile_SourceMismatchReportedAndNothingPulled(t *testing.T) {
	got, f := reconcileWithSource(t, hfSource(sourceTestRevision))

	cond := meta.FindStatusCondition(got.Status.Conditions, conditionTypeSourceCompatible)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonSourceEngineMismatch {
		t.Fatalf("SourceCompatible condition = %+v, want False/%s", cond, reasonSourceEngineMismatch)
	}
	if cond.Message == "" || got.Status.Message != cond.Message {
		t.Errorf("message %q should be set and match the status message %q", cond.Message, got.Status.Message)
	}
	if got.Status.State != stateError || got.Status.Ready {
		t.Errorf("state=%q ready=%v, want Error and not ready", got.Status.State, got.Status.Ready)
	}
	if len(got.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none: nothing was pulled", got.Finalizers)
	}
	if n := atomic.LoadInt32(&f.pulls); n != 0 {
		t.Errorf("pulls = %d, want 0", n)
	}
}

// A source that fits is reported as compatible.
func TestReconcile_FittingSourceReportsCompatible(t *testing.T) {
	got, _ := reconcileWithSource(t, ollamaSource())

	cond := meta.FindStatusCondition(got.Status.Conditions, conditionTypeSourceCompatible)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonSourceFitsEngine {
		t.Errorf("SourceCompatible condition = %+v, want True/%s", cond, reasonSourceFitsEngine)
	}
}

// pulledNames records the model name of every /api/pull request.
type pulledNames struct {
	mu    sync.Mutex
	names []string
}

func (p *pulledNames) record(w http.ResponseWriter, r *http.Request) {
	var req OllamaPullRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	p.mu.Lock()
	p.names = append(p.names, req.Name)
	p.mu.Unlock()
	emit(w, `{"status":"success"}`)
}

func (p *pulledNames) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.names...)
}

func TestPull_UsesSourceTagWhenSetElseModel(t *testing.T) {
	cases := map[string]struct {
		src  *aiv1alpha1.ModelSource
		want string
	}{
		"source.ollama.tag set": {&aiv1alpha1.ModelSource{Ollama: &aiv1alpha1.OllamaSource{Tag: otherPullTag}}, otherPullTag},
		"source absent":         {nil, testModelID},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pulled := &pulledNames{}
			h := newPullHarness(t, newFakeOllama(t, pulled.record), 0)
			h.model.Spec.Source = tc.src
			h.reconcile()
			eventually(t, "the pull request", func() bool { return len(pulled.snapshot()) == 1 })
			if got := pulled.snapshot()[0]; got != tc.want {
				t.Errorf("pulled %q, want %q", got, tc.want)
			}
			if h.r.tracker().get(pullKey(h.model.Spec.ProviderRef, tc.want)) == nil {
				t.Errorf("pull is not tracked under the tag %q", tc.want)
			}
		})
	}
}

// The mismatch is knowable from the spec, so it is reported even while the provider
// is not Ready yet.
func TestReconcile_SourceMismatchReportedBeforeProviderReady(t *testing.T) {
	model, provider := absentOllamaModel("http://unused.invalid")
	model.Spec.Source = hfSource(sourceTestRevision)
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).
		WithObjects(model, provider).WithStatusSubresource(model, provider).Build()
	r := &ModelReconciler{Client: cl}

	key := types.NamespacedName{Name: model.Name, Namespace: model.Namespace}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &aiv1alpha1.Model{}
	if err := cl.Get(context.Background(), key, got); err != nil {
		t.Fatalf("get model: %v", err)
	}
	if got.Status.State != stateError {
		t.Errorf("state = %q, want Error rather than Pending", got.Status.State)
	}
}
