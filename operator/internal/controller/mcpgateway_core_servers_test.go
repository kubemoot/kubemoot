/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	coreTestOperatorNS = "kubemoot"
	coreTestCrewNS     = "crew-a"
	coreTestTools      = "accelerator-mcp"
	coreTestToolsLabel = "kubemoot.ai/tools"
	coreTestOwn        = coreTestCrewNS + "/own-mcp"
	coreTestNone       = "none"
)

// notOfferedLabels mark an operator-namespace server that is not offered to crews.
func notOfferedLabels() map[string]string { return map[string]string{"app": "agent-tools"} }

// coreServer is a ready MCPServer in namespace with the given labels.
func coreServer(namespace, name string, ready bool, lbls map[string]string) *kubemootv1alpha1.MCPServer {
	mcp := &kubemootv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: lbls}}
	mcp.Status.Ready = ready
	if ready {
		mcp.Status.Endpoint = "http://" + name + "." + namespace + ":8080"
	}
	return mcp
}

func coreGateway(namespace, name string, own, core *metav1.LabelSelector) *kubemootv1alpha1.MCPGateway {
	return &kubemootv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       kubemootv1alpha1.MCPGatewaySpec{MCPServerSelector: own, CoreServerSelector: core},
	}
}

func coreReconciler(t *testing.T, objs ...client.Object) *MCPGatewayReconciler {
	t.Helper()
	t.Setenv("OPERATOR_NAMESPACE", coreTestOperatorNS)
	scheme := agentReconcileScheme(t)
	return &MCPGatewayReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(), Scheme: scheme}
}

func serverIDs(servers []kubemootv1alpha1.MCPServer) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Namespace+"/"+s.Name)
	}
	slices.Sort(out)
	return out
}

var (
	coreLabel     = map[string]string{labelCoreMCP: valueTrue, coreTestToolsLabel: "accelerators"}
	crewLabel     = map[string]string{labelCrew: "a"}
	everyCore     = &metav1.LabelSelector{}
	acceleratorOn = &metav1.LabelSelector{MatchLabels: map[string]string{coreTestToolsLabel: "accelerators"}}
	crewSelector  = &metav1.LabelSelector{MatchLabels: crewLabel}
)

func coreFixture() []client.Object {
	return []client.Object{
		coreServer(coreTestCrewNS, "own-mcp", true, crewLabel),
		coreServer(coreTestCrewNS, "other-crew-mcp", true, map[string]string{labelCrew: "b"}),
		coreServer(coreTestOperatorNS, coreTestTools, true, coreLabel),
		coreServer(coreTestOperatorNS, "github-mcp", true, notOfferedLabels()), // not offered
		coreServer(coreTestOperatorNS, "starting-mcp", false, coreLabel),
		coreServer("crew-b", "elsewhere-mcp", true, coreLabel), // core label outside the operator namespace
	}
}

func TestListMCPServers_CoreServers(t *testing.T) {
	cases := []struct {
		name    string
		gateway *kubemootv1alpha1.MCPGateway
		want    []string
	}{
		{"no core selector: own namespace only", coreGateway(coreTestCrewNS, "gw", crewSelector, nil),
			[]string{coreTestOwn}},
		{"no selectors at all: every ready server in its namespace, as before", coreGateway(coreTestCrewNS, "gw", nil, nil),
			[]string{coreTestCrewNS + "/other-crew-mcp", coreTestOwn}},
		{"empty core selector: every ready offered core server", coreGateway(coreTestCrewNS, "gw", crewSelector, everyCore),
			[]string{coreTestOwn, "kubemoot/" + coreTestTools}},
		{"core selector narrows the offer", coreGateway(coreTestCrewNS, "gw", crewSelector,
			&metav1.LabelSelector{MatchLabels: map[string]string{coreTestToolsLabel: coreTestNone}}),
			[]string{coreTestOwn}},
		{"matching core selector", coreGateway(coreTestCrewNS, "gw", crewSelector, acceleratorOn),
			[]string{coreTestOwn, "kubemoot/" + coreTestTools}},
		{"a gateway in the operator namespace lists its own namespace only", coreGateway(coreTestOperatorNS, "gw", nil, everyCore),
			[]string{"kubemoot/" + coreTestTools, "kubemoot/github-mcp"}},
	}
	for _, c := range cases {
		r := coreReconciler(t, coreFixture()...)
		got, err := r.listMCPServers(context.Background(), c.gateway)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ids := serverIDs(got); !slices.Equal(ids, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, ids, c.want)
		}
	}
}

// A crew's own server with the same name as a core server takes precedence.
func TestListMCPServers_OwnServerShadowsCore(t *testing.T) {
	objs := append(coreFixture(), coreServer(coreTestCrewNS, coreTestTools, true, crewLabel))
	r := coreReconciler(t, objs...)
	got, err := r.listMCPServers(context.Background(), coreGateway(coreTestCrewNS, "gw", crewSelector, everyCore))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"crew-a/" + coreTestTools, coreTestOwn}
	if ids := serverIDs(got); !slices.Equal(ids, want) {
		t.Errorf("got %v, want %v", ids, want)
	}
}

func TestListMCPServers_InvalidCoreSelector(t *testing.T) {
	r := coreReconciler(t, coreFixture()...)
	bad := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "k", Operator: "Bogus"}}}
	if _, err := r.listMCPServers(context.Background(), coreGateway(coreTestCrewNS, "gw", nil, bad)); err == nil {
		t.Error("an invalid core selector should be an error")
	}
}

