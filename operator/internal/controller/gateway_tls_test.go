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
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	gwTestCrew     = "pilot"
	gwTestCrewNS   = "crew-pilot"
	gwTestName     = "pilot-discussion"
	gwTestSecret   = "pilot-discussion-tls"
	gwTestOtherSec = "unrelated-secret"
	gwTestProbeRdy = "/ready"
	gwTestProbeHlt = "/health"
	gwTestCont     = "gateway-container"
)

// gwFixture is a gateway TLS issuer and a crew over one fake client.
type gwFixture struct {
	*caFixture
	g    *GatewayTLS
	crew *kubemootv1alpha1.Crew
}

func newGWFixture(t *testing.T) *gwFixture {
	t.Helper()
	f := newCAFixture(t)
	if err := kubemootv1alpha1.AddToScheme(f.scheme); err != nil {
		t.Fatal(err)
	}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: gwTestCrew, Namespace: gwTestCrewNS, UID: "u1"}}
	f.ca.Client = fake.NewClientBuilder().WithScheme(f.scheme).WithObjects(crew).WithStatusSubresource(crew).Build()
	return &gwFixture{caFixture: f, g: &GatewayTLS{CA: f.ca, PlainListener: true}, crew: crew}
}

func (f *gwFixture) ensure(t *testing.T) time.Time {
	t.Helper()
	notAfter, err := f.g.EnsureCert(context.Background(), f.crew, gwTestName, f.scheme)
	if err != nil {
		t.Fatal(err)
	}
	return notAfter
}

func (f *gwFixture) leaf(t *testing.T) (*corev1.Secret, *x509.Certificate) {
	t.Helper()
	s := &corev1.Secret{}
	if err := f.ca.Client.Get(context.Background(), client.ObjectKey{Namespace: gwTestCrewNS, Name: gwTestSecret}, s); err != nil {
		t.Fatal(err)
	}
	leaf, err := tlsKeyPair(s.Data)
	if err != nil {
		t.Fatal(err)
	}
	return s, leaf
}

func TestEnsureCertIssuesALabelledOwnedCertificateTheBundleVerifies(t *testing.T) {
	f := newGWFixture(t)
	notAfter := f.ensure(t)
	s, leaf := f.leaf(t)
	if err := f.ca.PublishCA(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range serviceDNSNames(gwTestName, gwTestCrewNS) {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: poolOf(f.bundle(t)), CurrentTime: f.now}); err != nil {
			t.Errorf("does not verify for %s: %v", name, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "other-discussion.crew-pilot.svc", Roots: poolOf(f.bundle(t)), CurrentTime: f.now}); err == nil {
		t.Error("verifies for a name it was not issued for")
	}
	if !notAfter.Equal(leaf.NotAfter) || s.Labels[LabelInstallCALeaf] != valueTrue || s.Type != corev1.SecretTypeTLS {
		t.Errorf("notAfter %v labels %v type %s", notAfter, s.Labels, s.Type)
	}
	if len(s.OwnerReferences) != 1 || s.OwnerReferences[0].Name != gwTestCrew {
		t.Errorf("owners %v", s.OwnerReferences)
	}
}

func TestEnsureCertLeavesAFreshCertificateAndRenewsOneNearExpiry(t *testing.T) {
	f := newGWFixture(t)
	f.ensure(t)
	_, first := f.leaf(t)
	f.ensure(t)
	if _, again := f.leaf(t); again.SerialNumber.Cmp(first.SerialNumber) != 0 {
		t.Fatal("a fresh certificate was replaced")
	}
	f.now = f.now.Add(leafLifetime * 3 / 4)
	f.ensure(t)
	if _, renewed := f.leaf(t); renewed.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Fatal("a certificate in the last third of its life was kept")
	}
}

func TestACertificateOfTheOldCAStillVerifiesMidRotationAndIsReplacedAfterIt(t *testing.T) {
	f := newGWFixture(t)
	ctx := context.Background()
	f.ensure(t)
	_, oldLeaf := f.leaf(t)
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	f.rotate(t)
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := oldLeaf.Verify(x509.VerifyOptions{DNSName: gwTestName, Roots: poolOf(f.bundle(t)), CurrentTime: f.now}); err != nil {
		t.Fatalf("a leaf of the old CA does not verify against the bundle mid-rotation: %v", err)
	}
	f.ensure(t)
	_, newLeaf := f.leaf(t)
	if newLeaf.SerialNumber.Cmp(oldLeaf.SerialNumber) == 0 {
		t.Fatal("the certificate the new CA did not sign was kept")
	}
	if err := f.ca.PublishCA(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.bundle(t); len(got) != 1 {
		t.Fatalf("the previous CA stayed after the certificate moved: %d", len(got))
	}
}

func TestEnsureCertReplacesACertificateWithADamagedKey(t *testing.T) {
	f := newGWFixture(t)
	f.ensure(t)
	s, before := f.leaf(t)
	s.Data[corev1.TLSPrivateKeyKey] = []byte("garbage")
	if err := f.ca.Client.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	f.ensure(t)
	if _, after := f.leaf(t); after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Fatal("a certificate with a damaged key was kept")
	}
}

