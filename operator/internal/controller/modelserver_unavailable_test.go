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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const unavailablePod = "vllm-0"

// refSlice is the EndpointSlice of the serviceRef Service with one endpoint, ready or not.
func refSlice(ready bool) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: refService + "-abc", Namespace: testNS1,
			Labels: map[string]string{discoveryv1.LabelServiceName: refService},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
	}
}

func readyCondition(t *testing.T, p *aiv1alpha1.ModelServer) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(p.Status.Conditions, conditionTypeReady)
	if c == nil {
		t.Fatal("no Ready condition")
	}
	return c
}

const (
	unavailableNode = "gpu-node"
	unavailableApp  = "vllm-app"
)

// unavailableObjects is a Service whose only pod is Pending on a node that is not ready.
func unavailableObjects() []client.Object {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: unavailablePod, Namespace: testNS1, Labels: map[string]string{"app": unavailableApp}},
		Spec:       corev1.PodSpec{NodeName: unavailableNode},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: unavailableNode},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}},
	}
	svc := refService1(8000)
	svc.Spec.Selector = map[string]string{"app": unavailableApp}
	return []client.Object{svc, pod, node, refSlice(false)}
}

// reconcileOnce reconciles the server over objs and returns what it wrote.
func reconcileOnce(t *testing.T, p *aiv1alpha1.ModelServer, objs ...client.Object) *aiv1alpha1.ModelServer {
	t.Helper()
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append([]client.Object{p}, objs...)...).WithStatusSubresource(p).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache(), HTTPClient: servedRoots{}.client()}
	key := types.NamespacedName{Name: p.Name, Namespace: p.Namespace}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	got := &aiv1alpha1.ModelServer{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestServiceWithoutReadyEndpointsIsUnavailable(t *testing.T) {
	got := reconcileOnce(t, refServer(), unavailableObjects()...)
	if got.Status.Ready || got.Status.Phase != phaseUnavailable {
		t.Fatalf("ready %v phase %q, want Unavailable", got.Status.Ready, got.Status.Phase)
	}
	c := readyCondition(t, got)
	if c.Status != metav1.ConditionFalse || c.Reason != reasonNoReadyEndpoints {
		t.Errorf("condition = %+v", c)
	}
	for _, want := range []string{"no ready endpoints", unavailablePod + " is Pending", unavailableNode + ", which is not ready"} {
		if !strings.Contains(got.Status.Message, want) {
			t.Errorf("message %q lacks %q", got.Status.Message, want)
		}
	}
}

func TestUnavailableServerDropsLiveCapacityAndKeepsCalibration(t *testing.T) {
	p := refServer()
	p.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{
		AcceleratorModel: "accel", AgentCount: 3, MullingAgentCount: 2, TriageAgentCount: 1,
		LoadedModels: []aiv1alpha1.LoadedModel{{Name: vllmModel14}},
	}
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: "Calibrated"}
	got := reconcileOnce(t, p, unavailableObjects()...)

	// The scheduler reads only the published state: not ready and nothing loaded.
	if state := buildServerState(got); state.Ready || len(state.LoadedModels) != 0 {
		t.Errorf("published state = ready %v loaded %v", state.Ready, state.LoadedModels)
	}
	c := got.Status.Capacity
	if len(c.LoadedModels) != 0 || c.AgentCount != 0 || c.MullingAgentCount != 0 || c.TriageAgentCount != 0 {
		t.Errorf("live capacity kept: %+v", c)
	}
	if c.AcceleratorModel != "accel" || got.Status.Calibration == nil {
		t.Errorf("hardware facts and calibration must stay: %+v %+v", c, got.Status.Calibration)
	}
}

