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
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	adminTestNS         = "crew-a"
	adminTestGateway    = "gw"
	adminTestSecret     = "gw-admin-token"
	adminTestUID        = "uid-gw"
	adminTestOperatorNS = "kubemoot-ops"
	adminTestToolsMCP   = "tools-mcp"
)

func adminTestGW() *kubemootv1alpha1.MCPGateway {
	return &kubemootv1alpha1.MCPGateway{
		ObjectMeta: metav1.ObjectMeta{Name: adminTestGateway, Namespace: adminTestNS, UID: adminTestUID},
		Spec:       kubemootv1alpha1.MCPGatewaySpec{Implementation: kubemootv1alpha1.ImplementationKubemoot},
	}
}

func adminTestReconciler(t *testing.T, rt http.RoundTripper, objs ...client.Object) *MCPGatewayReconciler {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithObjects(objs...).Build()
	return &MCPGatewayReconciler{Client: cl, Scheme: gatewayScheme(t), ConfigCache: NewConfigCache(),
		HTTPClient: &http.Client{Transport: rt}}
}

func readAdminSecret(t *testing.T, r *MCPGatewayReconciler) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: adminTestNS, Name: adminTestSecret}, secret); err != nil {
		t.Fatalf("admin token secret: %v", err)
	}
	return secret
}

func TestAdminTokenSecretIsCreatedOnceAndReused(t *testing.T) {
	r := adminTestReconciler(t, nil)
	gw := adminTestGW()
	ctx := context.Background()

	if err := r.ensureAdminTokenSecret(ctx, gw); err != nil {
		t.Fatal(err)
	}
	first := readAdminSecret(t, r)
	token := string(first.Data[gatewayAdminTokenKey])
	if len(token) < 40 {
		t.Fatalf("token too short to be random: %q", token)
	}
	if len(first.OwnerReferences) != 1 || first.OwnerReferences[0].Name != adminTestGateway {
		t.Fatalf("secret is not owned by the MCPGateway: %+v", first.OwnerReferences)
	}

	for range 3 {
		if err := r.ensureAdminTokenSecret(ctx, gw); err != nil {
			t.Fatal(err)
		}
	}
	if got := string(readAdminSecret(t, r).Data[gatewayAdminTokenKey]); got != token {
		t.Errorf("token changed on reconcile: %q -> %q", token, got)
	}
}

func TestAdminTokensDifferPerGateway(t *testing.T) {
	a, err := generateAdminToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := generateAdminToken()
	if a == b {
		t.Error("two generated tokens are equal")
	}
}

func TestAdminTokenSecretMissingTokenIsFilled(t *testing.T) {
	empty := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: adminTestSecret, Namespace: adminTestNS}}
	r := adminTestReconciler(t, nil, empty)
	if err := r.ensureAdminTokenSecret(context.Background(), adminTestGW()); err != nil {
		t.Fatal(err)
	}
	if len(readAdminSecret(t, r).Data[gatewayAdminTokenKey]) == 0 {
		t.Error("secret without a token was not filled")
	}
}

func TestAdminNetworkPolicyAdmitsOnlyTheOperatorToTheAdminPort(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", adminTestOperatorNS)
	r := adminTestReconciler(t, nil)
	gw := adminTestGW()
	if err := r.reconcileAdminAccess(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	policy := &networkingv1.NetworkPolicy{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: adminTestNS, Name: "gw-admin"}, policy); err != nil {
		t.Fatalf("network policy not rendered: %v", err)
	}
	if len(policy.Spec.Ingress) != 2 {
		t.Fatalf("want 2 ingress rules, got %d", len(policy.Spec.Ingress))
	}
	checkToolRule(t, policy.Spec.Ingress[0])
	checkAdminRule(t, policy.Spec.Ingress[1])
	if len(policy.OwnerReferences) != 1 {
		t.Error("policy is not owned by the MCPGateway")
	}
	// A second reconcile updates in place.
	if err := r.reconcileAdminAccess(context.Background(), gw); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
}

func checkToolRule(t *testing.T, rule networkingv1.NetworkPolicyIngressRule) {
	t.Helper()
	if len(rule.From) != 0 || rule.Ports[0].Port.IntVal != 8080 {
		t.Errorf("tool port must be open to every caller: %+v", rule)
	}
}

func checkAdminRule(t *testing.T, rule networkingv1.NetworkPolicyIngressRule) {
	t.Helper()
	if rule.Ports[0].Port.IntVal != 8081 || len(rule.From) != 1 {
		t.Fatalf("admin port rule wrong: %+v", rule)
	}
	peer := rule.From[0]
	if peer.NamespaceSelector.MatchLabels[namespaceNameLabel] != adminTestOperatorNS ||
		peer.PodSelector.MatchLabels[controlPlaneLabel] != controllerManagerValue {
		t.Errorf("admin port is not restricted to the operator pod: %+v", peer)
	}
}

