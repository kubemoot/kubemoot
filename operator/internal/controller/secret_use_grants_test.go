/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"fmt"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	grantsNamespace  = grantsReleaseNS
	grantsOperator   = "system:serviceaccount:" + grantsReleaseNS + ":" + grantsReleaseSA
	grantsK8sMCPUser = "system:serviceaccount:" + grantsReleaseNS + ":kubemoot-kubernetes-mcp"
	grantsExempt     = "exempt-admin"
	grantsAuthor     = "crew-author"
	grantsImage      = "tool:1"
	grantsRoleKind   = "Role"
	grantsAgents     = "agents"
)

// collectedWarnings records the warnings an API server sends back, for the Audit/Warn spec.
type collectedWarnings struct {
	mu   sync.Mutex
	text []string
}

func (c *collectedWarnings) HandleWarningHeader(_ int, _ string, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.text = append(c.text, text)
}

func (c *collectedWarnings) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.text, "\n")
}

// The policies the chart ships behind security.secretUseGrants.enabled. These specs render the
// chart with helm, apply the result to a real API server and ask as impersonated users, so the
// template, the CEL, the authorizer calls and the rejection text are the ones a cluster runs.
var _ = Describe("Secret use grants", Ordered, func() {
	var seq int

	// grantUse gives user the verb "use" on the named Secrets or ServiceAccounts.
	grantUse := func(user, resource string, names ...string) {
		seq++
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("grant-%d", seq), Namespace: grantsNamespace},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{resource}, Verbs: []string{"use"}, ResourceNames: names},
			},
		}
		Expect(k8sClient.Create(ctx, role)).To(Succeed())
		binding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: grantsNamespace},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user, APIGroup: rbacv1.GroupName}},
			RoleRef:    rbacv1.RoleRef{Kind: grantsRoleKind, Name: role.Name, APIGroup: rbacv1.GroupName},
		}
		Expect(k8sClient.Create(ctx, binding)).To(Succeed())
	}

	asUserWith := func(user string, warnings rest.WarningHandler) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user}
		if warnings != nil {
			c.WarningHandler = warnings
		}
		cl, err := client.New(c, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		return cl
	}
	asUser := func(user string) client.Client { return asUserWith(user, nil) }

	mcp := func(spec kubemootv1alpha1.MCPServerSpec) *kubemootv1alpha1.MCPServer {
		seq++
		spec.Image, spec.Transport = grantsImage, kubemootv1alpha1.TransportHTTP
		return &kubemootv1alpha1.MCPServer{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("m%d", seq), Namespace: grantsNamespace}, Spec: spec}
	}
	agent := func(spec kubemootv1alpha1.AgentSpec) *kubemootv1alpha1.Agent {
		seq++
		return &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("a%d", seq), Namespace: grantsNamespace}, Spec: spec}
	}
	// create submits obj as user and removes it afterwards when it was admitted.
	create := func(user string, obj client.Object) error {
		err := asUser(user).Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		}
		return err
	}
	// adminCreate makes obj as the cluster admin, which no policy checks.
	adminCreate := func(obj client.Object) {
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
	}
	expectDenied := func(err error, mentions ...string) {
		Expect(apierrors.IsForbidden(err)).To(BeTrue(), "want Forbidden, got %v", err)
		for _, m := range mentions {
			Expect(err.Error()).To(ContainSubstring(m))
		}
	}
	secretEnv := func(secret string) []corev1.EnvVar {
		return []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: "k"},
		}}}
	}
	deployment := func(d kubemootv1alpha1.DeploymentConfig) kubemootv1alpha1.AgentSpec {
		return kubemootv1alpha1.AgentSpec{Deployment: &d}
	}

	BeforeAll(func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: grantsNamespace}})).To(Succeed())
		for _, res := range []string{grantsMCPServers, grantsAgents} {
			role := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{Name: "write-" + res, Namespace: grantsNamespace},
				Rules: []rbacv1.PolicyRule{{APIGroups: []string{"kubemoot.ai"}, Resources: []string{res, res + "/status"},
					Verbs: []string{"get", "create", "update", "patch", "delete"}}},
			}
			Expect(k8sClient.Create(ctx, role)).To(Succeed())
			for _, user := range []string{grantsAuthor, grantsOperator, grantsExempt, grantsK8sMCPUser} {
				Expect(k8sClient.Create(ctx, &rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: role.Name + "-" + strings.NewReplacer(":", "-").Replace(user), Namespace: grantsNamespace},
					Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user, APIGroup: rbacv1.GroupName}},
					RoleRef:    rbacv1.RoleRef{Kind: grantsRoleKind, Name: role.Name, APIGroup: rbacv1.GroupName},
				})).To(Succeed())
			}
		}
	})

	Context("with the policy not installed (the chart flag off)", func() {
		It("lets an author reference any Secret and ServiceAccount", func() {
			m := mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "anything", ServiceAccountName: "anyone", Env: secretEnv("another")})
			Expect(create(grantsAuthor, m)).To(Succeed())
			a := agent(deployment(kubemootv1alpha1.DeploymentConfig{ServiceAccountName: "anyone"}))
			Expect(create(grantsAuthor, a)).To(Succeed())
		})
	})

	Context("with the policy installed and denying (the chart flag on)", func() {
		var installed []*unstructured.Unstructured
		BeforeAll(func() {
			installed = installUseGrants("--set", "security.secretUseGrants.exemptUsers={"+grantsExempt+"}")
		})
		AfterAll(func() { removeUseGrants(installed) })

		It("rejects an MCPServer secret reference without a grant and names the Secret", func() {
			err := create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "mail-credentials"}))
			expectDenied(err, "secrets/mail-credentials", grantsAuthor, `verb "use"`)
		})

		It("admits an MCPServer whose Secret the author holds use on", func() {
			grantUse(grantsAuthor, "secrets", "mail-ok")
			Expect(create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "mail-ok"}))).To(Succeed())
		})

		It("keeps a grant to one Secret from covering another", func() {
			grantUse(grantsAuthor, "secrets", "only-this")
			err := create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "only-this", Env: secretEnv("not-this")}))
			expectDenied(err, "secrets/not-this")
			Expect(err.Error()).NotTo(ContainSubstring("secrets/only-this"))
		})

		It("rejects every other way an MCPServer can name a Secret", func() {
			from := corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "via-sidecar-from"}}}
			cases := map[string]kubemootv1alpha1.MCPServerSpec{
				"env-secretKeyRef": {Env: secretEnv("via-env")},
				"secretVolumes":    {SecretVolumes: []kubemootv1alpha1.SecretVolume{{Name: "via-volume", MountPath: "/etc/s"}}},
				"registry-auth":    {Registry: &kubemootv1alpha1.RegistryConfig{AuthSecretRef: "via-auth"}},
				"sidecar-env":      {Sidecars: []corev1.Container{{Name: "s", Image: grantsImage, Env: secretEnv("via-sidecar-env")}}},
				"sidecar-envFrom":  {Sidecars: []corev1.Container{{Name: "s", Image: grantsImage, EnvFrom: []corev1.EnvFromSource{from}}}},
			}
			for name, spec := range cases {
				err := create(grantsAuthor, mcp(spec))
				Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%s: want Forbidden, got %v", name, err)
			}
			// The message names the field and every Secret the requester needs the grant on there.
			err := create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{SecretVolumes: []kubemootv1alpha1.SecretVolume{
				{Name: "vol-a", MountPath: "/a"}, {Name: "vol-b", MountPath: "/b"}}}))
			expectDenied(err, "spec.secretVolumes", "secrets/vol-a", "secrets/vol-b")
		})

		It("rejects an MCPServer ServiceAccount without a grant and admits it with one", func() {
			err := create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{ServiceAccountName: "cluster-reader"}))
			expectDenied(err, "serviceaccounts/cluster-reader")
			grantUse(grantsAuthor, "serviceaccounts", "cluster-reader")
			Expect(create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{ServiceAccountName: "cluster-reader"}))).To(Succeed())
		})

		It("treats the default ServiceAccount as no reference", func() {
			Expect(create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{ServiceAccountName: defaultServiceAccount}))).To(Succeed())
		})

		It("admits references that are not Secrets, and a Secret reference with no name", func() {
			configMap := corev1.EnvFromSource{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}
			env := []corev1.EnvVar{
				{Name: "A", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Key: "k"}}},
				{Name: "B", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
				{Name: "C", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "k"}}},
			}
			spec := kubemootv1alpha1.MCPServerSpec{Env: env, Sidecars: []corev1.Container{{Name: "s", Image: grantsImage, EnvFrom: []corev1.EnvFromSource{configMap}}}}
			Expect(create(grantsAuthor, mcp(spec))).To(Succeed())
		})

		It("rejects an Agent that names a Secret or ServiceAccount without a grant", func() {
			cases := map[string]kubemootv1alpha1.AgentSpec{
				"deployment-env": deployment(kubemootv1alpha1.DeploymentConfig{Env: secretEnv("agent-env")}),
				"harness-env":    {Harness: &kubemootv1alpha1.HarnessSpec{Name: "h", Env: secretEnv("agent-harness")}},
				"memory":         {Memory: &kubemootv1alpha1.MemoryConfig{SecretRef: "agent-memory"}},
				"serviceaccount": deployment(kubemootv1alpha1.DeploymentConfig{ServiceAccountName: "agent-sa"}),
			}
			for name, spec := range cases {
				err := create(grantsAuthor, agent(spec))
				Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%s: want Forbidden, got %v", name, err)
			}
			grantUse(grantsAuthor, "secrets", "agent-ok")
			Expect(create(grantsAuthor, agent(deployment(kubemootv1alpha1.DeploymentConfig{Env: secretEnv("agent-ok")})))).To(Succeed())
		})

		It("admits an Agent with no deployment and no harness", func() {
			Expect(create(grantsAuthor, agent(kubemootv1alpha1.AgentSpec{}))).To(Succeed())
		})

		It("checks a spec change on update but lets metadata-only writes through", func() {
			m := mcp(kubemootv1alpha1.MCPServerSpec{})
			adminCreate(m)
			author := asUser(grantsAuthor)
			current := &kubemootv1alpha1.MCPServer{}
			Expect(author.Get(ctx, client.ObjectKeyFromObject(m), current)).To(Succeed())
			current.Labels = map[string]string{"touched": "yes"}
			Expect(author.Update(ctx, current)).To(Succeed(), "a metadata-only update needs no grant")

			current.Spec.SecretRef = "added-later"
			expectDenied(author.Update(ctx, current), "secrets/added-later")
		})

		It("does the same for an Agent update", func() {
			a := agent(kubemootv1alpha1.AgentSpec{})
			adminCreate(a)
			author := asUser(grantsAuthor)
			current := &kubemootv1alpha1.Agent{}
			Expect(author.Get(ctx, client.ObjectKeyFromObject(a), current)).To(Succeed())
			current.Labels = map[string]string{"touched": "yes"}
			Expect(author.Update(ctx, current)).To(Succeed())

			current.Spec.Memory = &kubemootv1alpha1.MemoryConfig{SecretRef: "agent-added-later"}
			expectDenied(author.Update(ctx, current), "secrets/agent-added-later")
		})

		It("does not stop an author without use from changing the rest of a spec that already references a Secret", func() {
			// Documented: an update that changes spec is checked in full, so an author with no grant
			// on an already-referenced Secret cannot change that resource's spec at all.
			m := mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "admin-owned"})
			adminCreate(m)
			author := asUser(grantsAuthor)
			current := &kubemootv1alpha1.MCPServer{}
			Expect(author.Get(ctx, client.ObjectKeyFromObject(m), current)).To(Succeed())
			current.Spec.Port = 4000
			expectDenied(author.Update(ctx, current), "secrets/admin-owned")
		})

		It("leaves the status subresource alone", func() {
			m := mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "status-secret"})
			adminCreate(m)
			author := asUser(grantsAuthor)
			current := &kubemootv1alpha1.MCPServer{}
			Expect(author.Get(ctx, client.ObjectKeyFromObject(m), current)).To(Succeed())
			current.Status.Phase = phaseReady
			Expect(author.Status().Update(ctx, current)).To(Succeed())
		})

		It("exempts the operator and the configured exempt users", func() {
			Expect(create(grantsOperator, mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "operator-made"}))).To(Succeed())
			Expect(create(grantsExempt, mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "admin-made"}))).To(Succeed())
		})

		It("lets a cluster admin through without any grant", func() {
			adminCreate(mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "admin-secret", ServiceAccountName: "admin-sa"}))
		})

		It("lets the kubernetes MCP server's account keep its own ServiceAccount, and nothing else", func() {
			own := mcp(kubemootv1alpha1.MCPServerSpec{ServiceAccountName: "kubemoot-kubernetes-mcp"})
			adminCreate(own)
			k8sMCP := asUser(grantsK8sMCPUser)
			current := &kubemootv1alpha1.MCPServer{}
			Expect(k8sMCP.Get(ctx, client.ObjectKeyFromObject(own), current)).To(Succeed())
			current.Spec.Port = 4001
			Expect(k8sMCP.Update(ctx, current)).To(Succeed(), "the chart grants use on its own ServiceAccount")

			expectDenied(create(grantsK8sMCPUser, mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "onboarded-secret"})), "secrets/onboarded-secret")
			expectDenied(create(grantsK8sMCPUser, mcp(kubemootv1alpha1.MCPServerSpec{ServiceAccountName: "other-account"})), "serviceaccounts/other-account")
		})
	})

	Context("with the policy set to Audit and Warn", func() {
		var installed []*unstructured.Unstructured
		BeforeAll(func() {
			installed = installUseGrants("--set", "security.secretUseGrants.validationActions={Audit,Warn}")
		})
		AfterAll(func() { removeUseGrants(installed) })

		It("admits what would be rejected and warns about it", func() {
			warnings := &collectedWarnings{}
			author := asUserWith(grantsAuthor, warnings)
			Eventually(func() string {
				m := mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "audit-secret"})
				Expect(author.Create(ctx, m)).To(Succeed(), "Audit and Warn must never reject")
				_ = k8sClient.Delete(ctx, m)
				return warnings.joined()
			}, "30s", "250ms").Should(ContainSubstring("secrets/audit-secret"))
		})
	})
})