func TestEnsureCertEndsNoLaterThanTheCAAndRefusesAnExpiringCA(t *testing.T) {
	f := newGWFixture(t)
	f.now = f.now.Add(0)
	ca, _ := f.ca.Authority(context.Background())
	f.now = ca.cert.NotAfter.Add(-caMinRemaining - 24*time.Hour)
	if got := f.ensure(t); got.After(ca.cert.NotAfter) {
		t.Fatalf("certificate ends %v, after the CA at %v", got, ca.cert.NotAfter)
	}
	f.now = ca.cert.NotAfter.Add(-caMinRemaining / 2)
	if _, err := f.g.EnsureCert(context.Background(), f.crew, gwTestName, f.scheme); !errors.Is(err, errCAExpiring) {
		t.Fatalf("a certificate was issued by a CA near its end: %v", err)
	}
}

func TestReconcileGatewayCertRecordsAConditionAndAnEventOnFailure(t *testing.T) {
	f := newGWFixture(t)
	rec := events.NewFakeRecorder(4)
	r := &CrewReconciler{Client: f.ca.Client, Scheme: f.scheme, GatewayTLS: f.g, Recorder: rec}
	if err := r.reconcileGatewayCert(context.Background(), f.crew); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(f.crew.Status.Conditions, conditionTypeGatewayTLSReady)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("condition %+v", cond)
	}
	f.now = f.now.Add(caLifetime)
	if err := r.reconcileGatewayCert(context.Background(), f.crew); !errors.Is(err, errCAExpiring) {
		t.Fatalf("an expired CA: %v", err)
	}
	cond = meta.FindStatusCondition(f.crew.Status.Conditions, conditionTypeGatewayTLSReady)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("condition after the failure %+v", cond)
	}
	select {
	case e := <-rec.Events:
		if e == "" {
			t.Fatal("empty event")
		}
	default:
		t.Fatal("no event on the crew")
	}
}

func TestAFailedCertificateStopsTheDiscussionReconcileBeforeAnyDeployment(t *testing.T) {
	f := newGWFixture(t)
	f.ca.SecretName = "missing-ca"
	f.ca.Client = interceptCreateFail(f)
	f.crew.Spec.Discussion = &kubemootv1alpha1.DiscussionConfig{}
	r := &CrewReconciler{Client: f.ca.Client, Scheme: f.scheme, GatewayTLS: f.g, ConfigCache: NewConfigCache()}
	_, err, handled := r.reconcileDiscussion(context.Background(), f.crew)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if f.crew.Status.Phase != phaseError || !strings.Contains(f.crew.Status.Message, "TLS certificate") {
		t.Fatalf("status %q: %q, want an Error about the TLS certificate", f.crew.Status.Phase, f.crew.Status.Message)
	}
	deployments := &appsv1.DeploymentList{}
	if err := f.ca.Client.List(context.Background(), deployments); err != nil || len(deployments.Items) != 0 {
		t.Fatalf("a Deployment was made: %v %d", err, len(deployments.Items))
	}
}

// interceptCreateFail is a client that finds no CA and cannot create one.
func interceptCreateFail(f *gwFixture) client.Client {
	return fake.NewClientBuilder().WithScheme(f.scheme).WithObjects(f.crew).WithStatusSubresource(f.crew).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errors.New("forbidden")
			}
			return c.Create(ctx, obj, opts...)
		}}).Build()
}

func TestGatewayTLSFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	ca := &InstallCA{}
	if GatewayTLSFromEnv(nil, env(map[string]string{envGatewayTLSEnabled: valueTrue})) != nil {
		t.Fatal("TLS with no install CA")
	}
	if GatewayTLSFromEnv(ca, env(map[string]string{envGatewayTLSEnabled: valueFalse})) != nil || GatewayTLSFromEnv(ca, env(nil)) != nil {
		t.Fatal("TLS on although the chart turned it off")
	}
	g := GatewayTLSFromEnv(ca, env(map[string]string{envGatewayTLSEnabled: valueTrue}))
	if g == nil || !g.PlainListener {
		t.Fatalf("got %+v", g)
	}
	if g = GatewayTLSFromEnv(ca, env(map[string]string{envGatewayTLSEnabled: valueTrue, envGatewayTLSPlain: valueFalse})); g.PlainListener {
		t.Fatal("plain listener stayed on")
	}
}

