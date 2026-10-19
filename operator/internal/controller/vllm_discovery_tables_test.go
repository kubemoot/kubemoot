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
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	svcName   = "svc"
	oldServed = "old"
)

func TestServiceKeyDefaultsToTheProvidersNamespace(t *testing.T) {
	cases := map[string]struct {
		ref  aiv1alpha1.VLLMServiceRef
		want types.NamespacedName
	}{
		"no namespace":   {aiv1alpha1.VLLMServiceRef{Name: svcName}, types.NamespacedName{Namespace: testNS1, Name: svcName}},
		"own namespace":  {aiv1alpha1.VLLMServiceRef{Name: svcName, Namespace: modelsNamespace}, types.NamespacedName{Namespace: modelsNamespace, Name: svcName}},
		"explicit match": {aiv1alpha1.VLLMServiceRef{Name: svcName, Namespace: testNS1}, types.NamespacedName{Namespace: testNS1, Name: svcName}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := refProvider()
			p.Spec.ServiceRef = &tc.ref
			if got := serviceKey(p); got != tc.want {
				t.Errorf("serviceKey = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServedNamesOfListsTheNamesInOrder(t *testing.T) {
	cases := map[string]struct {
		in   []aiv1alpha1.VLLMServer
		want []string
	}{
		coreTestNone: {nil, []string{}},
		"one":        {[]aiv1alpha1.VLLMServer{{Model: "a"}}, []string{"a"}},
		"several":    {[]aiv1alpha1.VLLMServer{{Model: "b"}, {Model: "a"}, {Model: "c"}}, []string{"b", "a", "c"}},
		"an alias":   {[]aiv1alpha1.VLLMServer{{Model: "a", Endpoint: "x"}, {Model: "a2", Endpoint: "x"}}, []string{"a", "a2"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := servedNamesOf(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("servedNamesOf = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVLLMModelInfoComesFromTheSourceOnly(t *testing.T) {
	withQuantization := &aiv1alpha1.ModelSource{HuggingFace: &aiv1alpha1.HuggingFaceSource{Repo: "r", Revision: "abcdef1", Quantization: vllmQuantization}}
	cases := map[string]struct {
		source *aiv1alpha1.ModelSource
		want   *aiv1alpha1.ModelInfo
	}{
		"no source":                 {nil, nil},
		"an empty source":           {&aiv1alpha1.ModelSource{}, nil},
		"an ollama source":          {&aiv1alpha1.ModelSource{Ollama: &aiv1alpha1.OllamaSource{Tag: "t"}}, nil},
		"huggingFace, no quantizer": {&aiv1alpha1.ModelSource{HuggingFace: &aiv1alpha1.HuggingFaceSource{Repo: "r", Revision: "abcdef1"}}, nil},
		"huggingFace with one":      {withQuantization, &aiv1alpha1.ModelInfo{Quantization: vllmQuantization}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := &aiv1alpha1.Model{Spec: aiv1alpha1.ModelSpec{Source: tc.source}}
			if got := vllmModelInfo(m); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("vllmModelInfo = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// aliasServer is a vLLM server that lists several ids.
func aliasServer(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := ""
		for i, id := range ids {
			if i > 0 {
				body += ","
			}
			body += `{"id":"` + id + `"}`
		}
		_, _ = w.Write([]byte(`{"data":[` + body + `]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverVLLMServersTable(t *testing.T) {
	udp := corev1.ServicePort{Name: "metrics", Port: 9000, Protocol: corev1.ProtocolUDP}
	tcp := func(port int32) corev1.ServicePort {
		return corev1.ServicePort{Name: "p", Port: port, Protocol: corev1.ProtocolTCP}
	}
	cases := map[string]struct {
		ports  []corev1.ServicePort
		roots  func(t *testing.T) servedRoots
		before []aiv1alpha1.VLLMServer
		want   []aiv1alpha1.VLLMServer
	}{
		"no ports": {nil, func(*testing.T) servedRoots { return servedRoots{} }, nil, nil},
		"a UDP port is not asked": {
			[]corev1.ServicePort{udp, tcp(8000)},
			func(t *testing.T) servedRoots { return servedRoots{8000: aliasServer(t, "a")} },
			nil,
			[]aiv1alpha1.VLLMServer{{Model: "a", Endpoint: refEndpointFor(8000)}},
		},
		"every served name of a port is kept": {
			[]corev1.ServicePort{tcp(8000)},
			func(t *testing.T) servedRoots { return servedRoots{8000: aliasServer(t, "a", "a-alias")} },
			nil,
			[]aiv1alpha1.VLLMServer{{Model: "a", Endpoint: refEndpointFor(8000)}, {Model: "a-alias", Endpoint: refEndpointFor(8000)}},
		},
		"a port with an empty list keeps what it had": {
			[]corev1.ServicePort{tcp(8000)},
			func(t *testing.T) servedRoots { return servedRoots{8000: aliasServer(t)} },
			[]aiv1alpha1.VLLMServer{{Model: oldServed, Endpoint: refEndpointFor(8000)}},
			[]aiv1alpha1.VLLMServer{{Model: oldServed, Endpoint: refEndpointFor(8000)}},
		},
		"a port that answers replaces what it had": {
			[]corev1.ServicePort{tcp(8000)},
			func(t *testing.T) servedRoots { return servedRoots{8000: aliasServer(t, "new")} },
			[]aiv1alpha1.VLLMServer{{Model: oldServed, Endpoint: refEndpointFor(8000)}},
			[]aiv1alpha1.VLLMServer{{Model: "new", Endpoint: refEndpointFor(8000)}},
		},
		"a name an earlier port serves is skipped": {
			[]corev1.ServicePort{tcp(8000), tcp(8001)},
			func(t *testing.T) servedRoots {
				return servedRoots{8000: aliasServer(t, "a"), 8001: aliasServer(t, "a", "b")}
			},
			nil,
			[]aiv1alpha1.VLLMServer{{Model: "a", Endpoint: refEndpointFor(8000)}, {Model: "b", Endpoint: refEndpointFor(8001)}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := refService1()
			svc.Spec.Ports = tc.ports
			cli := fake.NewClientBuilder().WithScheme(refScheme(t)).WithObjects(svc).Build()
			p := refProvider()
			p.Status.VLLMServers = tc.before
			got, _, err := discoverVLLMServers(context.Background(), cli, tc.roots(t).client(), p)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
				t.Errorf("servers = %v, want %v", got, tc.want)
			}
		})
	}
}

// A Service that cannot be read right now has not lost its servers.
func TestServiceReadErrorKeepsTheServersFoundBefore(t *testing.T) {
	p := refProvider()
	known := []aiv1alpha1.VLLMServer{{Model: vllmModel14, Endpoint: refEndpointFor(8000)}}
	p.Status.VLLMServers = known
	scheme := refScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).Build() // no Service
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache(), HTTPClient: servedRoots{}.client()}
	key := types.NamespacedName{Name: p.Name, Namespace: p.Namespace}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	got := &aiv1alpha1.ModelProvider{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseFailed || !strings.Contains(got.Status.Message, "reading Service") {
		t.Errorf("status = %q %q", got.Status.Phase, got.Status.Message)
	}
	if !reflect.DeepEqual(got.Status.VLLMServers, known) {
		t.Errorf("status servers = %v, want the ones found before: %v", got.Status.VLLMServers, known)
	}
}

func TestPortsOfAnotherNamespaceServiceAreReadFromThatNamespace(t *testing.T) {
	p := refProvider()
	p.Spec.ServiceRef.Namespace = modelsNamespace
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: refService, Namespace: modelsNamespace},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8000}}},
	}
	cli := fake.NewClientBuilder().WithScheme(refScheme(t)).WithObjects(svc).Build()
	got, _, err := discoverVLLMServers(context.Background(), cli, http.DefaultClient, p)
	if err != nil || len(got) != 0 {
		t.Errorf("servers %v err %v: the Service is read, the unreachable port adds nothing", got, err)
	}
}
