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
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	gqNS         = "crew-a"
	gqOtherNS    = "crew-b"
	gqGroup      = "crew-a-query"
	gqRegistry   = "crew-a-query-registry"
	gqImage      = "ghcr.io/kubemoot/query-service-go:1.2.3"
	gqEmbedURL   = "http://vllm-embed.models:8000"
	gqModel      = "nomic-embed-text"
	gqSecret     = "pg-creds"
	gqPG         = "pg.crew-a:5432/vectors"
	gqDocsA      = "docs-a"
	gqDocsB      = "docs-b"
	gqDocsC      = "docs-c"
	gqNewColl    = "docs_a_v2"
	gqGoEndpoint = "http://crew-a-query.crew-a:8000/sources/"
	gqPyDocsURL  = "http://docs-query.ns:8000"
	gqServer     = "vllm"
)

// gqHarness runs the RAGSource reconciler's query service work against a fake cluster.
type gqHarness struct {
	t     *testing.T
	c     client.Client
	r     *RAGSourceReconciler
	cache *ConfigCache
	ready *readyTransport
}

// readyTransport answers the operator's calls to the query pods' /ready from a table of
// "ip:port" to body; any other address is unreachable.
type readyTransport struct{ bodies map[string]string }

func (t *readyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, ok := t.bodies[req.URL.Host]
	if !ok || req.URL.Path != readyPath {
		return nil, fmt.Errorf("no pod at %s", req.URL.Host)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func gqSource(name, namespace string) *kubemootv1alpha1.RAGSource {
	return &kubemootv1alpha1.RAGSource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID("uid-" + name)},
		Spec: kubemootv1alpha1.RAGSourceSpec{
			EmbeddingModelRef: embedTestName,
			Source:            kubemootv1alpha1.SourceConfig{Type: kubemootv1alpha1.RAGSourceTypeURL},
			VectorStore: kubemootv1alpha1.VectorStoreConfig{
				Type: kubemootv1alpha1.VectorStorePgvector, Endpoint: "pg." + namespace + ":5432/vectors",
				Collection: strings.ReplaceAll(name, "-", "_"), Dimensions: 768,
			},
		},
	}
}

func gqEmbeddingModel(namespace string) *kubemootv1alpha1.EmbeddingModel {
	return &kubemootv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: embedTestName, Namespace: namespace},
		Spec:       kubemootv1alpha1.EmbeddingModelSpec{Model: gqModel},
		Status: kubemootv1alpha1.EmbeddingModelStatus{
			Ready: true, BoundServer: gqServer, Endpoint: gqEmbedURL,
			ModelInfo: &kubemootv1alpha1.EmbeddingModelInfo{Dimensions: 768},
		},
	}
}

func gqSecretObject(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: gqSecret, Namespace: namespace},
		Data:       map[string][]byte{secretKeyPassword: []byte("pw"), secretKeyUsername: []byte("reader")},
	}
}

// newGQHarness is a cluster with the EmbeddingModel and Secret of each namespace and the sources.
func newGQHarness(t *testing.T, engine kubemootv1alpha1.QueryServiceEngine, sources ...*kubemootv1alpha1.RAGSource) *gqHarness {
	t.Helper()
	scheme := rbacTestClient(t).Scheme()
	objs := make([]client.Object, 0, 4+len(sources))
	for _, ns := range []string{gqNS, gqOtherNS} {
		objs = append(objs, gqEmbeddingModel(ns), gqSecretObject(ns))
	}
	for _, rs := range sources {
		objs = append(objs, rs)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	h := &gqHarness{t: t, c: c, cache: NewConfigCache(), ready: &readyTransport{bodies: map[string]string{}}}
	h.setEngine(engine)
	h.r = &RAGSourceReconciler{Client: c, Scheme: scheme, ConfigCache: h.cache, HTTPClient: &http.Client{Transport: h.ready}}
	return h
}

func (h *gqHarness) setEngine(engine kubemootv1alpha1.QueryServiceEngine) {
	h.cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Images:       kubemootv1alpha1.ImageConfig{QueryServiceGo: gqImage, QueryService: "ghcr.io/kubemoot/query-service:9.9.9"},
		QueryService: &kubemootv1alpha1.QueryServiceSettings{Engine: engine},
	}})
}