func gatewayPodSpec() *corev1.PodSpec {
	probe := func(path string) *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(crewGatewayPort)}}}
	}
	return &corev1.PodSpec{Containers: []corev1.Container{{
		Name: gwTestCont, LivenessProbe: probe(gwTestProbeHlt), ReadinessProbe: probe(gwTestProbeRdy),
	}}}
}

func gwEnvValue(c corev1.Container, name string) string {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return "(unset)"
}

func TestApplyAddsTheTLSListenerAndMovesTheProbesToHTTPS(t *testing.T) {
	spec := gatewayPodSpec()
	(&GatewayTLS{PlainListener: false}).apply(spec, gwTestName)
	c := spec.Containers[0]
	if len(spec.Volumes) != 1 || spec.Volumes[0].Secret.SecretName != gwTestSecret {
		t.Fatalf("volumes %v", spec.Volumes)
	}
	want := map[string]string{
		"TLS_CERT_FILE": gatewayTLSMount + "/tls.crt", "TLS_KEY_FILE": gatewayTLSMount + "/tls.key",
		"TLS_PORT": "8443", "PLAIN_LISTENER": valueFalse,
	}
	for name, value := range want {
		if got := gwEnvValue(c, name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	for _, p := range []*corev1.Probe{c.LivenessProbe, c.ReadinessProbe} {
		if p.HTTPGet.Scheme != corev1.URISchemeHTTPS || p.HTTPGet.Port.IntValue() != int(gatewayTLSPort) {
			t.Fatalf("probe %+v", p.HTTPGet)
		}
	}
	spec = gatewayPodSpec()
	(&GatewayTLS{PlainListener: true}).apply(spec, gwTestName)
	if gwEnvValue(spec.Containers[0], "PLAIN_LISTENER") != valueTrue {
		t.Fatal("PLAIN_LISTENER is not true")
	}
}

func TestApplyWithoutTLSChangesNothing(t *testing.T) {
	spec := gatewayPodSpec()
	var g *GatewayTLS
	g.apply(spec, gwTestName)
	if len(spec.Volumes) != 0 || len(spec.Containers[0].Env) != 0 || spec.Containers[0].LivenessProbe.HTTPGet.Scheme != "" {
		t.Fatalf("spec changed: %+v", spec)
	}
}

func TestTheGatewayServiceOffersHTTPSOnlyWithTLS(t *testing.T) {
	f := newGWFixture(t)
	service := func(tlsOn bool) *corev1.Service {
		r := &CrewReconciler{Client: f.ca.Client, Scheme: f.scheme}
		if tlsOn {
			r.GatewayTLS = f.g
		}
		if err := r.reconcileService(context.Background(), f.crew); err != nil {
			t.Fatal(err)
		}
		svc := &corev1.Service{}
		if err := f.ca.Client.Get(context.Background(), client.ObjectKey{Namespace: gwTestCrewNS, Name: r.gatewayName(f.crew)}, svc); err != nil {
			t.Fatal(err)
		}
		return svc
	}
	if plain := service(false); len(plain.Spec.Ports) != 1 || plain.Spec.Ports[0].Name == portNameHTTPS {
		t.Fatalf("plain service ports %v", plain.Spec.Ports)
	}
	secured := service(true)
	if len(secured.Spec.Ports) != 2 {
		t.Fatalf("secured service ports %v", secured.Spec.Ports)
	}
	if https := secured.Spec.Ports[1]; https.Name != portNameHTTPS || https.Port != 443 || https.TargetPort.IntValue() != 8443 {
		t.Fatalf("https port %+v", https)
	}
}

func TestAHealthyCrewIsCheckedAgainBeforeItsCertificateCanExpire(t *testing.T) {
	f := newGWFixture(t)
	f.crew.Spec.Discussion = &kubemootv1alpha1.DiscussionConfig{}
	r := &CrewReconciler{GatewayTLS: f.g}
	if got := r.withCertCheck(ctrl.Result{}, f.crew).RequeueAfter; got != gatewayCertCheckEvery {
		t.Fatalf("requeue %v", got)
	}
	if got := r.withCertCheck(ctrl.Result{RequeueAfter: time.Minute}, f.crew).RequeueAfter; got != time.Minute {
		t.Fatalf("a sooner requeue was replaced: %v", got)
	}
	if got := r.withCertCheck(ctrl.Result{RequeueAfter: 100 * time.Hour}, f.crew).RequeueAfter; got != gatewayCertCheckEvery {
		t.Fatalf("a later requeue was kept: %v", got)
	}
	disabled := false
	f.crew.Spec.Discussion = &kubemootv1alpha1.DiscussionConfig{Enabled: &disabled}
	if got := r.withCertCheck(ctrl.Result{}, f.crew).RequeueAfter; got != 0 {
		t.Fatalf("a crew with no gateway was requeued: %v", got)
	}
	if got := (&CrewReconciler{}).withCertCheck(ctrl.Result{}, f.crew).RequeueAfter; got != 0 {
		t.Fatalf("a plain install requeued: %v", got)
	}
}

func gwSecret(ns, name, data string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string][]byte{"k": []byte(data)}}
}

