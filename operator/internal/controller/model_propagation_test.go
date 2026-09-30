package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func mkModel(name, ns string) *kubemootv1alpha1.Model {
	return &kubemootv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"latencyClass": "low"}},
		Spec:       kubemootv1alpha1.ModelSpec{Model: "qwen3.5:9b", ProviderRef: "ollama-gpu"},
	}
}

func TestModelChangeEnqueuesTheNamespacesAgents(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mkAgent("a1", "crew-ns", "demo"), mkAgent("a2", "crew-ns", "other"), mkAgent("b1", "elsewhere", "demo"),
	).Build()
	reqs := mapModelToAgentRequests(context.Background(), cli, mkModel("qwen3.5-9b", "crew-ns"))
	if len(reqs) != 2 {
		t.Fatalf("want both agents in crew-ns, got %v", reqs)
	}
	for _, r := range reqs {
		if r.Namespace != "crew-ns" {
			t.Fatalf("enqueued an agent from another namespace: %v", r)
		}
	}
	if got := mapModelToAgentRequests(context.Background(), cli, mkAgent("x", "crew-ns", "demo")); got != nil {
		t.Fatalf("a non-Model object maps to nothing, got %v", got)
	}
}

func TestModelBindingChangedPredicate(t *testing.T) {
	p := modelBindingChanged()
	base := mkModel("m", "ns")
	change := func(mut func(m *kubemootv1alpha1.Model)) bool {
		n := base.DeepCopy()
		mut(n)
		return p.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: n})
	}
	if change(func(*kubemootv1alpha1.Model) {}) {
		t.Error("an update that changes nothing relevant must not enqueue")
	}
	if !change(func(m *kubemootv1alpha1.Model) { m.Status.Ready = true }) {
		t.Error("a model becoming usable must enqueue")
	}
	if !change(func(m *kubemootv1alpha1.Model) { m.Labels["latencyClass"] = "high" }) {
		t.Error("a relabel must enqueue")
	}
	if !change(func(m *kubemootv1alpha1.Model) { m.Spec.ProviderRef = "ollama-rig1" }) {
		t.Error("a spec change must enqueue")
	}
	if !change(func(m *kubemootv1alpha1.Model) { now := metav1.Now(); m.DeletionTimestamp = &now }) {
		t.Error("the start of a deletion must enqueue")
	}
	if change(func(m *kubemootv1alpha1.Model) { m.Status.Endpoint = "http://x" }) {
		t.Error("status noise must not enqueue")
	}
	loaded := base.DeepCopy()
	loaded.Status.Ready, loaded.Status.State = true, "Loaded"
	available := loaded.DeepCopy()
	available.Status.State = "Available"
	if p.Update(event.UpdateEvent{ObjectOld: loaded, ObjectNew: available}) ||
		p.Update(event.UpdateEvent{ObjectOld: available, ObjectNew: loaded}) {
		t.Error("a model loading or unloading must not enqueue: it would roll agents mid-discussion")
	}
	if p.Update(event.UpdateEvent{ObjectOld: mkAgent("a", "ns", "c"), ObjectNew: mkAgent("a", "ns", "c")}) {
		t.Error("non-Model objects must not enqueue")
	}
}
