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
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	grantsNamespace   = "use-grants"
	grantsOperator    = "system:serviceaccount:kubemoot:kubemoot-operator"
	grantsPoliciesRel = "../../chart/kubemoot-operator/files/secret-use-grants-policies.yaml"
	grantsExemptToken = "__EXEMPT_USERS__"
	grantsAuthor      = "crew-author"
	grantsImage       = "tool:1"
	grantsRoleKind    = "Role"
	grantsAgents      = "agents"
)

// The policies the chart ships behind security.secretUseGrants.enabled. These specs apply the
// chart's own policy file to a real API server and ask as impersonated users, so the CEL, the
// authorizer calls and the rejection text are the ones a cluster runs.
var _ = Describe("Secret use grants", Ordered, func() {
	var seq int

	// grantUse gives user the verb "use" on the named Secrets or ServiceAccounts, plus the right
	// to write MCPServers and Agents in the namespace: a crew author who may apply a crew.
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

	// asUser returns a client that calls the API server as user.
	asUser := func(user string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user}
		cl, err := client.New(c, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		return cl
	}

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
	expectDenied := func(err error, mentions ...string) {
		Expect(apierrors.IsForbidden(err)).To(BeTrue(), "want Forbidden, got %v", err)
		for _, m := range mentions {
			Expect(err.Error()).To(ContainSubstring(m))
		}
	}
	envFromSecret := func(secret string) []corev1.EnvVar {
		return []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: "k"},
		}}}
	}

	BeforeAll(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: grantsNamespace}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		for _, res := range []string{"mcpservers", grantsAgents} {
			role := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{Name: "write-" + res, Namespace: grantsNamespace},
				Rules:      []rbacv1.PolicyRule{{APIGroups: []string{"kubemoot.ai"}, Resources: []string{res}, Verbs: []string{"get", "create", "update", "patch", "delete"}}},
			}
			Expect(k8sClient.Create(ctx, role)).To(Succeed())
			for _, user := range []string{grantsAuthor, grantsOperator, "exempt-admin"} {
				name := fmt.Sprintf("%s-%s", role.Name, strings.NewReplacer(":", "-").Replace(user))
				Expect(k8sClient.Create(ctx, &rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: grantsNamespace},
					Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: user, APIGroup: rbacv1.GroupName}},
					RoleRef:    rbacv1.RoleRef{Kind: grantsRoleKind, Name: role.Name, APIGroup: rbacv1.GroupName},
				})).To(Succeed())
			}
		}
	})

	Context("with the policy not installed (the chart flag off)", func() {
		It("lets an author reference any Secret and ServiceAccount", func() {
			m := mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "anything", ServiceAccountName: "anyone", Env: envFromSecret("another")})
			Expect(create(grantsAuthor, m)).To(Succeed())
			a := agent(kubemootv1alpha1.AgentSpec{Deployment: &kubemootv1alpha1.DeploymentConfig{ServiceAccountName: "anyone"}})
			Expect(create(grantsAuthor, a)).To(Succeed())
		})
	})

	Context("with the policy installed (the chart flag on)", func() {
		BeforeAll(func() {
			applyGrantsPolicies(operatorExempt())
			// The policies take effect a moment after they are created: wait for the first denial.
			Eventually(func() bool {
				probe := mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: csProbe})
				err := asUser(grantsAuthor).Create(ctx, probe)
				if err == nil {
					_ = k8sClient.Delete(ctx, probe)
				}
				return apierrors.IsForbidden(err)
			}, "30s", "250ms").Should(BeTrue())
		})
		AfterAll(removeGrantsPolicies)

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
			err := create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "only-this", Env: envFromSecret("not-this")}))
			expectDenied(err, "secrets/not-this")
			Expect(err.Error()).NotTo(ContainSubstring("secrets/only-this"))
		})

		It("rejects every other way an MCPServer can name a Secret", func() {
			cases := map[string]kubemootv1alpha1.MCPServerSpec{
				"env-secretKeyRef": {Env: envFromSecret("via-env")},
				"secretVolumes":    {SecretVolumes: []kubemootv1alpha1.SecretVolume{{Name: "via-volume", MountPath: "/etc/s"}}},
				"registry-auth":    {Registry: &kubemootv1alpha1.RegistryConfig{AuthSecretRef: "via-auth"}},
				"sidecar-env":      {Sidecars: []corev1.Container{{Name: "s", Image: grantsImage, Env: envFromSecret("via-sidecar-env")}}},
				"sidecar-envFrom":  {Sidecars: []corev1.Container{{Name: "s", Image: grantsImage, EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "via-sidecar-from"}}}}}}},
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
			Expect(create(grantsAuthor, mcp(kubemootv1alpha1.MCPServerSpec{ServiceAccountName: DefaultKubemootConfigName}))).To(Succeed())
		})

		It("rejects an Agent that names a Secret or ServiceAccount without a grant", func() {
			cases := map[string]kubemootv1alpha1.AgentSpec{
				"deployment-env": {Deployment: &kubemootv1alpha1.DeploymentConfig{Env: envFromSecret("agent-env")}},
				"harness-env":    {Harness: &kubemootv1alpha1.HarnessSpec{Name: "h", Env: envFromSecret("agent-harness")}},
				"memory":         {Memory: &kubemootv1alpha1.MemoryConfig{SecretRef: "agent-memory"}},
				"serviceaccount": {Deployment: &kubemootv1alpha1.DeploymentConfig{ServiceAccountName: "agent-sa"}},
			}
			for name, spec := range cases {
				err := create(grantsAuthor, agent(spec))
				Expect(apierrors.IsForbidden(err)).To(BeTrue(), "%s: want Forbidden, got %v", name, err)
			}
			grantUse(grantsAuthor, "secrets", "agent-ok")
			Expect(create(grantsAuthor, agent(kubemootv1alpha1.AgentSpec{Deployment: &kubemootv1alpha1.DeploymentConfig{Env: envFromSecret("agent-ok")}}))).To(Succeed())
		})

		It("checks a spec change on update but lets metadata-only writes through", func() {
			m := mcp(kubemootv1alpha1.MCPServerSpec{})
			Expect(k8sClient.Create(ctx, m)).To(Succeed()) // as the cluster admin
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, m) })

			author := asUser(grantsAuthor)
			current := &kubemootv1alpha1.MCPServer{}
			Expect(author.Get(ctx, client.ObjectKeyFromObject(m), current)).To(Succeed())
			current.Labels = map[string]string{"touched": "yes"}
			Expect(author.Update(ctx, current)).To(Succeed(), "a metadata-only update needs no grant")

			current.Spec.SecretRef = "added-later"
			expectDenied(author.Update(ctx, current), "secrets/added-later")
		})

		It("exempts the operator and the configured exempt users", func() {
			Expect(create(grantsOperator, mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "operator-made"}))).To(Succeed())
			Expect(create("exempt-admin", mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "admin-made"}))).To(Succeed())
		})

		It("lets a cluster admin through without any grant", func() {
			m := mcp(kubemootv1alpha1.MCPServerSpec{SecretRef: "admin-secret", ServiceAccountName: "admin-sa"})
			Expect(k8sClient.Create(ctx, m)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, m) })
		})
	})
})