func TestOnlyTheCASecretQueuesCrews(t *testing.T) {
	f := newGWFixture(t)
	p := caSecretPredicate(f.ca)
	ca := gwSecret(caTestNS, caTestSecret, "a")
	if !p.Create(event.CreateEvent{Object: ca}) {
		t.Fatal("the CA did not queue")
	}
	if p.Create(event.CreateEvent{Object: gwSecret(caTestNS, gwTestOtherSec, "a")}) {
		t.Fatal("another Secret queued")
	}
	if p.Create(event.CreateEvent{Object: gwSecret(gwTestCrewNS, caTestSecret, "a")}) {
		t.Fatal("a Secret of the CA's name in another namespace queued crews")
	}
	if p.Delete(event.DeleteEvent{Object: ca}) || p.Generic(event.GenericEvent{Object: ca}) {
		t.Fatal("delete or generic queued")
	}
	if caSecretPredicate(nil).Create(event.CreateEvent{Object: ca}) {
		t.Fatal("a nil CA admitted an event")
	}
}

func TestTheCASecretQueuesCrewsOnlyWhenItsContentChanges(t *testing.T) {
	f := newGWFixture(t)
	p := caSecretPredicate(f.ca)
	ca := gwSecret(caTestNS, caTestSecret, "a")
	if !p.Update(event.UpdateEvent{ObjectOld: ca, ObjectNew: gwSecret(caTestNS, caTestSecret, "b")}) {
		t.Fatal("a changed CA did not queue")
	}
	if p.Update(event.UpdateEvent{ObjectOld: ca, ObjectNew: gwSecret(caTestNS, caTestSecret, "a")}) {
		t.Fatal("an unchanged CA (a metadata-only write) queued")
	}
	reqs := allCrewRequests(context.Background(), f.ca.Client)
	if len(reqs) != 1 || reqs[0] != (reconcile.Request{NamespacedName: client.ObjectKeyFromObject(f.crew)}) {
		t.Fatalf("requests %v", reqs)
	}
}

func TestAnUnlistableCrewSetQueuesNothing(t *testing.T) {
	f := newGWFixture(t)
	failing := fake.NewClientBuilder().WithScheme(f.scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("unavailable")
		},
	}).Build()
	if reqs := allCrewRequests(context.Background(), failing); reqs != nil {
		t.Fatalf("requests %v", reqs)
	}
}

func TestOnlyCertificateSecretsOfTheInstallCAWakeACrew(t *testing.T) {
	p := labelledInstallCALeaf()
	leaf := gwSecret(gwTestCrewNS, gwTestSecret, "a")
	leaf.Labels = map[string]string{LabelInstallCALeaf: valueTrue}
	if !p.Create(event.CreateEvent{Object: leaf}) {
		t.Fatal("a labelled certificate Secret did not wake the crew")
	}
	if p.Create(event.CreateEvent{Object: gwSecret(gwTestCrewNS, gwTestOtherSec, "a")}) {
		t.Fatal("an unlabelled Secret woke the crew")
	}
}

func TestTheGatewayTLSConditionIsRemovedWhenTheInstallStopsServingTLS(t *testing.T) {
	f := newGWFixture(t)
	r := &CrewReconciler{Client: f.ca.Client, Scheme: f.scheme, GatewayTLS: f.g}
	if err := r.reconcileGatewayCert(context.Background(), f.crew); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(f.crew.Status.Conditions, conditionTypeGatewayTLSReady) == nil {
		t.Fatal("no condition with TLS on")
	}
	r.GatewayTLS = nil
	if err := r.reconcileGatewayCert(context.Background(), f.crew); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(f.crew.Status.Conditions, conditionTypeGatewayTLSReady) != nil {
		t.Fatal("the condition stayed after TLS was turned off")
	}
}