func TestContextForgeGetsNoAdminPortOrPolicy(t *testing.T) {
	gw := adminTestGW()
	gw.Spec.Implementation = kubemootv1alpha1.ImplementationContextForge
	r := adminTestReconciler(t, nil)
	if err := r.reconcileAdminAccess(context.Background(), gw); err != nil {
		t.Fatal(err)
	}
	list := &networkingv1.NetworkPolicyList{}
	if err := r.List(context.Background(), list); err != nil || len(list.Items) != 0 {
		t.Errorf("unexpected policies %v err %v", list.Items, err)
	}
	if gatewayAdminPort(gw) != gatewayToolPort(gw) {
		t.Error("contextforge must keep one port")
	}
}

func TestAdminPortAvoidsTheToolPort(t *testing.T) {
	gw := adminTestGW()
	if gatewayAdminPort(gw) != 8081 {
		t.Errorf("default admin port = %d", gatewayAdminPort(gw))
	}
	gw.Spec.Port = 8081
	if gatewayAdminPort(gw) == gatewayToolPort(gw) {
		t.Error("admin port collides with the tool port")
	}
}

func checkGatewayContainer(t *testing.T, container corev1.Container) {
	t.Helper()
	env := map[string]corev1.EnvVar{}
	for _, e := range container.Env {
		env[e.Name] = e
	}
	if env[envAdminPort].Value != "8081" {
		t.Errorf("admin port env = %q", env[envAdminPort].Value)
	}
	ref := env[envAdminToken].ValueFrom
	if env[envAdminToken].Value != "" || ref == nil || ref.SecretKeyRef.Name != adminTestSecret {
		t.Errorf("token must come from the secret, never inline: %+v", env[envAdminToken])
	}
	if len(container.Ports) != 2 || container.Ports[1].ContainerPort != 8081 {
		t.Errorf("container ports = %+v", container.Ports)
	}
	for _, probe := range []*corev1.Probe{container.LivenessProbe, container.ReadinessProbe} {
		if probe.HTTPGet.Port.IntVal != 8080 {
			t.Errorf("probes must stay on the tool port: %v", probe.HTTPGet.Port)
		}
	}

}

func TestDeploymentAndServiceCarryTheAdminPort(t *testing.T) {
	r := adminTestReconciler(t, nil)
	gw := adminTestGW()
	dep := r.buildDeployment(gw)
	container := dep.Spec.Template.Spec.Containers[0]

	checkGatewayContainer(t, container)

	svc, err := r.reconcileService(context.Background(), gw)
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.Spec.Ports) != 2 || svc.Spec.Ports[1].Port != 8081 {
		t.Errorf("service ports = %+v", svc.Spec.Ports)
	}
}

type recordedCall struct {
	method, url, auth string
}

type recorder struct {
	mu    sync.Mutex
	calls []recordedCall
}

func (rec *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	rec.mu.Lock()
	rec.calls = append(rec.calls, recordedCall{req.Method, req.URL.String(), req.Header.Get("Authorization")})
	rec.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
}

func TestOperatorCallsAdminRoutesOnTheAdminPortWithTheToken(t *testing.T) {
	rec := &recorder{}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: adminTestSecret, Namespace: adminTestNS},
		Data:       map[string][]byte{gatewayAdminTokenKey: []byte("tok-123")},
	}
	r := adminTestReconciler(t, rec, secret)
	gw := adminTestGW()
	mcp := &kubemootv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: adminTestToolsMCP, Namespace: adminTestNS}}
	mcp.Status.Endpoint = "http://" + adminTestToolsMCP + ".crew-a:8080"
	ctx := context.Background()

	r.syncMCPServerRegistrations(ctx, gw, []kubemootv1alpha1.MCPServer{*mcp})
	r.scrapeGatewayFeedback(ctx, gw)
	if err := r.unregisterMCPServer(ctx, gw, "gone", 8081, "tok-123"); err != nil {
		t.Fatal(err)
	}

	if len(rec.calls) != 3 {
		t.Fatalf("calls = %+v", rec.calls)
	}
	for _, call := range rec.calls {
		if !strings.Contains(call.url, ":8081/admin/") {
			t.Errorf("admin call not on the admin port: %s", call.url)
		}
		if call.auth != "Bearer tok-123" {
			t.Errorf("%s %s sent auth %q", call.method, call.url, call.auth)
		}
	}
}