// unhealthyVLLM lists a model on /v1/models but fails /health.
func unhealthyVLLM(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == vllmModelsPath {
			_, _ = fmt.Fprintf(w, `{"data":[{"id":%q}]}`, vllmModel14)
			return
		}
		http.Error(w, "unhealthy", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReadyEndpointsWithAFailingProbeIsFailed(t *testing.T) {
	p, _ := reconcileRef(t, servedRoots{8000: unhealthyVLLM(t)}, 8000)
	if p.Status.Ready || p.Status.Phase != phaseFailed {
		t.Fatalf("ready %v phase %q (%s), want Failed", p.Status.Ready, p.Status.Phase, p.Status.Message)
	}
	if c := readyCondition(t, p); c.Reason != reasonProbeFailed {
		t.Errorf("reason = %q, want %s", c.Reason, reasonProbeFailed)
	}
}

func TestProbeFailureWithReadyEndpointsReportsProbeFailed(t *testing.T) {
	p := vllmServer(server(vllmModel14, "http://127.0.0.1:1"))
	p.Namespace = testNS1
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache(), HTTPClient: servedRoots{}.client()}
	key := client.ObjectKeyFromObject(p)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	got := &aiv1alpha1.ModelServer{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Ready || got.Status.Phase != phaseFailed {
		t.Fatalf("ready %v phase %q, want Failed", got.Status.Ready, got.Status.Phase)
	}
	if c := readyCondition(t, got); c.Reason != reasonProbeFailed {
		t.Errorf("reason = %q, want %s", c.Reason, reasonProbeFailed)
	}
}

func TestServerReturningToReadyRestoresCapacity(t *testing.T) {
	roots := servedRoots{8000: vllmServing(t, vllmModel14, "", false)}
	p := refServer()
	svc := refService1(8000)
	slice := refSlice(false)
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p, svc, slice).WithStatusSubresource(p).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache(), HTTPClient: roots.client()}
	key := client.ObjectKeyFromObject(p)
	reconcile := func() *aiv1alpha1.ModelServer {
		t.Helper()
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		got := &aiv1alpha1.ModelServer{}
		if err := cli.Get(context.Background(), key, got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := reconcile(); got.Status.Phase != phaseUnavailable {
		t.Fatalf("phase %q, want Unavailable", got.Status.Phase)
	}

	ready := true
	slice.Endpoints[0].Conditions.Ready = &ready
	if err := cli.Update(context.Background(), slice); err != nil {
		t.Fatal(err)
	}
	got := reconcile()
	if !got.Status.Ready || got.Status.Phase != phaseReady {
		t.Fatalf("ready %v phase %q (%s), want Ready", got.Status.Ready, got.Status.Phase, got.Status.Message)
	}
	if got.Status.Capacity == nil || len(got.Status.Capacity.AvailableModels) == 0 {
		t.Errorf("capacity not restored: %+v", got.Status.Capacity)
	}
}

func TestHasReadyEndpoint(t *testing.T) {
	notReady, ready := false, true
	ep := func(r *bool) discoveryv1.Endpoint {
		return discoveryv1.Endpoint{Conditions: discoveryv1.EndpointConditions{Ready: r}}
	}
	slice := func(eps ...discoveryv1.Endpoint) discoveryv1.EndpointSlice {
		return discoveryv1.EndpointSlice{Endpoints: eps}
	}
	cases := map[string]struct {
		slices []discoveryv1.EndpointSlice
		want   bool
	}{
		"none":                     {nil, false},
		"empty slice":              {[]discoveryv1.EndpointSlice{slice()}, false},
		"not ready":                {[]discoveryv1.EndpointSlice{slice(ep(&notReady))}, false},
		"ready":                    {[]discoveryv1.EndpointSlice{slice(ep(&ready))}, true},
		"readiness unstated":       {[]discoveryv1.EndpointSlice{slice(ep(nil))}, true},
		"one not ready, one ready": {[]discoveryv1.EndpointSlice{slice(ep(&notReady)), slice(ep(&ready))}, true},
	}
	for name, tc := range cases {
		if got := hasReadyEndpoint(tc.slices); got != tc.want {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}

func nodeWith(name string, unschedulable bool, conds ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
		Status:     corev1.NodeStatus{Conditions: conds},
	}
}

func podOn(name, node string, phase corev1.PodPhase, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS1, Labels: labels},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func TestNodeIsReady(t *testing.T) {
	cases := map[string]struct {
		node *corev1.Node
		want bool
	}{
		"ready":              {nodeWith("n", false, corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionTrue}), true},
		"not ready":          {nodeWith("n", false, corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionFalse}), false},
		"unknown":            {nodeWith("n", false, corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}), false},
		"no Ready condition": {nodeWith("n", false, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse}), false},
		"no conditions":      {nodeWith("n", false), false},
	}
	for name, tc := range cases {
		if got := nodeIsReady(tc.node); got != tc.want {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}

func TestDescribePodNamesAnUnreadyOrCordonedNode(t *testing.T) {
	readyNode := corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionTrue}
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		nodeWith("cordoned", true, readyNode),
		nodeWith("fine", false, readyNode),
		nodeWith("down", false, corev1.NodeCondition{Type: corev1.NodeReady, Status: corev1.ConditionFalse}),
	).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme}
	cases := map[string]struct {
		node string
		want string
	}{
		"unscheduled":   {"", "pod p is Pending"},
		"node missing":  {"gone", "pod p is Pending"},
		"healthy node":  {"fine", "pod p is Pending"},
		"cordoned node": {"cordoned", "pod p is Pending on node cordoned, which is cordoned"},
		"unready node":  {"down", "pod p is Pending on node down, which is not ready"},
	}
	for name, tc := range cases {
		if got := r.describePod(context.Background(), podOn("p", tc.node, corev1.PodPending, nil)); got != tc.want {
			t.Errorf("%s: got %q want %q", name, got, tc.want)
		}
	}
}