// reconcileAll runs the query service step for every RAGSource, as their reconciles would.
func (h *gqHarness) reconcileAll() {
	h.t.Helper()
	var list kubemootv1alpha1.RAGSourceList
	if err := h.c.List(context.Background(), &list); err != nil {
		h.t.Fatal(err)
	}
	for i := range list.Items {
		rs := &list.Items[i]
		em := gqEmbeddingModel(rs.Namespace)
		h.r.reconcileQueryService(context.Background(), rs, em)
	}
}

func (h *gqHarness) deployments(namespace string) []appsv1.Deployment {
	h.t.Helper()
	var list appsv1.DeploymentList
	if err := h.c.List(context.Background(), &list, client.InNamespace(namespace)); err != nil {
		h.t.Fatal(err)
	}
	return list.Items
}

func (h *gqHarness) services(namespace string) []corev1.Service {
	h.t.Helper()
	var list corev1.ServiceList
	if err := h.c.List(context.Background(), &list, client.InNamespace(namespace)); err != nil {
		h.t.Fatal(err)
	}
	return list.Items
}

func (h *gqHarness) registry(namespace string) map[string]queryRegistrySource {
	h.t.Helper()
	cm := &corev1.ConfigMap{}
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: queryRegistryName(namespace)}, cm); err != nil {
		h.t.Fatalf("registry ConfigMap: %v", err)
	}
	return parseRegistry(cm.Data[registryKey])
}

// deleteSources removes the RAGSources from the cluster.
func (h *gqHarness) deleteSources(names ...string) {
	h.t.Helper()
	for _, name := range names {
		rs := &kubemootv1alpha1.RAGSource{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gqNS}}
		if err := h.c.Delete(context.Background(), rs); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *gqHarness) registryExists(namespace string) bool {
	h.t.Helper()
	err := h.c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: queryRegistryName(namespace)}, &corev1.ConfigMap{})
	return err == nil
}

func (h *gqHarness) group() *appsv1.Deployment {
	h.t.Helper()
	dep := &appsv1.Deployment{}
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: gqNS, Name: gqGroup}, dep); err != nil {
		h.t.Fatalf("group Deployment: %v", err)
	}
	return dep
}

// markAvailable says the Deployment finished rolling out.
func (h *gqHarness) markAvailable(name string) {
	h.t.Helper()
	dep := &appsv1.Deployment{}
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: gqNS, Name: name}, dep); err != nil {
		h.t.Fatal(err)
	}
	want := *dep.Spec.Replicas
	dep.Status = appsv1.DeploymentStatus{
		ObservedGeneration: dep.Generation, Replicas: want, UpdatedReplicas: want, AvailableReplicas: want, ReadyReplicas: want,
	}
	if err := h.c.Status().Update(context.Background(), dep); err != nil {
		h.t.Fatal(err)
	}
}

func threeSources() []*kubemootv1alpha1.RAGSource {
	a, b, c := gqSource(gqDocsA, gqNS), gqSource(gqDocsB, gqNS), gqSource(gqDocsC, gqNS)
	a.Spec.VectorStore.SecretRef, b.Spec.VectorStore.SecretRef = gqSecret, gqSecret
	return []*kubemootv1alpha1.RAGSource{a, b, c}
}

func TestOneNamespaceWithThreeSourcesGetsOneDeploymentServiceAndRegistry(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)

	h.reconcileAll()

	if deps := h.deployments(gqNS); len(deps) != 1 || deps[0].Name != gqGroup {
		t.Fatalf("deployments = %v, want the one group Deployment %s", gqDeploymentNames(deps), gqGroup)
	}
	if svcs := h.services(gqNS); len(svcs) != 1 || svcs[0].Name != gqGroup {
		t.Fatalf("services = %d, want the one group Service", len(svcs))
	}
	reg := h.registry(gqNS)
	if len(reg) != 3 {
		t.Fatalf("registry has %d entries, want 3: %v", len(reg), reg)
	}
	for _, name := range []string{gqDocsA, gqDocsB, gqDocsC} {
		checkRegistryEntry(t, name, reg[name])
	}
	if got := reg[gqDocsA].VectorStore.CredentialsDir; got != "/etc/kubemoot/credentials/"+gqSecret {
		t.Errorf("credentialsDir = %q, want the Secret's directory", got)
	}
	if got := reg[gqDocsC].VectorStore.CredentialsDir; got != "" {
		t.Errorf("a source with no secretRef has credentialsDir %q", got)
	}
}

