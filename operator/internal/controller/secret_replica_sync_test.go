package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	syncSecretName    = "harbor-pull-secret"
	syncTargetNS      = "crew-a"
	syncAnnotationRef = "shared/db"
	syncForeignData   = "mine"
)

func syncSecret(ns, pw string, labels map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: syncSecretName, Namespace: ns, Labels: labels},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{".dockerconfigjson": []byte(pw)},
	}
}

func replicaLabels(from string) map[string]string {
	return map[string]string{labelManagedBy: managedByValue, labelReplicatedFrom: from}
}

// syncClient returns a fake client that counts Create and Update calls.
func syncClient(t *testing.T, writes *int, objs ...client.Object) client.Client {
	t.Helper()
	t.Setenv("OPERATOR_NAMESPACE", testOperatorNS)
	t.Setenv(secretSourceNamespacesEnv, "")
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			*writes++
			return c.Create(ctx, o, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			*writes++
			return c.Update(ctx, o, opts...)
		},
	}).Build()
}

func getSecret(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: syncSecretName, Namespace: syncTargetNS}, s); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	return s
}

func TestReplicateSecret_CreatesMissingCopy(t *testing.T) {
	var writes int
	c := syncClient(t, &writes, syncSecret(testOperatorNS, "new", nil))
	replicateSecret(context.Background(), c, syncSecretName, syncTargetNS)
	got := getSecret(t, c)
	if string(got.Data[".dockerconfigjson"]) != "new" || got.Labels[labelReplicatedFrom] != testOperatorNS || got.Labels[labelManagedBy] != managedByValue {
		t.Fatalf("unexpected copy: %+v", got)
	}
}

func TestReplicateSecret_UpdatesCopyWhenSourceChanges(t *testing.T) {
	var writes int
	c := syncClient(t, &writes,
		syncSecret(testOperatorNS, "rotated", nil),
		syncSecret(syncTargetNS, "stale", replicaLabels(testOperatorNS)))
	replicateSecret(context.Background(), c, syncSecretName, syncTargetNS)
	if got := getSecret(t, c); string(got.Data[".dockerconfigjson"]) != "rotated" {
		t.Fatalf("copy not updated: %q", got.Data)
	}
	if writes != 1 {
		t.Fatalf("expected one write, got %d", writes)
	}
}

func TestReplicateSecret_UnchangedCopyIsNotWritten(t *testing.T) {
	var writes int
	c := syncClient(t, &writes,
		syncSecret(testOperatorNS, "same", nil),
		syncSecret(syncTargetNS, "same", replicaLabels(testOperatorNS)))
	replicateSecret(context.Background(), c, syncSecretName, syncTargetNS)
	if writes != 0 {
		t.Fatalf("expected no writes, got %d", writes)
	}
}

func TestReplicateSecret_NeverOverwritesForeignSecret(t *testing.T) {
	cases := map[string]map[string]string{
		"unlabeled":               nil,
		"managed by someone else": {labelManagedBy: "helm", labelReplicatedFrom: testOperatorNS},
		"replicated from other":   replicaLabels("elsewhere"),
	}
	for name, labels := range cases {
		t.Run(name, func(t *testing.T) {
			var writes int
			c := syncClient(t, &writes,
				syncSecret(testOperatorNS, "source", nil),
				syncSecret(syncTargetNS, syncForeignData, labels))
			replicateSecret(context.Background(), c, syncSecretName, syncTargetNS)
			if got := getSecret(t, c); string(got.Data[".dockerconfigjson"]) != syncForeignData || writes != 0 {
				t.Fatalf("foreign secret was modified (writes=%d, data=%q)", writes, got.Data)
			}
		})
	}
}

func TestReplicateSecret_DisallowedSourceNeverUpdates(t *testing.T) {
	var writes int
	c := syncClient(t, &writes,
		syncSecret("tenant", "evil", nil),
		syncSecret(syncTargetNS, "stale", replicaLabels("tenant")))
	replicateSecretFrom(context.Background(), c, syncSecretName, "tenant", syncTargetNS)
	if got := getSecret(t, c); string(got.Data[".dockerconfigjson"]) != "stale" || writes != 0 {
		t.Fatalf("disallowed source changed a copy (writes=%d)", writes)
	}
}

