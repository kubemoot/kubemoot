package liaison

import (
	"context"
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func crew(name, ns string, discussion *kubemootv1alpha1.DiscussionConfig, ready bool) *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       kubemootv1alpha1.CrewSpec{Description: name + " crew", Discussion: discussion},
		Status:     kubemootv1alpha1.CrewStatus{Ready: ready},
	}
}

func TestK8sListerFiltersGatewayless(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		crew("hello", "hello", nil, true),
		crew("pilot", "pilot", &kubemootv1alpha1.DiscussionConfig{Enabled: true}, false),
		crew("silent", "silent", &kubemootv1alpha1.DiscussionConfig{Enabled: false}, true),
	).Build()

	crews, err := NewLister(c).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Crew{}
	for _, cr := range crews {
		got[cr.Name] = cr
	}
	if len(got) != 2 || got["hello"].Description != "hello crew" || !got["hello"].Ready || got["pilot"].Ready {
		t.Fatalf("listed %+v", got)
	}
	if _, ok := got["silent"]; ok {
		t.Fatal("crew without a gateway was listed")
	}
}

func TestFind(t *testing.T) {
	crews := []Crew{{Name: "a", Namespace: "x"}, {Name: "b", Namespace: "x"}, {Name: "b", Namespace: "y"}}
	if c, err := find(crews, "a", ""); err != nil || c.Namespace != "x" {
		t.Fatalf("unique: %v %+v", err, c)
	}
	if _, err := find(crews, "b", ""); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	if c, err := find(crews, "b", "y"); err != nil || c.Namespace != "y" {
		t.Fatalf("namespaced: %v %+v", err, c)
	}
	if _, err := find(crews, "zzz", "x"); err == nil {
		t.Fatal("unknown name accepted")
	}
}