// checkRegistryEntry says what a source's entry holds when it is the plain test RAGSource.
func checkRegistryEntry(t *testing.T, name string, e queryRegistrySource) {
	t.Helper()
	if e.Namespace != gqNS || e.VectorStore.Type != string(kubemootv1alpha1.VectorStorePgvector) || e.VectorStore.Endpoint != gqPG ||
		e.VectorStore.Dimensions != 768 || e.Embedding.Endpoint != gqEmbedURL || e.Embedding.Model != gqModel {
		t.Errorf("entry %s = %+v", name, e)
	}
	if want := strings.ReplaceAll(name, "-", "_"); e.VectorStore.Collection != want {
		t.Errorf("entry %s collection = %q, want %q", name, e.VectorStore.Collection, want)
	}
}

func TestEverySourceIsQueriedAtItsPathUnderTheGroupService(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)
	ctx := context.Background()

	for _, name := range []string{gqDocsA, gqDocsB, gqDocsC} {
		rs := &kubemootv1alpha1.RAGSource{}
		if err := h.c.Get(ctx, types.NamespacedName{Namespace: gqNS, Name: name}, rs); err != nil {
			t.Fatal(err)
		}
		h.r.reconcileQueryService(ctx, rs, gqEmbeddingModel(gqNS))
		if want := gqGoEndpoint + name; rs.Status.QueryEndpoint != want {
			t.Errorf("%s queryEndpoint = %q, want %q", name, rs.Status.QueryEndpoint, want)
		}
		if got, want := ragSearchEndpoint(kubemootv1alpha1.QueryServiceEngineGo, rs), gqGoEndpoint+name+"/mcp"; got != want {
			t.Errorf("%s search endpoint = %q, want %q", name, got, want)
		}
	}
}

func TestTheGroupDeploymentMountsTheRegistryAsADirectoryAndEachSecretOnce(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)

	h.reconcileAll()

	dep := h.group()
	pod := dep.Spec.Template.Spec
	c := pod.Containers[0]
	if c.Image != gqImage {
		t.Errorf("image = %q, want the KubemootConfig Go image", c.Image)
	}
	registryMounted, secretMounts := countMounts(t, c.VolumeMounts)
	if !registryMounted {
		t.Fatalf("no mount at %s: %+v", registryDir, c.VolumeMounts)
	}
	if secretMounts != 1 {
		t.Errorf("secret mounts = %d, want 1 for the Secret two sources share", secretMounts)
	}
	checkGroupContainerEnv(t, c)
	if c.ReadinessProbe.HTTPGet.Path != readyPath || c.LivenessProbe.HTTPGet.Path != "/health" {
		t.Errorf("probes = %+v / %+v", c.ReadinessProbe, c.LivenessProbe)
	}
	if got := dep.Spec.Selector.MatchLabels[labelQueryGroup]; got != gqGroup {
		t.Errorf("selector = %v, want the group label", dep.Spec.Selector.MatchLabels)
	}
}

// checkGroupContainerEnv says the container finds its registry and reads no Secret from its environment.
func checkGroupContainerEnv(t *testing.T, c corev1.Container) {
	t.Helper()
	if got := gqEnv(c.Env, "KUBEMOOT_REGISTRY_FILE"); got != registryDir+"/"+registryKey {
		t.Errorf("KUBEMOOT_REGISTRY_FILE = %q", got)
	}
	for _, e := range c.Env {
		if e.ValueFrom != nil {
			t.Errorf("env %s reads a Secret; credentials come from the mounted directory", e.Name)
		}
	}
}

// countMounts reports whether the registry directory is mounted and how many Secrets are, and
// fails the test for a mount that uses a subPath, which never receives an update.
func countMounts(t *testing.T, mounts []corev1.VolumeMount) (registryMounted bool, secretMounts int) {
	t.Helper()
	for _, m := range mounts {
		if m.SubPath != "" {
			t.Errorf("mount %s uses subPath %q, which never receives an update", m.Name, m.SubPath)
		}
		switch {
		case m.MountPath == registryDir:
			registryMounted = true
		case strings.HasPrefix(m.MountPath, credentialsRoot+"/"):
			secretMounts++
		}
	}
	return registryMounted, secretMounts
}

func gqEnv(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func gqDeploymentNames(deps []appsv1.Deployment) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.Name)
	}
	return out
}

func TestEveryLiveSourceOwnsTheGroupAndNoneControlsIt(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)

	h.reconcileAll()

	owners := h.group().OwnerReferences
	if len(owners) != 3 {
		t.Fatalf("owners = %+v, want the three RAGSources", owners)
	}
	for _, o := range owners {
		if o.Controller != nil && *o.Controller {
			t.Errorf("%s controls the group; no single RAGSource may decide alone when it goes", o.Name)
		}
	}
	cm := &corev1.ConfigMap{}
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: gqNS, Name: gqRegistry}, cm); err != nil {
		t.Fatal(err)
	}
	if len(cm.OwnerReferences) != 3 {
		t.Errorf("registry owners = %d, want 3", len(cm.OwnerReferences))
	}
}