func operatorExempt() string {
	return fmt.Sprintf("'%s', 'exempt-admin'", grantsOperator)
}

// grantsPolicyObjects reads the chart's policy file with the exempt users filled in.
func grantsPolicyObjects(exempt string) []admissionregistrationv1.ValidatingAdmissionPolicy {
	raw, err := os.ReadFile(filepath.Clean(grantsPoliciesRel))
	Expect(err).NotTo(HaveOccurred())
	text := strings.ReplaceAll(string(raw), grantsExemptToken, exempt)
	var out []admissionregistrationv1.ValidatingAdmissionPolicy
	for _, doc := range strings.Split(text, "\n---\n") {
		if !strings.Contains(doc, "kind: ValidatingAdmissionPolicy") {
			continue
		}
		var p admissionregistrationv1.ValidatingAdmissionPolicy
		Expect(yaml.UnmarshalStrict([]byte(doc), &p)).To(Succeed())
		out = append(out, p)
	}
	Expect(out).To(HaveLen(2), "the chart ships one policy for MCPServer and one for Agent")
	return out
}

// applyGrantsPolicies creates the chart's policies and a Deny binding for each, as the chart does.
func applyGrantsPolicies(exempt string) {
	for _, p := range grantsPolicyObjects(exempt) {
		policy := p
		Expect(k8sClient.Create(ctx, &policy)).To(Succeed())
		binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
			ObjectMeta: metav1.ObjectMeta{Name: policy.Name},
			Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        policy.Name,
				ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
			},
		}
		Expect(k8sClient.Create(ctx, binding)).To(Succeed())
	}
}

func removeGrantsPolicies() {
	for _, p := range grantsPolicyObjects(operatorExempt()) {
		_ = k8sClient.Delete(ctx, &admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: p.Name}})
		_ = k8sClient.Delete(ctx, &admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: p.Name}})
	}
}
