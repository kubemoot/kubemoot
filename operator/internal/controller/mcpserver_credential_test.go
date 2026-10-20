/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	credNS         = "crew-x"
	credSecret     = "ext-auth"
	credToken      = "tok-9f2c-very-secret"
	credExternal   = "https://mcp.example.com/v1/mcp/"
	credServerName = "ext"
)

func credServer(authHeader string) *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: credServerName, Namespace: credNS},
		Spec: kubemootv1alpha1.MCPServerSpec{
			ExternalEndpoint: credExternal,
			Transport:        kubemootv1alpha1.TransportHTTP,
			Registry:         &kubemootv1alpha1.RegistryConfig{AuthSecretRef: credSecret, AuthHeader: authHeader},
		},
		Status: kubemootv1alpha1.MCPServerStatus{Endpoint: credExternal, Ready: true},
	}
}

func credSecretWith(data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: credSecret, Namespace: credNS}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func credReconciler(t *testing.T, objs ...client.Object) *MCPGatewayReconciler {
	t.Helper()
	scheme := gatewayScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &MCPGatewayReconciler{Client: cli, Scheme: scheme}
}

// payloadAuth builds the registration the Kubemoot gateway receives and returns its auth object.
func payloadAuth(t *testing.T, r *MCPGatewayReconciler, mcp *kubemootv1alpha1.MCPServer) (map[string]string, error) {
	t.Helper()
	payload := r.buildRegistrationPayload(kubemootv1alpha1.ImplementationKubemoot, mcp)
	if _, err := r.attachRegistrationCredential(context.Background(), kubemootv1alpha1.ImplementationKubemoot, mcp, payload); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Auth map[string]string `json:"auth"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got.Auth, nil
}

func TestRegistrationCarriesBearerToken(t *testing.T) {
	r := credReconciler(t, credSecretWith(map[string]string{secretKeyBearerToken: credToken + "\n"}))

	auth, err := payloadAuth(t, r, credServer(""))

	if err != nil {
		t.Fatal(err)
	}
	if auth["header"] != headerAuthorization || auth["value"] != "Bearer "+credToken {
		t.Errorf("auth = %v, want Authorization: Bearer <token> with the newline trimmed", auth)
	}
}

func TestRegistrationCarriesAPIKeyInTheDefaultHeader(t *testing.T) {
	r := credReconciler(t, credSecretWith(map[string]string{secretKeyAPIKey: credToken}))

	auth, err := payloadAuth(t, r, credServer(""))

	if err != nil {
		t.Fatal(err)
	}
	if auth["header"] != "X-API-Key" || auth["value"] != credToken {
		t.Errorf("auth = %v", auth)
	}
}

func TestRegistrationCarriesAPIKeyInTheNamedHeader(t *testing.T) {
	r := credReconciler(t, credSecretWith(map[string]string{secretKeyAPIKey: credToken}))

	auth, err := payloadAuth(t, r, credServer("X-Subscriber-Key"))

	if err != nil {
		t.Fatal(err)
	}
	if auth["header"] != "X-Subscriber-Key" || auth["value"] != credToken {
		t.Errorf("auth = %v", auth)
	}
}

func TestBearerTokenWinsOverAPIKey(t *testing.T) {
	r := credReconciler(t, credSecretWith(map[string]string{secretKeyBearerToken: credToken, secretKeyAPIKey: testMismatchName}))

	auth, _ := payloadAuth(t, r, credServer("X-Key"))

	if auth["header"] != headerAuthorization {
		t.Errorf("auth = %v", auth)
	}
}

func TestRegistrationWithoutAuthCarriesNone(t *testing.T) {
	r := credReconciler(t)
	mcp := credServer("")
	mcp.Spec.Registry = nil

	auth, err := payloadAuth(t, r, mcp)

	if err != nil || auth != nil {
		t.Errorf("auth = %v, err = %v; want neither", auth, err)
	}
}

func TestContextForgeRegistrationCarriesABearerTokenInItsOwnFields(t *testing.T) {
	r := credReconciler(t, credSecretWith(map[string]string{secretKeyBearerToken: credToken}))
	mcp := credServer("")
	payload := r.buildRegistrationPayload(kubemootv1alpha1.ImplementationContextForge, mcp)

	cred, err := r.attachRegistrationCredential(context.Background(), kubemootv1alpha1.ImplementationContextForge, mcp, payload)

	if err != nil || cred == nil {
		t.Fatalf("cred %v err %v", cred, err)
	}
	if payload["auth_type"] != "bearer" || payload["auth_token"] != credToken {
		t.Errorf("payload = %v", payload)
	}
	if _, ok := payload["auth"]; ok {
		t.Error("payload carries the Kubemoot auth object for ContextForge")
	}
}

func TestContextForgeRegistrationWithAMissingSecretIsRefused(t *testing.T) {
	r := credReconciler(t)
	mcp := credServer("")
	payload := r.buildRegistrationPayload(kubemootv1alpha1.ImplementationContextForge, mcp)

	if _, err := r.attachRegistrationCredential(context.Background(), kubemootv1alpha1.ImplementationContextForge, mcp, payload); err == nil {
		t.Error("expected an error")
	}
}

func TestCredentialProblemsAreNamedWithoutAValue(t *testing.T) {
	tests := []struct {
		name       string
		secret     *corev1.Secret
		authHeader string
		reason     string
	}{
		{"no Secret", nil, "", reasonCredentialNoSecret},
		{"no usable key", credSecretWith(map[string]string{testMismatchName: credToken}), "", reasonCredentialNoKey},
		{"empty key", credSecretWith(map[string]string{secretKeyBearerToken: "  "}), "", reasonCredentialNoKey},
		{"bad header", credSecretWith(map[string]string{secretKeyAPIKey: credToken}), "Bad Header", reasonCredentialBadHeader},
		{"multi-line value", credSecretWith(map[string]string{secretKeyAPIKey: "a\nX-Evil: 1"}), "", reasonCredentialBadHeader},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []client.Object
			if tt.secret != nil {
				objs = append(objs, tt.secret)
			}
			r := credReconciler(t, objs...)

			_, err := payloadAuth(t, r, credServer(tt.authHeader))

			if err == nil {
				t.Fatal("expected an error")
			}
			if credentialReason(err) != tt.reason {
				t.Errorf("reason = %s, want %s (%v)", credentialReason(err), tt.reason, err)
			}
			if strings.Contains(err.Error(), credToken) || strings.Contains(err.Error(), "X-Evil") {
				t.Errorf("error exposes a value: %v", err)
			}
		})
	}
}

func TestFormattingACredentialNeverPrintsItsValue(t *testing.T) {
	c := &registryCredential{Header: headerAuthorization, Value: "Bearer " + credToken}
	for _, text := range []string{fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), c.String(), fmt.Sprint(*c)} {
		if strings.Contains(text, credToken) {
			t.Errorf("formatting exposes the value: %s", text)
		}
	}
	payload := map[string]interface{}{"auth": c}
	if strings.Contains(fmt.Sprintf("%v", payload), credToken) {
		t.Error("formatting a payload exposes the value")
	}
}

func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d x", status), Body: io.NopCloser(strings.NewReader(body))}
}

func credGateway() *kubemootv1alpha1.MCPGateway {
	return &kubemootv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: credNS}}
}

func TestRegisterSendsTheCredentialToTheGatewayAndNeverLogsIt(t *testing.T) {
	var logged bytes.Buffer
	ctx := logf.IntoContext(context.Background(), funcr.New(func(prefix, args string) { logged.WriteString(prefix + args + "\n") }, funcr.Options{Verbosity: 9}))
	r := credReconciler(t, credSecretWith(map[string]string{secretKeyBearerToken: credToken}))
	var sent []byte
	r.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sent, _ = io.ReadAll(req.Body)
		return reply(http.StatusCreated, "{}"), nil
	})}

	if err := r.registerMCPServerWithGateway(ctx, credGateway(), credServer(""), 8080, ""); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(sent), `"value":"Bearer `+credToken+`"`) {
		t.Errorf("the gateway did not receive the credential: %s", sent)
	}
	if strings.Contains(logged.String(), credToken) {
		t.Errorf("the token reached the log: %s", logged.String())
	}
}

func TestRegisterErrorNeverEchoesTheToken(t *testing.T) {
	var logged bytes.Buffer
	ctx := logf.IntoContext(context.Background(), funcr.New(func(prefix, args string) { logged.WriteString(prefix + args + "\n") }, funcr.Options{Verbosity: 9}))
	r := credReconciler(t, credSecretWith(map[string]string{secretKeyBearerToken: credToken}))
	r.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		return reply(http.StatusBadRequest, "cannot parse "+string(body)), nil
	})}

	err := r.registerMCPServerWithGateway(ctx, credGateway(), credServer(""), 8080, "")

	if err == nil {
		t.Fatal("expected the registration to fail")
	}
	if strings.Contains(err.Error(), credToken) || strings.Contains(logged.String(), credToken) {
		t.Errorf("the token leaked: %v / %s", err, logged.String())
	}
}

func TestRegisterWithAMissingSecretSendsNothing(t *testing.T) {
	r := credReconciler(t)
	called := false
	r.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return reply(http.StatusCreated, "{}"), nil
	})}

	err := r.registerMCPServerWithGateway(context.Background(), credGateway(), credServer(""), 8080, "")

	if err == nil || called {
		t.Errorf("a server whose credential is missing must not register (err %v, called %v)", err, called)
	}
}

func credMCPReconcile(t *testing.T, objs ...client.Object) (*kubemootv1alpha1.MCPServer, error) {
	t.Helper()
	scheme := gatewayScheme(t)
	ms := credServer("")
	ms.Status = kubemootv1alpha1.MCPServerStatus{}
	ms.Finalizers = []string{mcpServerFinalizer}
	objs = append(objs, ms)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(ms).Build()
	r := &MCPServerReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: credServerName, Namespace: credNS}})
	got := &kubemootv1alpha1.MCPServer{}
	if gerr := cli.Get(context.Background(), client.ObjectKeyFromObject(ms), got); gerr != nil {
		t.Fatal(gerr)
	}
	return got, err
}

func TestMCPServerReportsAMissingCredentialSecret(t *testing.T) {
	got, err := credMCPReconcile(t)

	if err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionTypeCredentialReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonCredentialNoSecret {
		t.Fatalf("condition = %+v", cond)
	}
	if got.Status.Ready || !strings.Contains(got.Status.Message, credSecret) {
		t.Errorf("a server that cannot get its key is not ready and says which Secret: %+v", got.Status)
	}
}

func TestMCPServerReportsAMissingKey(t *testing.T) {
	got, _ := credMCPReconcile(t, credSecretWith(map[string]string{"x": credToken}))

	cond := meta.FindStatusCondition(got.Status.Conditions, conditionTypeCredentialReady)
	if cond == nil || cond.Reason != reasonCredentialNoKey {
		t.Fatalf("condition = %+v", cond)
	}
	if strings.Contains(got.Status.Message, credToken) {
		t.Error("status exposes a value")
	}
}

func TestMCPServerWithItsCredentialIsReadyAndNeverShowsIt(t *testing.T) {
	got, _ := credMCPReconcile(t, credSecretWith(map[string]string{secretKeyBearerToken: credToken}))

	cond := meta.FindStatusCondition(got.Status.Conditions, conditionTypeCredentialReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || !got.Status.Ready {
		t.Fatalf("condition = %+v, ready = %v", cond, got.Status.Ready)
	}
	raw, _ := json.Marshal(got.Status)
	if strings.Contains(string(raw), credToken) {
		t.Errorf("status exposes the token: %s", raw)
	}
}

func TestASecretMapsToTheServersThatUseIt(t *testing.T) {
	scheme := gatewayScheme(t)
	other := credServer("")
	other.Name = testMismatchName
	other.Spec.Registry.AuthSecretRef = "different"
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(credServer(""), other).Build()
	r := &MCPServerReconciler{Client: cli, Scheme: scheme}

	requests := r.findMCPServersForSecret(context.Background(), credSecretWith(nil))

	if len(requests) != 1 || requests[0].Name != credServerName || requests[0].Namespace != credNS {
		t.Errorf("requests = %v", requests)
	}
}

func TestASecretMapsToTheGatewaysOfItsServers(t *testing.T) {
	r := credReconciler(t, credServer(""), credGateway())

	requests := r.findGatewaysForSecret(context.Background(), credSecretWith(nil))

	if len(requests) != 1 || requests[0].Name != "gw" {
		t.Errorf("requests = %v", requests)
	}
}

func TestRedactIsANoOpWithoutACredential(t *testing.T) {
	var c *registryCredential
	if got := c.redact("text"); got != "text" {
		t.Errorf("redact = %q", got)
	}
}