func TestTwoNamespacesGetTwoGroups(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, append(threeSources(), gqSource("manual", gqOtherNS))...)

	h.reconcileAll()

	if len(h.deployments(gqNS)) != 1 || len(h.deployments(gqOtherNS)) != 1 {
		t.Fatalf("each namespace needs its own group: %d and %d", len(h.deployments(gqNS)), len(h.deployments(gqOtherNS)))
	}
	if len(h.registry(gqNS)) != 3 || len(h.registry(gqOtherNS)) != 1 {
		t.Errorf("registries hold %d and %d entries, want 3 and 1", len(h.registry(gqNS)), len(h.registry(gqOtherNS)))
	}
}

func TestAnIndexSwapChangesTheRegistryAndLeavesThePodsAlone(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)
	h.reconcileAll()
	before := h.group()
	ctx := context.Background()

	rs := &kubemootv1alpha1.RAGSource{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: gqNS, Name: gqDocsA}, rs); err != nil {
		t.Fatal(err)
	}
	rs.Status.Index = &kubemootv1alpha1.IndexProvenance{
		Collection: gqNewColl, Base: rs.Spec.VectorStore.Collection, Model: gqModel, Server: gqServer, Endpoint: gqEmbedURL, Dimensions: 768,
	}
	if err := h.c.Update(ctx, rs); err != nil {
		t.Fatal(err)
	}
	h.reconcileAll()

	if got := h.registry(gqNS)[gqDocsA].VectorStore.Collection; got != gqNewColl {
		t.Errorf("registry collection = %q, want the swapped-in %q", got, gqNewColl)
	}
	if got := h.registry(gqNS)[gqDocsB].VectorStore.Collection; got != "docs_b" {
		t.Errorf("an untouched source moved to %q", got)
	}
	after := h.group()
	if after.Annotations[deploymentHashAnnotation] != before.Annotations[deploymentHashAnnotation] || after.Generation != before.Generation {
		t.Error("a registry change rolled the pods; the mounted ConfigMap carries it")
	}
}

func TestDeletingASourceDropsItFromTheRegistryAndTheLastOneRemovesTheGroup(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)
	h.reconcileAll()
	ctx := context.Background()

	h.deleteSources(gqDocsA, gqDocsB)
	h.reconcileAll()

	reg := h.registry(gqNS)
	if _, ok := reg[gqDocsC]; !ok || len(reg) != 1 {
		t.Fatalf("registry = %v, want only %s", reg, gqDocsC)
	}
	if owners := h.group().OwnerReferences; len(owners) != 1 || owners[0].Name != gqDocsC {
		t.Errorf("owners = %+v, want only %s", owners, gqDocsC)
	}

	h.deleteSources(gqDocsC)
	if err := h.r.reconcileQueryGroup(ctx, gqNS); err != nil {
		t.Fatal(err)
	}
	if len(h.deployments(gqNS)) != 0 || len(h.services(gqNS)) != 0 || h.registryExists(gqNS) {
		t.Error("the group outlived its last RAGSource")
	}
}

func TestADeletedSourceIsDroppedBeforeItsFinalizerGoes(t *testing.T) {
	sources := threeSources()
	sources[0].Finalizers = []string{ragSourceFinalizer}
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, sources...)
	h.reconcileAll()
	ctx := context.Background()
	rs := &kubemootv1alpha1.RAGSource{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: gqNS, Name: gqDocsA}, rs); err != nil {
		t.Fatal(err)
	}
	if err := h.c.Delete(ctx, rs); err != nil {
		t.Fatal(err)
	}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: gqNS, Name: gqDocsA}, rs); err != nil {
		t.Fatal(err)
	}

	if _, err := h.r.handleDeletion(ctx, rs); err != nil {
		t.Fatal(err)
	}

	if _, ok := h.registry(gqNS)[gqDocsA]; ok {
		t.Error("the registry still lists the deleted RAGSource")
	}
	if len(h.registry(gqNS)) != 2 {
		t.Errorf("registry = %v, want the two others", h.registry(gqNS))
	}
}

