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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	refService  = "vllm-gpu"
	refRepo14   = "Qwen/Qwen3-14B-AWQ"
	refHostFmt  = "vllm-gpu.ns1.svc:%d"
	refEndpoint = "http://vllm-gpu.ns1.svc:%d"

	vllmModelsPath  = "/v1/models"
	stateAsleepName = "asleep"
	modelsNamespace = "models"
)

// servedRoots maps a Service port to the fake server behind it. The operator reaches
// a server at http://<service>.<namespace>.svc:<port>, which the tests answer from
// local servers by rewriting the address.
type servedRoots map[int32]*httptest.Server

func (m servedRoots) RoundTrip(req *http.Request) (*http.Response, error) {
	for port, srv := range m {
		if req.URL.Host == fmt.Sprintf(refHostFmt, port) {
			target, err := url.Parse(srv.URL)
			if err != nil {
				return nil, err
			}
			clone := req.Clone(req.Context())
			clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
			return http.DefaultTransport.RoundTrip(clone)
		}
	}
	return nil, fmt.Errorf("no fake server for %s", req.URL.Host)
}

func (m servedRoots) client() *http.Client { return &http.Client{Transport: m} }

// vllmServing is a vLLM server that serves one model loaded from root ("" omits it).
func vllmServing(t *testing.T, served, root string, asleep bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case healthPath:
		case vllmModelsPath:
			rootField := ""
			if root != "" {
				rootField = fmt.Sprintf(`,"root":%q`, root)
			}
			_, _ = fmt.Fprintf(w, `{"data":[{"id":%q%s}]}`, served, rootField)
		case "/is_sleeping":
			_, _ = fmt.Fprintf(w, `{"is_sleeping":%t}`, asleep)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func refScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := aiv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func refService1(ports ...int32) *corev1.Service {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: refService, Namespace: testNS1}}
	for _, p := range ports {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: fmt.Sprintf("s%d", p), Port: p})
	}
	return svc
}

func refServer() *aiv1alpha1.ModelServer {
	return &aiv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: vllmServerName, Namespace: testNS1},
		Spec: aiv1alpha1.ModelServerSpec{
			Type:       aiv1alpha1.ServerTypeVLLM,
			ServiceRef: &aiv1alpha1.VLLMServiceRef{Name: refService},
		},
	}
}

func refEndpointFor(port int32) string { return fmt.Sprintf(refEndpoint, port) }

func TestDiscoverReadsEachPortsServedNameInPortOrder(t *testing.T) {
	roots := servedRoots{
		8000: vllmServing(t, vllmModel14, refRepo14, false),
		8001: vllmServing(t, vllmModel8, "", true),
	}
	// The Service lists its ports out of order.
	cli := fake.NewClientBuilder().WithScheme(refScheme(t)).WithObjects(refService1(8001, 8000)).Build()
	servers, skipped, err := discoverVLLMServers(context.Background(), cli, roots.client(), refServer())
	if err != nil || len(skipped) != 0 {
		t.Fatalf("err %v skipped %v", err, skipped)
	}
	want := []aiv1alpha1.VLLMServer{{Model: vllmModel14, Endpoint: refEndpointFor(8000)}, {Model: vllmModel8, Endpoint: refEndpointFor(8001)}}
	if len(servers) != 2 || servers[0] != want[0] || servers[1] != want[1] {
		t.Errorf("servers = %v, want %v", servers, want)
	}
}

func TestDiscoverSkipsPortsThatDoNotAnswerOrRepeatAName(t *testing.T) {
	roots := servedRoots{
		8000: vllmServing(t, vllmModel14, "", false),
		8002: vllmServing(t, vllmModel14, "", false), // the same name again
	}
	cli := fake.NewClientBuilder().WithScheme(refScheme(t)).WithObjects(refService1(8000, 8001, 8002)).Build()
	servers, skipped, err := discoverVLLMServers(context.Background(), cli, roots.client(), refServer())
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Endpoint != refEndpointFor(8000) {
		t.Errorf("servers = %v, want only the first port", servers)
	}
	if len(skipped) != 2 || !strings.Contains(skipped[0], "port 8001") || !strings.Contains(skipped[1], "an earlier port serves") {
		t.Errorf("skipped = %v", skipped)
	}
}