// installUseGrants renders the chart with extraArgs, applies every object, and waits until the
// policies are enforced (or, for an Audit binding, until they warn). It returns the objects so a
// spec can remove them.
func installUseGrants(extraArgs ...string) []*unstructured.Unstructured {
	objs, err := renderUseGrants(extraArgs...)
	Expect(err).NotTo(HaveOccurred())
	Expect(objs).NotTo(BeEmpty())
	for _, o := range objs {
		Expect(k8sClient.Create(ctx, o)).To(Succeed())
	}
	if len(extraArgs) > 0 && strings.Contains(strings.Join(extraArgs, " "), "validationActions") {
		return objs
	}
	Eventually(func() bool {
		probe := &kubemootv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "probe-" + fmt.Sprint(len(objs)), Namespace: grantsNamespace},
			Spec:       kubemootv1alpha1.MCPServerSpec{Image: grantsImage, Transport: kubemootv1alpha1.TransportHTTP, SecretRef: csProbe},
		}
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: grantsAuthor}
		cl, cerr := client.New(c, client.Options{Scheme: scheme.Scheme})
		Expect(cerr).NotTo(HaveOccurred())
		err := cl.Create(ctx, probe)
		if err == nil {
			_ = k8sClient.Delete(ctx, probe)
		}
		return apierrors.IsForbidden(err)
	}, "30s", "250ms").Should(BeTrue())
	return objs
}

func removeUseGrants(objs []*unstructured.Unstructured) {
	for _, o := range objs {
		_ = k8sClient.Delete(ctx, o)
	}
}