func TestASourceWithTheQueryServiceOffIsNotServed(t *testing.T) {
	sources := threeSources()
	off := false
	sources[2].Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{Enabled: &off}
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, sources...)

	h.reconcileAll()

	if reg := h.registry(gqNS); len(reg) != 2 {
		t.Errorf("registry = %v, want the two sources that have a query service", reg)
	}
}

func TestASourceThatCannotBeServedYetKeepsItsEntryOrIsLeftOut(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)
	h.reconcileAll()
	ctx := context.Background()

	secret := gqSecretObject(gqNS)
	if err := h.c.Delete(ctx, secret); err != nil {
		t.Fatal(err)
	}
	fresh := gqSource("docs-d", gqNS)
	fresh.Spec.VectorStore.SecretRef = gqSecret
	if err := h.c.Create(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	h.reconcileAll()

	reg := h.registry(gqNS)
	if _, ok := reg[gqDocsA]; !ok {
		t.Error("a source whose Secret is briefly missing lost the entry it had")
	}
	if _, ok := reg["docs-d"]; ok {
		t.Error("a source whose Secret never existed is listed, and would keep the registry from loading")
	}
}

func TestASourceOnAnotherVectorStoreIsNotInTheGoRegistry(t *testing.T) {
	sources := threeSources()
	sources[1].Spec.VectorStore.Type = kubemootv1alpha1.VectorStoreQdrant
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, sources...)

	h.reconcileAll()

	reg := h.registry(gqNS)
	if _, ok := reg[gqDocsB]; ok || len(reg) != 2 {
		t.Errorf("registry = %v, want the two pgvector sources: the service refuses a file with another type", reg)
	}
}

func TestResolveGroupSettingsPerGroupConflicts(t *testing.T) {
	big := &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")}}
	small := &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("96Mi")}}
	mk := func(name string, qs *kubemootv1alpha1.QueryServiceConfig) *kubemootv1alpha1.RAGSource {
		rs := gqSource(name, gqNS)
		rs.Spec.QueryService = qs
		return rs
	}
	tests := []struct {
		name         string
		sources      []*kubemootv1alpha1.RAGSource
		replicas     int32
		port         int32
		memoryLimit  string
		defaultLimit bool
	}{
		{"no settings", []*kubemootv1alpha1.RAGSource{mk("a", nil)}, 1, 8000, "", true},
		{"replicas: the largest wins", []*kubemootv1alpha1.RAGSource{
			mk("a", &kubemootv1alpha1.QueryServiceConfig{Replicas: 2}), mk("b", &kubemootv1alpha1.QueryServiceConfig{Replicas: 3})}, 3, 8000, "", true},
		{"resources: the first by name that sets them wins", []*kubemootv1alpha1.RAGSource{
			mk("a", &kubemootv1alpha1.QueryServiceConfig{Resources: small}),
			mk("b", &kubemootv1alpha1.QueryServiceConfig{Resources: big})}, 1, 8000, "96Mi", false},
		{"a later source fills in what an earlier one left unset", []*kubemootv1alpha1.RAGSource{
			mk("a", &kubemootv1alpha1.QueryServiceConfig{}), mk("b", &kubemootv1alpha1.QueryServiceConfig{Resources: big})}, 1, 8000, "2Gi", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveQueryGroupSettings(tc.sources)
			mem := got.Resources.Limits[corev1.ResourceMemory]
			want := tc.memoryLimit
			if tc.defaultLimit {
				defaults := defaultGoQueryResources().Limits[corev1.ResourceMemory]
				want = defaults.String()
			}
			if got.Replicas != tc.replicas || got.Port != tc.port || mem.String() != want {
				t.Errorf("settings = replicas %d port %d memory %s, want %d %d %s", got.Replicas, got.Port, mem.String(), tc.replicas, tc.port, want)
			}
		})
	}
}

func TestASourcesImageOverrideNamesAPythonImageAndIsNotUsedByTheGoEngine(t *testing.T) {
	sources := threeSources()
	sources[0].Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{Image: "registry.example/query-service:1.0"}
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, sources...)

	h.reconcileAll()

	if got := h.group().Spec.Template.Spec.Containers[0].Image; got != gqImage {
		t.Errorf("image = %q, want the Go image of the KubemootConfig", got)
	}
}