func TestReplicateSecret_SourceDeletionKeepsCopies(t *testing.T) {
	var writes int
	c := syncClient(t, &writes, syncSecret(syncTargetNS, "kept", replicaLabels(testOperatorNS)))
	replicateSecret(context.Background(), c, syncSecretName, syncTargetNS)
	if got := getSecret(t, c); string(got.Data[".dockerconfigjson"]) != "kept" || writes != 0 {
		t.Fatalf("copy changed after source deletion (writes=%d)", writes)
	}
}

func pullCache(names ...string) *ConfigCache {
	cc := NewConfigCache()
	cfg := &kubemootv1alpha1.KubemootConfig{}
	for _, n := range names {
		cfg.Spec.Defaults.ImagePullSecrets = append(cfg.Spec.Defaults.ImagePullSecrets, corev1.LocalObjectReference{Name: n})
	}
	cc.Update(cfg)
	return cc
}

func TestSourceSecretPredicate(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", testOperatorNS)
	t.Setenv(secretSourceNamespacesEnv, "")
	p := sourceSecretPredicate()
	old := syncSecret(testOperatorNS, "a", nil)
	changed := syncSecret(testOperatorNS, "b", nil)
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: changed}) {
		t.Error("changed source data must pass")
	}
	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: old.DeepCopy()}) {
		t.Error("unchanged data must not pass")
	}
	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: syncSecret("tenant", "b", nil)}) {
		t.Error("secret outside the allowed sources must not pass")
	}
	if p.Delete(event.DeleteEvent{Object: old}) {
		t.Error("source deletion must not pass")
	}
	if !p.Create(event.CreateEvent{Object: old}) || p.Create(event.CreateEvent{Object: syncSecret("tenant", "a", nil)}) {
		t.Error("create filter wrong")
	}
}

func TestSecretMappers(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", testOperatorNS)
	t.Setenv(secretSourceNamespacesEnv, "shared")
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kubemootv1alpha1.AddToScheme(scheme)
	mk := func(name string, ann map[string]string, crew bool) *corev1.Namespace {
		l := map[string]string{}
		if crew {
			l["kubemoot.ai/crew"] = "x"
		}
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l, Annotations: ann}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mk("plain", nil, true),
		mk("annotated", map[string]string{testReplicateSecretsKey: syncAnnotationRef}, true),
		mk("unlabeled", map[string]string{testReplicateSecretsKey: syncAnnotationRef}, false),
		&kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "plain"}},
	).Build()
	cache := pullCache(syncSecretName)

	db := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shared"}}
	if !namespaceReplicates(mk("a", map[string]string{testReplicateSecretsKey: syncAnnotationRef}, true), db) {
		t.Error("annotation shared/db must match secret db in shared")
	}
	if namespaceReplicates(mk("a", map[string]string{testReplicateSecretsKey: "db"}, true), db) {
		t.Error("bare name resolves to the operator namespace, not shared")
	}

	reqs := secretToNamespaceRequests(context.Background(), c, cache, db)
	if len(reqs) != 1 || reqs[0].Name != "annotated" {
		t.Errorf("annotation secret: got %v", reqs)
	}
	pull := syncSecret(testOperatorNS, "x", nil)
	reqs = secretToNamespaceRequests(context.Background(), c, cache, pull)
	if len(reqs) != 2 {
		t.Errorf("pull secret must enqueue both crew namespaces, got %v", reqs)
	}
	if got := secretToCrewRequests(context.Background(), c, cache, pull); len(got) != 1 || got[0].Name != "c1" {
		t.Errorf("pull secret must enqueue the crew, got %v", got)
	}
	if got := secretToCrewRequests(context.Background(), c, cache, db); len(got) != 0 {
		t.Errorf("non-pull secret must not enqueue crews, got %v", got)
	}
}
