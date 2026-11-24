package coordinator

import (
	"context"
	"strings"
	"testing"
	"time"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func coordinatorAgent(name, crew, endpoint string, ready bool) *kubemootv1alpha1.Agent {
	a := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "ns",
		Labels: map[string]string{"kubemoot.ai/crew": crew, "kubemoot.ai/role": "coordinator"},
	}}
	a.Status.Endpoint = endpoint
	a.Status.Ready = ready
	return a
}

func resolverOver(t *testing.T, ttl time.Duration, objs ...client.Object) (*Resolver, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return NewResolver(c, ttl), c
}

func TestResolvePrefersAReadyCoordinator(t *testing.T) {
	r, _ := resolverOver(t, time.Minute,
		coordinatorAgent("a", "crew", "http://not-ready", false),
		coordinatorAgent("b", "crew", "http://ready", true))
	got, err := r.Resolve(context.Background(), "crew")
	if err != nil || got != "http://ready" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
}

func TestResolveFallsBackToAnUnreadyEndpoint(t *testing.T) {
	r, _ := resolverOver(t, time.Minute, coordinatorAgent("a", "crew", "http://only", false))
	got, err := r.Resolve(context.Background(), "crew")
	if err != nil || got != "http://only" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
}

func TestResolveRefusesACrewWithoutACoordinatorOrEndpoint(t *testing.T) {
	r, _ := resolverOver(t, time.Minute,
		coordinatorAgent("a", "no-endpoint", "", true),
		coordinatorAgent("b", "other", "http://other", true))
	if _, err := r.Resolve(context.Background(), "missing"); err == nil || !strings.Contains(err.Error(), "no coordinator found") {
		t.Fatalf("missing crew err = %v", err)
	}
	if _, err := r.Resolve(context.Background(), "no-endpoint"); err == nil || !strings.Contains(err.Error(), "no endpoint") {
		t.Fatalf("no endpoint err = %v", err)
	}
}

func TestResolveCachesUntilInvalidated(t *testing.T) {
	agent := coordinatorAgent("a", "crew", "http://first", true)
	r, c := resolverOver(t, time.Hour, agent)
	if got, _ := r.Resolve(context.Background(), "crew"); got != "http://first" {
		t.Fatalf("first = %q", got)
	}
	if err := c.Delete(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Resolve(context.Background(), "crew"); err != nil || got != "http://first" {
		t.Fatalf("cached = %q, %v", got, err)
	}
	r.Invalidate("crew")
	if _, err := r.Resolve(context.Background(), "crew"); err == nil {
		t.Fatal("after Invalidate the deleted coordinator must no longer resolve")
	}
}

func TestResolveRereadsAfterTheTTL(t *testing.T) {
	agent := coordinatorAgent("a", "crew", "http://first", true)
	r, c := resolverOver(t, time.Nanosecond, agent)
	if _, err := r.Resolve(context.Background(), "crew"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := c.Delete(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), "crew"); err == nil {
		t.Fatal("an expired entry must be read again")
	}
}