func TestAPerSourceTopKBecomesTheSourcesDefaultInTheRegistry(t *testing.T) {
	sources := threeSources()
	sources[0].Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{TopK: 8}
	sources[1].Spec.QueryService = &kubemootv1alpha1.QueryServiceConfig{TopK: 500}
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, sources...)

	h.reconcileAll()

	reg := h.registry(gqNS)
	if reg[gqDocsA].DefaultTopK != 8 || reg[gqDocsB].DefaultTopK != 100 || reg[gqDocsC].DefaultTopK != 0 {
		t.Errorf("defaultTopK = %d, %d, %d; want 8, 100 (cut to the most a query may ask for), 0",
			reg[gqDocsA].DefaultTopK, reg[gqDocsB].DefaultTopK, reg[gqDocsC].DefaultTopK)
	}
}

func TestSwitchingFromPythonToGoKeepsThePerSourcePodsUntilTheGroupIsUp(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEnginePython, threeSources()...)
	h.reconcileAll()
	if len(h.deployments(gqNS)) != 3 {
		t.Fatalf("python engine runs %d Deployments, want one per source", len(h.deployments(gqNS)))
	}

	h.setEngine(kubemootv1alpha1.QueryServiceEngineGo)
	h.reconcileAll()
	if len(h.deployments(gqNS)) != 4 {
		t.Fatalf("Deployments = %v, want the three old ones kept beside the new group until it is available", gqDeploymentNames(h.deployments(gqNS)))
	}

	h.markAvailable(gqGroup)
	h.reconcileAll()
	deps := h.deployments(gqNS)
	if len(deps) != 1 || deps[0].Name != gqGroup {
		t.Fatalf("Deployments = %v, want only the group once it is available", gqDeploymentNames(deps))
	}
	if svcs := h.services(gqNS); len(svcs) != 1 || svcs[0].Name != gqGroup {
		t.Errorf("services = %d, want only the group Service", len(svcs))
	}
}

func TestSwitchingFromGoToPythonKeepsTheGroupUntilEverySourceHasItsOwnPod(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)
	h.reconcileAll()

	h.setEngine(kubemootv1alpha1.QueryServiceEnginePython)
	h.reconcileAll()
	if len(h.deployments(gqNS)) != 4 {
		t.Fatalf("Deployments = %v, want the group kept beside the three new ones", gqDeploymentNames(h.deployments(gqNS)))
	}

	for _, name := range []string{"docs-a-query", "docs-b-query", "docs-c-query"} {
		h.markAvailable(name)
	}
	h.reconcileAll()
	if deps := h.deployments(gqNS); len(deps) != 3 {
		t.Fatalf("Deployments = %v, want the group gone and one per source", gqDeploymentNames(deps))
	}
	if h.registryExists(gqNS) {
		t.Error("the registry ConfigMap outlived the Go engine")
	}
}

func TestPythonEngineLeavesTheClusterAsItWas(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEnginePython, threeSources()...)

	h.reconcileAll()

	for _, d := range h.deployments(gqNS) {
		if d.Labels[labelQueryGroup] != "" || !strings.HasSuffix(d.Name, "-query") || d.Spec.Template.Spec.Containers[0].Image != "ghcr.io/kubemoot/query-service:9.9.9" {
			t.Errorf("Deployment %s is not a per-source Python Deployment: %+v", d.Name, d.Labels)
		}
	}
	if h.registryExists(gqNS) {
		t.Error("the python engine wrote a registry")
	}
	rs := &kubemootv1alpha1.RAGSource{}
	if err := h.c.Get(context.Background(), types.NamespacedName{Namespace: gqNS, Name: gqDocsA}, rs); err != nil {
		t.Fatal(err)
	}
	h.r.reconcileQueryService(context.Background(), rs, gqEmbeddingModel(gqNS))
	if want := "http://docs-a-query.crew-a:8000"; rs.Status.QueryEndpoint != want {
		t.Errorf("queryEndpoint = %q, want %q", rs.Status.QueryEndpoint, want)
	}
}

func TestASourceNamedLikeItsNamespaceDoesNotCollideWithTheGroup(t *testing.T) {
	same := gqSource(gqNS, gqNS)
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEnginePython, same, gqSource(gqDocsA, gqNS))
	h.reconcileAll()
	if h.group().Labels[labelQueryGroup] != "" {
		t.Fatal("the python Deployment of the source named like its namespace is labelled as a group")
	}

	h.setEngine(kubemootv1alpha1.QueryServiceEngineGo)
	h.reconcileAll()

	if h.group().Labels[labelQueryGroup] != gqGroup {
		t.Error("the legacy Deployment with the group's name was not replaced by the group's")
	}
	if len(h.registry(gqNS)) != 2 {
		t.Errorf("registry = %v, want both sources", h.registry(gqNS))
	}
}