// A server that restarts does not answer for a while; its Model must not read as a
// misnamed one, so the server keeps what it found at that port before.
func TestDiscoverKeepsTheServerOfAPortThatStopsAnswering(t *testing.T) {
	roots := servedRoots{8000: vllmServing(t, vllmModel14, "", false)} // 8001 is down
	p := refServer()
	p.Status.VLLMServers = []aiv1alpha1.VLLMServer{
		{Model: vllmModel14, Endpoint: refEndpointFor(8000)},
		{Model: vllmModel8, Endpoint: refEndpointFor(8001)},
	}
	cli := fake.NewClientBuilder().WithScheme(refScheme(t)).WithObjects(refService1(8000, 8001)).Build()
	servers, notes, err := discoverVLLMServers(context.Background(), cli, roots.client(), p)
	if err != nil || len(servers) != 2 || servers[1].Model != vllmModel8 {
		t.Fatalf("servers %v err %v, want the known server at 8001 kept", servers, err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "keeping "+vllmModel8) {
		t.Errorf("notes = %v", notes)
	}

	// A port the Service no longer has is gone, not kept.
	cli = fake.NewClientBuilder().WithScheme(refScheme(t)).WithObjects(refService1(8000)).Build()
	servers, _, _ = discoverVLLMServers(context.Background(), cli, roots.client(), p)
	if len(servers) != 1 || servers[0].Model != vllmModel14 {
		t.Errorf("servers = %v, want only the port the Service still has", servers)
	}
}

func TestDiscoverUsesTheNamespaceOfTheRef(t *testing.T) {
	p := refServer()
	p.Spec.ServiceRef.Namespace = modelsNamespace
	svc := refService1(8000)
	svc.Namespace = modelsNamespace
	cli := fake.NewClientBuilder().WithScheme(refScheme(t)).WithObjects(svc).Build()
	servers, _, err := discoverVLLMServers(context.Background(), cli, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "vllm-gpu.models.svc:8000" {
			t.Errorf("asked %s", r.URL.Host)
		}
		return nil, fmt.Errorf("down")
	})}, p)
	if err != nil || len(servers) != 0 {
		t.Errorf("servers %v err %v", servers, err)
	}
}

func TestDiscoverFailsWhenTheServiceIsMissing(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(refScheme(t)).Build()
	if _, _, err := discoverVLLMServers(context.Background(), cli, http.DefaultClient, refServer()); err == nil {
		t.Error("a missing Service must be an error")
	}
}

// reconcileRef reconciles the serviceRef server over a Service and fake servers.
func reconcileRef(t *testing.T, roots servedRoots, ports ...int32) (*aiv1alpha1.ModelServer, *ModelServerReconciler) {
	t.Helper()
	p := refServer()
	objs := []client.Object{p}
	if len(ports) > 0 {
		objs = append(objs, refService1(ports...))
	}
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(p).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache(), HTTPClient: roots.client()}
	key := types.NamespacedName{Name: p.Name, Namespace: p.Namespace}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &aiv1alpha1.ModelServer{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	return got, r
}

func TestServiceRefServerIsReadyAndPublishesTheDiscoveredEndpoints(t *testing.T) {
	roots := servedRoots{8000: vllmServing(t, vllmModel14, "", false), 8001: vllmServing(t, vllmModel8, "", true)}
	p, r := reconcileRef(t, roots, 8000, 8001)

	if !p.Status.Ready || p.Status.Message != "2 of 2 vLLM servers answering" {
		t.Errorf("status = ready %v %q", p.Status.Ready, p.Status.Message)
	}
	if len(p.Status.VLLMServers) != 2 || p.EndpointFor(vllmModel8) != refEndpointFor(8001) {
		t.Errorf("status servers = %v", p.Status.VLLMServers)
	}
	state := publishedState(t, p, r)
	if state.ModelEndpoints[vllmModel14] != refEndpointFor(8000) || state.ModelStates[vllmModel8] != stateAsleepName {
		t.Errorf("published state = %+v", state)
	}
}

func TestServiceRefServerIsPendingWhileNoPortReportsAModel(t *testing.T) {
	p, _ := reconcileRef(t, servedRoots{}, 8000)
	if p.Status.Ready || p.Status.Phase != statePending || !strings.Contains(p.Status.Message, "reports a model yet") {
		t.Errorf("status = ready %v %q %q", p.Status.Ready, p.Status.Phase, p.Status.Message)
	}
}

func TestServiceRefServerFailsWithoutItsService(t *testing.T) {
	p, _ := reconcileRef(t, servedRoots{})
	if p.Status.Ready || p.Status.Phase != phaseFailed || !strings.Contains(p.Status.Message, "reading Service") {
		t.Errorf("status = ready %v %q %q", p.Status.Ready, p.Status.Phase, p.Status.Message)
	}
}

func TestServicesMapToTheServersThatNameThem(t *testing.T) {
	other := refServer()
	other.Name = testMismatchName
	other.Spec.ServiceRef.Name = "another-service"
	servers := vllmServer(server(vllmModel14, vllmEndpoint14))
	servers.Name = "listed"
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(refServer(), other, servers).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme}
	got := r.servicesToServers(context.Background(), refService1(8000))
	if len(got) != 1 || got[0].Name != vllmServerName {
		t.Errorf("requests = %v, want only the server that names the Service", got)
	}
}