func requestIDs(t *testing.T, r *MCPGatewayReconciler, mcp *kubemootv1alpha1.MCPServer) []string {
	t.Helper()
	reqs := r.findGatewaysForMCPServer(context.Background(), mcp)
	out := make([]string, 0, len(reqs))
	for _, req := range reqs {
		out = append(out, req.Namespace+"/"+req.Name)
	}
	slices.Sort(out)
	return out
}

func TestFindGatewaysForMCPServer_CoreServers(t *testing.T) {
	gateways := []client.Object{
		coreGateway(coreTestCrewNS, "takes-core", crewSelector, acceleratorOn),
		coreGateway("crew-b", "no-core", nil, nil),
		coreGateway("crew-c", "other-core", nil, &metav1.LabelSelector{MatchLabels: map[string]string{coreTestToolsLabel: coreTestNone}}),
		coreGateway(coreTestOperatorNS, "operator-gw", nil, nil),
	}
	r := coreReconciler(t, gateways...)

	core := coreServer(coreTestOperatorNS, coreTestTools, true, coreLabel)
	if got, want := requestIDs(t, r, core), []string{"crew-a/takes-core", "kubemoot/operator-gw"}; !slices.Equal(got, want) {
		t.Errorf("core server: got %v, want %v", got, want)
	}
	notOffered := coreServer(coreTestOperatorNS, "github-mcp", true, notOfferedLabels())
	if got, want := requestIDs(t, r, notOffered), []string{"kubemoot/operator-gw"}; !slices.Equal(got, want) {
		t.Errorf("server not offered: got %v, want %v", got, want)
	}
	crewServer := coreServer(coreTestCrewNS, "own-mcp", true, crewLabel)
	if got, want := requestIDs(t, r, crewServer), []string{"crew-a/takes-core"}; !slices.Equal(got, want) {
		t.Errorf("crew server: got %v, want %v", got, want)
	}
	unmatched := coreServer(coreTestCrewNS, "x", true, map[string]string{labelCrew: "z"})
	if got := requestIDs(t, r, unmatched); len(got) != 0 {
		t.Errorf("a server no selector matches: got %v", got)
	}
}

// A core server that carries registry credentials is not offered to crews: its
// secret lives in the operator's namespace.
func TestListMCPServers_CoreServerWithCredentialsIsNotOffered(t *testing.T) {
	secretive := coreServer(coreTestOperatorNS, "secretive-mcp", true, coreLabel)
	secretive.Spec.Registry = &kubemootv1alpha1.RegistryConfig{AuthSecretRef: "token"}
	r := coreReconciler(t, append(coreFixture(), secretive)...)
	got, err := r.listMCPServers(context.Background(), coreGateway(coreTestCrewNS, "gw", crewSelector, everyCore))
	if err != nil {
		t.Fatal(err)
	}
	if ids := serverIDs(got); slices.Contains(ids, coreTestOperatorNS+"/secretive-mcp") {
		t.Errorf("a core server with credentials was offered: %v", ids)
	}
}

// recordingTransport answers every gateway admin call with 200 and records it.
type recordingTransport struct {
	mu    sync.Mutex
	calls []string
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.calls = append(rt.calls, req.Method+" "+req.URL.Path)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
}

// A server registered at one endpoint that now has another (a crew server shadowing
// a core server of the same name) is unregistered and registered again.
func TestSyncMCPServerRegistrations_EndpointChange(t *testing.T) {
	rt := &recordingTransport{}
	r := coreReconciler(t)
	r.HTTPClient = &http.Client{Transport: rt}
	gw := coreGateway(coreTestCrewNS, "gw", crewSelector, everyCore)
	gw.Status.MCPServers = []string{coreTestTools, "own-mcp"}
	gw.Status.MCPServerEndpoints = map[string]string{
		coreTestTools: "http://" + coreTestTools + "." + coreTestOperatorNS + ":8080",
		"own-mcp":     "http://own-mcp." + coreTestCrewNS + ":8080",
	}
	servers := []kubemootv1alpha1.MCPServer{
		*coreServer(coreTestCrewNS, coreTestTools, true, crewLabel), // now the crew's own
		*coreServer(coreTestCrewNS, "own-mcp", true, crewLabel),     // unchanged
	}
	got := r.syncMCPServerRegistrations(context.Background(), gw, servers)

	if got[coreTestTools] != servers[0].Status.Endpoint || got["own-mcp"] != servers[1].Status.Endpoint {
		t.Errorf("registered endpoints: %v", got)
	}
	deletes := 0
	for _, c := range rt.calls {
		if strings.HasPrefix(c, http.MethodDelete) {
			deletes++
			if !strings.HasSuffix(c, "/"+coreTestTools) {
				t.Errorf("unregistered a server whose endpoint did not change: %s", c)
			}
		}
	}
	if deletes != 1 {
		t.Errorf("want one unregister for the moved server, calls: %v", rt.calls)
	}
}

func TestGwWithMCPServers(t *testing.T) {
	var s kubemootv1alpha1.MCPGatewayStatus
	gwWithMCPServers(map[string]string{"b": "http://b", "a": "http://a"})(&s)
	if !slices.Equal(s.MCPServers, []string{"a", "b"}) || s.RegisteredServers != 2 || s.MCPServerEndpoints["a"] != "http://a" {
		t.Errorf("status: %+v", s)
	}
	gwWithMCPServers(nil)(&s)
	if s.MCPServers != nil || s.MCPServerEndpoints != nil || s.RegisteredServers != 0 {
		t.Errorf("empty registrations should clear the status: %+v", s)
	}
}