func TestBackingDetail(t *testing.T) {
	labels := map[string]string{"app": unavailableApp}
	svc := func(selector map[string]string) *corev1.Service {
		s := refService1(8000)
		s.Spec.Selector = selector
		return s
	}
	cases := map[string]struct {
		svc  *corev1.Service
		pods []client.Object
		want string
	}{
		"no selector":     {svc(nil), nil, ""},
		"no matching pod": {svc(labels), []client.Object{podOn("other", "", corev1.PodRunning, map[string]string{"app": "x"})}, "; no pod matches its selector"},
		"several, sorted": {svc(labels), []client.Object{
			podOn("b", "", corev1.PodRunning, labels), podOn("a", "", corev1.PodPending, labels),
		}, "; pod a is Pending; pod b is Running"},
	}
	scheme := refScheme(t)
	for name, tc := range cases {
		cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.pods...).Build()
		r := &ModelServerReconciler{Client: cli, Scheme: scheme}
		if got := r.backingDetail(context.Background(), tc.svc); got != tc.want {
			t.Errorf("%s: got %q want %q", name, got, tc.want)
		}
	}
}

func TestNoReadyEndpointsIsFalseWhenTheServiceOrSlicesCannotBeRead(t *testing.T) {
	scheme := refScheme(t)
	ctx := context.Background()

	missing := &ModelServerReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	if msg, none := missing.noReadyEndpoints(ctx, refServer()); none {
		t.Errorf("an unreadable Service is not a Service without endpoints: %q", msg)
	}

	listErr := fake.NewClientBuilder().WithScheme(scheme).WithObjects(refService1(8000)).
		WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*discoveryv1.EndpointSliceList); ok {
				return fmt.Errorf("list refused")
			}
			return c.List(ctx, list, opts...)
		}}).Build()
	r := &ModelServerReconciler{Client: listErr, Scheme: scheme}
	if msg, none := r.noReadyEndpoints(ctx, refServer()); none {
		t.Errorf("slices that cannot be listed are not missing endpoints: %q", msg)
	}

	servers := &ModelServerReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	srv := refServer()
	srv.Spec.ServiceRef = nil
	if _, none := servers.noReadyEndpoints(ctx, srv); none {
		t.Error("a server without serviceRef has no Service to check")
	}
}

func TestServerStillPendingOrMissingItsServiceDropsLiveCapacity(t *testing.T) {
	seeded := func() *aiv1alpha1.ModelServer {
		p := refServer()
		p.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{
			AgentCount: 2, LoadedModels: []aiv1alpha1.LoadedModel{{Name: vllmModel14}},
		}
		return p
	}
	cases := map[string]struct {
		objs  []client.Object
		phase string
	}{
		"pending, no port reports a model": {[]client.Object{refService1(8000), refSlice(true)}, statePending},
		"service missing":                  {nil, phaseFailed},
	}
	for name, tc := range cases {
		got := reconcileOnce(t, seeded(), tc.objs...)
		if got.Status.Phase != tc.phase {
			t.Fatalf("%s: phase %q, want %q", name, got.Status.Phase, tc.phase)
		}
		if c := got.Status.Capacity; c != nil && (len(c.LoadedModels) != 0 || c.AgentCount != 0) {
			t.Errorf("%s: live capacity kept: %+v", name, c)
		}
		if state := buildServerState(got); state.Ready || len(state.LoadedModels) != 0 {
			t.Errorf("%s: published state = ready %v loaded %v", name, state.Ready, state.LoadedModels)
		}
	}
}

func TestEndpointReadinessChangedPassesOnlyReadinessChanges(t *testing.T) {
	ready, notReady := true, false
	slice := func(r *bool, addr string) *discoveryv1.EndpointSlice {
		s := refSlice(true)
		s.Endpoints = []discoveryv1.Endpoint{{Addresses: []string{addr}, Conditions: discoveryv1.EndpointConditions{Ready: r}}}
		return s
	}
	p := endpointReadinessChanged()
	if !p.Update(event.UpdateEvent{ObjectOld: slice(&ready, "10.0.0.1"), ObjectNew: slice(&notReady, "10.0.0.1")}) {
		t.Error("a readiness change must pass")
	}
	if p.Update(event.UpdateEvent{ObjectOld: slice(&ready, "10.0.0.1"), ObjectNew: slice(&ready, "10.0.0.2")}) {
		t.Error("an address change is not a readiness change")
	}
	unlabeled := refSlice(true)
	unlabeled.Labels = nil
	if p.Create(event.CreateEvent{Object: unlabeled}) || !p.Create(event.CreateEvent{Object: refSlice(true)}) {
		t.Error("only slices of a Service pass")
	}
}

func TestEndpointSliceMapsToTheServerThatNamesItsService(t *testing.T) {
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(refServer()).Build()
	r := &ModelServerReconciler{Client: cli, Scheme: scheme}
	got := r.endpointSlicesToServers(context.Background(), refSlice(true))
	if len(got) != 1 || got[0].Name != vllmServerName {
		t.Errorf("requests = %v", got)
	}
	if got := r.endpointSlicesToServers(context.Background(), &discoveryv1.EndpointSlice{}); got != nil {
		t.Errorf("a slice of no Service maps to %v", got)
	}
}