func TestReadersMoveOnlyWhenEveryPodServesTheNewCollectionOnTheCurrentRegistry(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)
	h.reconcileAll()
	h.markAvailable(gqGroup)
	ctx := context.Background()
	cm := &corev1.ConfigMap{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: gqNS, Name: gqRegistry}, cm); err != nil {
		t.Fatal(err)
	}
	version := registryVersion([]byte(cm.Data[registryKey]))
	for i, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("q-%d", i), Namespace: gqNS, Labels: queryGroupSelector(gqNS)},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
		}
		if err := h.c.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
	rs := &kubemootv1alpha1.RAGSource{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: gqNS, Name: gqDocsA}, rs); err != nil {
		t.Fatal(err)
	}
	body := func(v, coll string) string {
		return fmt.Sprintf(`{"status":"ready","registry":{"version":%q,"sources":{"docs-a":%q}}}`, v, coll)
	}

	h.ready.bodies["10.0.0.1:8000"] = body(version, gqNewColl)
	h.ready.bodies["10.0.0.2:8000"] = body("oldversion00", "docs_a")
	if h.r.groupReadersMoved(ctx, rs, gqNewColl) {
		t.Fatal("one pod still serves the old registry, so the old collection may not be dropped")
	}
	h.ready.bodies["10.0.0.2:8000"] = body(version, "docs_a")
	if h.r.groupReadersMoved(ctx, rs, gqNewColl) {
		t.Fatal("a pod on the current registry that reads the old collection has not moved")
	}
	h.ready.bodies["10.0.0.2:8000"] = body(version, gqNewColl)
	if !h.r.groupReadersMoved(ctx, rs, gqNewColl) {
		t.Fatal("every pod serves the new collection on the current registry, so the readers moved")
	}
}

func TestReadersHaveNotMovedWhileAPodCannotBeAsked(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)
	h.reconcileAll()
	h.markAvailable(gqGroup)
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "q", Namespace: gqNS, Labels: queryGroupSelector(gqNS)},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.9"},
	}
	if err := h.c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	rs := &kubemootv1alpha1.RAGSource{}
	if err := h.c.Get(ctx, types.NamespacedName{Namespace: gqNS, Name: gqDocsA}, rs); err != nil {
		t.Fatal(err)
	}

	if h.r.groupReadersMoved(ctx, rs, "docs_a") {
		t.Error("an unreachable pod counted as moved")
	}
}

func TestAFinishedSwapPollsUntilTheOldCollectionCanGo(t *testing.T) {
	h := newGQHarness(t, kubemootv1alpha1.QueryServiceEngineGo, threeSources()...)
	rs := gqSource(gqDocsA, gqNS)

	if got := h.r.pollWhileReadersMove(rs, gqResult(0)).RequeueAfter; got != 0 {
		t.Errorf("no retired collection, requeue = %v, want none", got)
	}
	rs.Status.RetiredCollections = []string{"docs_a"}
	if got := h.r.pollWhileReadersMove(rs, gqResult(0)).RequeueAfter; got != readersPollInterval {
		t.Errorf("requeue = %v, want %v", got, readersPollInterval)
	}
	if got := h.r.pollWhileReadersMove(rs, gqResult(300)).RequeueAfter; got != readersPollInterval {
		t.Errorf("a long requeue = %v, want it cut to %v", got, readersPollInterval)
	}
	h.setEngine(kubemootv1alpha1.QueryServiceEnginePython)
	if got := h.r.pollWhileReadersMove(rs, gqResult(0)).RequeueAfter; got != 0 {
		t.Errorf("python engine requeue = %v, want none: its readers show in the Deployment status", got)
	}
}

func TestQueryGroupNameFitsAServiceName(t *testing.T) {
	if got := queryGroupName("crew-a"); got != gqGroup {
		t.Errorf("name = %q", got)
	}
	long := strings.Repeat("n", 63)
	a, b := queryGroupName(long), queryGroupName(long[:62]+"m")
	if len(a) > 63 || len(b) > 63 || !strings.HasSuffix(a, "-query") {
		t.Errorf("names %q and %q must fit a Service name", a, b)
	}
	if a == b {
		t.Error("two long namespaces share a group name")
	}
	if got := queryRegistryName("crew-a"); got != gqRegistry {
		t.Errorf("registry name = %q", got)
	}
}

