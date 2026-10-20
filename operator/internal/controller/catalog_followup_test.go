package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

// A vLLM server that is restarting does not answer, but its model stays in the
// catalog: dropping it would roll every pod bound to it twice for a restart. A model
// that never answered is not present.
func TestVLLMFactsPresenceSticksThroughARestart(t *testing.T) {
	r, p, httpClient := vllmCatalogFixture(t, "")
	p.Status.Models = []aiv1alpha1.CatalogModel{{Name: candModel14, Present: true}}
	facts := r.vllmFacts(context.Background(), httpClient, p, engine.Snapshot{})
	if len(facts) != 1 || !facts[0].Present {
		t.Errorf("a model present before stays present while its server restarts: %+v", facts)
	}
	p.Status.Models = []aiv1alpha1.CatalogModel{{Name: candModel14, Present: false}}
	if facts := r.vllmFacts(context.Background(), httpClient, p, engine.Snapshot{}); facts[0].Present {
		t.Errorf("a model that never answered is not present: %+v", facts)
	}
	snap := engine.Snapshot{Available: []aiv1alpha1.AvailableModel{{Name: candModel14}}}
	if facts := r.vllmFacts(context.Background(), httpClient, p, snap); !facts[0].Present {
		t.Errorf("an answering model is present: %+v", facts)
	}
}

func TestModelPolicyRequestsReachThePoliciesOfTheNamespace(t *testing.T) {
	scheme := agentReconcileScheme(t)
	mk := func(ns, name string) *aiv1alpha1.CrewSchedulingPolicy {
		return &aiv1alpha1.CrewSchedulingPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mk(selNS, "a"), mk(selNS, "b"), mk("other", "c")).Build()
	own := candModel("own", candModel14, true, nil)
	own.Namespace = selNS
	reqs := modelPolicyRequests(context.Background(), cli, &own)
	if len(reqs) != 2 || reqs[0].Namespace != selNS || reqs[1].Namespace != selNS {
		t.Errorf("requests = %+v, want the two policies of the namespace", reqs)
	}
	if got := modelPolicyRequests(context.Background(), cli, &aiv1alpha1.Agent{}); got != nil {
		t.Errorf("a non-Model maps to %+v", got)
	}
	if got := modelPolicyRequests(context.Background(), failingLister{}, &own); got != nil {
		t.Errorf("a failed list maps to %+v", got)
	}
}

func TestCatalogModeOfFollowsTheModelsInTheNamespace(t *testing.T) {
	scheme := agentReconcileScheme(t)
	own := candModel("own", candModel14, true, nil)
	own.Namespace = selNS
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&own).Build()
	for ns, want := range map[string]string{selNS: catalogModeCrewLocal, "empty-ns": catalogModeCluster, selOpNS: catalogModeCluster} {
		got, err := catalogModeOf(context.Background(), cli, ns)
		if err != nil || got != want {
			t.Errorf("%s: mode %q err %v, want %q", ns, got, err, want)
		}
	}
	if catalogModeFor(nil) != catalogModeCluster || catalogModeFor([]aiv1alpha1.Model{own}) != catalogModeCrewLocal {
		t.Error("catalogModeFor decides the mode")
	}
}

func TestApplyCatalogConditionsLeavesThemWhenModelsCannotBeListed(t *testing.T) {
	policy := pilotPolicy()
	setCatalogConditions(policy, catalogModeCluster)
	before := len(policy.Status.Conditions)
	r := &CrewSchedulingPolicyReconciler{Client: failingLister{}}
	r.applyCatalogConditions(context.Background(), policy)
	if len(policy.Status.Conditions) != before {
		t.Errorf("conditions = %+v", policy.Status.Conditions)
	}
}

// serverOf looks a catalog Model's server up in the Model's own namespace, and a
// crew's own Model through the usual search, including when the namespaces agree.
func TestServerOfResolvesCatalogAndCrewLocalModels(t *testing.T) {
	scheme := agentReconcileScheme(t)
	inOp := opServer(selOllama, aiv1alpha1.ServerTypeOllama)
	inCrew := &aiv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: selCrewServer, Namespace: selNS}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inOp, inCrew).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme}
	ctx := context.Background()

	catalogModel := catalogEntryModel(inOp, catEntryOf(selModel8, aiv1alpha1.SizeSmall))
	if p, ok := r.serverOf(ctx, selNS, &catalogModel); !ok || p.Namespace != selOpNS {
		t.Errorf("a catalog Model resolves in its server's namespace: %v %v", p, ok)
	}
	crewModel := candModel("m", candModel14, true, nil)
	crewModel.Namespace = selNS
	crewModel.Spec.ServerRef = selCrewServer
	if p, ok := r.serverOf(ctx, selNS, &crewModel); !ok || p.Name != selCrewServer {
		t.Errorf("a crew's Model resolves through the usual search: %v %v", p, ok)
	}
	crewModel.Spec.ServerRef = selOllama
	if p, ok := r.serverOf(ctx, selNS, &crewModel); !ok || p.Namespace != selOpNS {
		t.Errorf("a crew's Model may name an operator-namespace server: %v %v", p, ok)
	}
	missing := catalogEntryModel(inOp, catEntryOf("x", ""))
	missing.Spec.ServerRef = rjAbsent
	if _, ok := r.serverOf(ctx, selNS, &missing); ok {
		t.Error("an absent server must not resolve")
	}
}

// The operator's namespace comes from OPERATOR_NAMESPACE for the catalog and for the
// server search alike.
func TestResolveServerHonorsTheOperatorNamespace(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", "custom-op")
	scheme := agentReconcileScheme(t)
	p := opServer(selOllama, aiv1alpha1.ServerTypeOllama)
	p.Namespace = "custom-op"
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).Build(), Scheme: scheme}
	if got, ok := r.resolveServer(context.Background(), selNS, selOllama); !ok || got.Namespace != "custom-op" {
		t.Errorf("server = %v %v", got, ok)
	}
}