// refModel is a Model on the serviceRef server; repo "" leaves source out.
func refModel(served, repo string) *aiv1alpha1.Model {
	m := &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: testNS1},
		Spec:       aiv1alpha1.ModelSpec{ServerRef: vllmServerName, Model: served},
	}
	if repo != "" {
		m.Spec.Source = &aiv1alpha1.ModelSource{HuggingFace: &aiv1alpha1.HuggingFaceSource{Repo: repo, Revision: vllmRevision}}
	}
	return m
}

// reconcileRefModel reconciles model against a discovered server.
func reconcileRefModel(t *testing.T, model *aiv1alpha1.Model, roots servedRoots, ports ...int32) *aiv1alpha1.Model {
	t.Helper()
	server, _ := reconcileRef(t, roots, ports...)
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model).WithStatusSubresource(model).Build()
	r := &ModelReconciler{Client: cli, Scheme: scheme, HTTPClient: roots.client()}
	if _, err := r.reconcileVLLMModel(context.Background(), model, server); err != nil {
		t.Fatal(err)
	}
	return model
}

func condition(m *aiv1alpha1.Model, typ string) *metav1.Condition {
	return meta.FindStatusCondition(m.Status.Conditions, typ)
}

func TestModelWithoutASourceIsAvailableWhenItsServedNameIsReported(t *testing.T) {
	roots := servedRoots{8000: vllmServing(t, vllmModel14, "", true)}
	m := reconcileRefModel(t, refModel(vllmModel14, ""), roots, 8000)
	if m.Status.State != stateAvailable || !m.Status.Ready || m.Status.Endpoint != refEndpointFor(8000) {
		t.Errorf("status = %+v", m.Status)
	}
	if c := condition(m, "ServedNameFound"); c == nil || c.Status != metav1.ConditionTrue || c.Reason != "ServedNameReported" {
		t.Errorf("ServedNameFound = %+v", c)
	}
	if condition(m, "ServedRepoMatches") != nil {
		t.Error("a Model without a source has no repository to compare")
	}
}

func TestModelWhoseNameNoServerReportsIsAnErrorNamingTheReportedNames(t *testing.T) {
	roots := servedRoots{8000: vllmServing(t, vllmModel14, "", false)}
	m := reconcileRefModel(t, refModel("qwen3:14B", ""), roots, 8000)
	if m.Status.State != stateError || m.Status.Ready {
		t.Errorf("state %q ready %v, want Error", m.Status.State, m.Status.Ready)
	}
	c := condition(m, "ServedNameFound")
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != "ServedNameNotReported" || !strings.Contains(c.Message, vllmModel14) {
		t.Errorf("ServedNameFound = %+v", c)
	}
}

func TestModelWaitsWhileTheServerHasNoServers(t *testing.T) {
	m := reconcileRefModel(t, refModel(vllmModel14, ""), servedRoots{}, 8000)
	if m.Status.State != statePending || condition(m, "ServedNameFound") != nil {
		t.Errorf("state %q, want Pending with no verdict on the name yet", m.Status.State)
	}
}

func TestSourceRepoIsComparedWithTheRepoTheServerReports(t *testing.T) {
	cases := map[string]struct {
		root, repo string
		want       metav1.ConditionStatus
	}{
		"same repo":                 {refRepo14, refRepo14, metav1.ConditionTrue},
		"case differs only":         {"qwen/qwen3-14b-awq", refRepo14, metav1.ConditionTrue},
		"a different repo":          {"Qwen/Qwen3-14B", refRepo14, metav1.ConditionFalse},
		"the server does not say":   {"", refRepo14, ""},
		"no source on the Model":    {refRepo14, "", ""},
		"a local path is a differ.": {"/models/x", refRepo14, metav1.ConditionFalse},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			roots := servedRoots{8000: vllmServing(t, vllmModel14, tc.root, false)}
			m := reconcileRefModel(t, refModel(vllmModel14, tc.repo), roots, 8000)
			if m.Status.State != stateLoaded {
				t.Errorf("state = %q: the source is information and never fails the Model", m.Status.State)
			}
			c := condition(m, "ServedRepoMatches")
			switch {
			case tc.want == "" && c != nil:
				t.Errorf("ServedRepoMatches = %+v, want none", c)
			case tc.want != "" && (c == nil || c.Status != tc.want):
				t.Errorf("ServedRepoMatches = %+v, want %s", c, tc.want)
			}
		})
	}
}

func TestVLLMModelWithoutASourceFitsAVLLMServerButAnOllamaSourceDoesNot(t *testing.T) {
	if msg := sourceMismatch(refModel(vllmModel14, ""), aiv1alpha1.ServerTypeVLLM); msg != "" {
		t.Errorf("no source on vllm = %q", msg)
	}
	m := refModel(vllmModel14, "")
	m.Spec.Source = &aiv1alpha1.ModelSource{Ollama: &aiv1alpha1.OllamaSource{Tag: vllmModel14}}
	if msg := sourceMismatch(m, aiv1alpha1.ServerTypeVLLM); msg == "" {
		t.Error("an ollama source on vllm must still be a mismatch")
	}
}