func TestRagQueryEndpointForEachEngine(t *testing.T) {
	if got, want := ragQueryEndpoint(kubemootv1alpha1.QueryServiceEnginePython, "docs", "ns", 8000), gqPyDocsURL; got != want {
		t.Errorf("python = %q, want %q", got, want)
	}
	if got, want := ragQueryEndpoint(kubemootv1alpha1.QueryServiceEngineGo, "docs", "ns", 9100), "http://ns-query.ns:8000/sources/docs"; got != want {
		t.Errorf("go = %q, want %q", got, want)
	}
	if got, want := ragQueryEndpoint("", "docs", "ns", 8000), gqPyDocsURL; got != want {
		t.Errorf("an unset engine = %q, want the python form %q", got, want)
	}
}

func TestResumeSearchEndpointForEachEngine(t *testing.T) {
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: gqNS}}
	if got, want := resumeSearchEndpoint(kubemootv1alpha1.QueryServiceEnginePython, agent, "pilot"), "http://crew-pilot-resumes-query.crew-a:8000"; got != want {
		t.Errorf("python = %q, want %q", got, want)
	}
	if got, want := resumeSearchEndpoint(kubemootv1alpha1.QueryServiceEngineGo, agent, "pilot"), "http://crew-a-query.crew-a:8000/sources/crew-pilot-resumes"; got != want {
		t.Errorf("go = %q, want %q", got, want)
	}
}

func TestTheAgentsFallbackEndpointFollowsTheEngine(t *testing.T) {
	cache := NewConfigCache()
	r := &AgentReconciler{Client: rbacTestClient(t), ConfigCache: cache}
	got, ok := r.ragQueryEndpointFor(context.Background(), "docs", gqNS)
	if !ok || got != "http://docs-query.crew-a:8000" {
		t.Errorf("python fallback = %q %v", got, ok)
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		QueryService: &kubemootv1alpha1.QueryServiceSettings{Engine: kubemootv1alpha1.QueryServiceEngineGo},
	}})
	got, ok = r.ragQueryEndpointFor(context.Background(), "docs", gqNS)
	if !ok || got != "http://crew-a-query.crew-a:8000/sources/docs" {
		t.Errorf("go fallback = %q %v", got, ok)
	}
}

func TestTheEngineIsPythonUnlessTheConfigSaysGo(t *testing.T) {
	var nilCache *ConfigCache
	cache := NewConfigCache()
	cases := map[string]kubemootv1alpha1.QueryServiceEngine{
		"nil cache": nilCache.GetQueryServiceEngine(), "no config": cache.GetQueryServiceEngine(),
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{})
	cases["no settings"] = cache.GetQueryServiceEngine()
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{QueryService: &kubemootv1alpha1.QueryServiceSettings{Engine: "rust"}}})
	cases["unknown value"] = cache.GetQueryServiceEngine()
	for name, got := range cases {
		if got != kubemootv1alpha1.QueryServiceEnginePython {
			t.Errorf("%s: engine = %q, want python", name, got)
		}
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{QueryService: &kubemootv1alpha1.QueryServiceSettings{Engine: kubemootv1alpha1.QueryServiceEngineGo}}})
	if cache.GetQueryServiceEngine() != kubemootv1alpha1.QueryServiceEngineGo {
		t.Error("engine go in the config was not honored")
	}
	if got := NewConfigCache().GetQueryServiceGoImage(); got != FallbackQueryServiceGoImage {
		t.Errorf("fallback image = %q", got)
	}
}

func TestRegistryBytesAndVersionAreStable(t *testing.T) {
	a := queryRegistrySource{Name: "b", Namespace: "ns"}
	b := queryRegistrySource{Name: "a", Namespace: "ns"}
	one, err := marshalRegistry([]queryRegistrySource{a, b})
	if err != nil {
		t.Fatal(err)
	}
	two, err := marshalRegistry([]queryRegistrySource{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if string(one) != string(two) || registryVersion(one) != registryVersion(two) {
		t.Error("the order the sources came in changed the registry")
	}
	if len(registryVersion(one)) != registryVersionLength {
		t.Errorf("version %q is not %d characters", registryVersion(one), registryVersionLength)
	}
	empty, err := marshalRegistry(nil)
	if err != nil || string(empty) != `{"sources":[]}` {
		t.Errorf("an empty registry = %s, %v; the service needs sources to be a list", empty, err)
	}
	if parseRegistry("not json") != nil {
		t.Error("an unreadable registry read as entries")
	}
}

// gqResult is a reconcile result that asks to run again after the given seconds.
func gqResult(seconds int) ctrl.Result {
	return ctrl.Result{RequeueAfter: time.Duration(seconds) * time.Second}
}
