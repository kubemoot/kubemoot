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
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
)

var credNamespaceSeq atomic.Int32

// envText is the plain value of an environment variable, empty when it is not set.
func envText(env []corev1.EnvVar, name string) string {
	v, _ := envValue(env, name)
	return v
}

const (
	credVerifyURL      = "https://api.example.com/mcp"
	credExternalURL    = "http://external:9000"
	credNamespace      = "default"
	credTeamLabel      = "team"
	credKubernetesName = "kubernetes"
	credVerifier       = "oreilly"
	credHashKey        = "install-hash"
)

func tokenVerifier(passToTools bool) kubemootv1alpha1.ServiceTokenVerifier {
	return kubemootv1alpha1.ServiceTokenVerifier{
		Name: credVerifier, VerifyURL: credVerifyURL, VerifyMethod: identity.VerifyMcpInitialize,
		SubjectClaim: "upstream_claims.sub", MaxCacheSeconds: 300, PassToTools: passToTools,
		Authorize: identity.AuthorizeAnyAccepted,
	}
}

func requiredServer(namespace string) *kubemootv1alpha1.MCPServer {
	return &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: namespace},
		Spec: kubemootv1alpha1.MCPServerSpec{
			ExternalEndpoint: credExternalURL,
			Registry: &kubemootv1alpha1.RegistryConfig{CallerCredential: &kubemootv1alpha1.CallerCredential{
				Mode: kubemootv1alpha1.CallerCredentialRequired, Verifier: credVerifier,
				OnForbidden: []string{"https://fallback.example.com/mcp"}, RelayContext: true, AccountName: "Example",
			}},
		},
	}
}

var _ = Describe("Each person's own access: operator wiring", func() {
	var ns string

	create := func(obj client.Object) {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
	}

	crewOf := func(name string, access *kubemootv1alpha1.DiscussionAccess) *kubemootv1alpha1.Crew {
		return &kubemootv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       kubemootv1alpha1.CrewSpec{Discussion: &kubemootv1alpha1.DiscussionConfig{Access: access}},
		}
	}
	tokenAccess := func(passToTools bool) *kubemootv1alpha1.DiscussionAccess {
		return &kubemootv1alpha1.DiscussionAccess{
			ServiceTokens:    []kubemootv1alpha1.ServiceTokenVerifier{tokenVerifier(passToTools)},
			HashKeySecretRef: &kubemootv1alpha1.HashKeySecretRef{Name: credHashKey},
		}
	}
	gatewayOf := func(name string) *kubemootv1alpha1.MCPGateway {
		return &kubemootv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	}
	crewReconciler := func() *CrewReconciler {
		return &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(),
			NetPol: &NetPolicySync{Client: k8sClient, Scheme: k8sClient.Scheme(), Config: netpol.Config{
				Enabled: true, Ingress: true, Cilium: netpol.CiliumOff, OperatorNamespace: defaultOperatorNamespace}}}
	}
	gatewayReconciler := func() *MCPGatewayReconciler {
		return &MCPGatewayReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache()}
	}
	deploymentOf := func(r *CrewReconciler, crew *kubemootv1alpha1.Crew) *appsv1.Deployment {
		GinkgoHelper()
		d, err := r.reconcileDeployment(ctx, crew)
		Expect(err).NotTo(HaveOccurred())
		return d
	}
	envOf := func(d *appsv1.Deployment) []corev1.EnvVar { return d.Spec.Template.Spec.Containers[0].Env }

	BeforeEach(func() {
		ns = fmt.Sprintf("cred-%d", credNamespaceSeq.Add(1))
		create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	Context("a crew that sets none of it", func() {
		It("renders the gateway as it always did", func() {
			crew := crewOf("plain", nil)
			create(crew)
			r := crewReconciler()
			d := deploymentOf(r, crew)
			Expect(r.reconcileService(ctx, crew)).To(Succeed())
			Expect(r.reconcileNetworkPolicies(ctx, crew)).To(Succeed())
			Expect(r.reconcileCallerCredentials(ctx, crew)).To(Succeed())

			Expect(d.Spec.Template.Labels).NotTo(HaveKey(netpol.LabelCredentialLease))
			Expect(d.Spec.Template.Spec.Containers[0].Ports).To(HaveLen(1))
			for _, name := range []string{identity.EnvServiceTokens, identity.EnvHashKey, envAccessBrokerSubject, envInternalPort} {
				Expect(envNamed(envOf(d), name)).To(BeEmpty(), name)
			}
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "plain-discussion"}, svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(1))
			policies := &networkingv1.NetworkPolicyList{}
			Expect(k8sClient.List(ctx, policies, client.InNamespace(ns))).To(Succeed())
			for _, p := range policies.Items {
				Expect(p.Name).NotTo(ContainSubstring("lease"))
			}
			Expect(meta.FindStatusCondition(crew.Status.Conditions, conditionTypeCallerCredentials)).To(BeNil())
		})

		It("is not changed by a tool server that sets no caller credential", func() {
			crew := crewOf("plain", &kubemootv1alpha1.DiscussionAccess{})
			create(crew)
			server := &kubemootv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: ns},
				Spec:       kubemootv1alpha1.MCPServerSpec{ExternalEndpoint: credExternalURL},
			}
			create(server)
			Expect(requiredCallerCredential(server)).To(BeFalse())
			Expect(callerCredentialPayload(server)).To(BeNil())
			Expect(crewReconciler().reconcileCallerCredentials(ctx, crew)).To(Succeed())
			Expect(meta.FindStatusCondition(crew.Status.Conditions, conditionTypeCallerCredentials)).To(BeNil())
		})
	})

	Context("a server in mode Off", func() {
		offServer := func(relay bool) *kubemootv1alpha1.MCPServer {
			return &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{
				Registry: &kubemootv1alpha1.RegistryConfig{CallerCredential: &kubemootv1alpha1.CallerCredential{
					Mode: kubemootv1alpha1.CallerCredentialOff, RelayContext: relay, Verifier: "ignored",
				}}}}
		}

		It("registers only the mode and relayContext when relayContext is set", func() {
			got := callerCredentialPayload(offServer(true))
			Expect(got).To(HaveLen(2))
			Expect(got).To(HaveKeyWithValue("mode", "Off"))
			Expect(got).To(HaveKeyWithValue("relayContext", true))
		})

		It("registers nothing when relayContext is not set", func() {
			Expect(callerCredentialPayload(offServer(false))).To(BeNil())
		})
	})

	Context("a crew with a service token verifier", func() {
		It("renders the verifier list and the hash key from the Secret into the gateway", func() {
			crew := crewOf("tok", tokenAccess(false))
			create(crew)
			d := deploymentOf(crewReconciler(), crew)

			raw, ok := envValue(envOf(d), identity.EnvServiceTokens)
			Expect(ok).To(BeTrue())
			parsed, err := identity.ParseServiceTokens(raw)
			Expect(err).NotTo(HaveOccurred(), "the gateway must accept what the operator wrote")
			Expect(parsed).To(HaveLen(1))
			Expect(parsed[0].Name).To(Equal(credVerifier))
			Expect(parsed[0].SubjectClaim).To(Equal("upstream_claims.sub"))
			Expect(parsed[0].PassToTools).To(BeFalse())

			hash := envNamed(envOf(d), identity.EnvHashKey)
			Expect(hash).To(HaveLen(1))
			Expect(hash[0].Value).To(BeEmpty(), "the key is never written into the Deployment")
			ref := hash[0].ValueFrom.SecretKeyRef
			Expect(ref.Name).To(Equal(credHashKey))
			Expect(ref.Key).To(Equal("hash-key"), "the key's default")
			Expect(envText(envOf(d), identity.EnvEnforce)).To(Equal(valueTrue))

			By("keeping no lease port and no lease label while no verifier passes tokens")
			Expect(d.Spec.Template.Labels).NotTo(HaveKey(netpol.LabelCredentialLease))
			Expect(envNamed(envOf(d), envAccessBrokerSubject)).To(BeEmpty())
		})

		It("reads the hash key from the key the crew names", func() {
			access := tokenAccess(false)
			access.HashKeySecretRef.Key = "custom"
			crew := crewOf("custom", access)
			create(crew)
			hash := envNamed(envOf(deploymentOf(crewReconciler(), crew)), identity.EnvHashKey)
			Expect(hash[0].ValueFrom.SecretKeyRef.Key).To(Equal("custom"))
		})

		It("reports the Authentication condition with reason ServiceToken", func() {
			crew := crewOf("authn", tokenAccess(false))
			reconcileAuthentication(crew)
			c := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeAuthentication)
			Expect(c).NotTo(BeNil())
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Reason).To(Equal(reasonServiceToken))
			Expect(c.Message).To(ContainSubstring(credVerifier))
		})
	})

	Context("a crew whose verifier passes tokens to tools", func() {
		It("names the tool gateway's ServiceAccount, opens the lease port, and admits only that gateway to it", func() {
			crew := crewOf("lease", tokenAccess(true))
			create(crew)
			create(gatewayOf("tools"))
			r := crewReconciler()
			d := deploymentOf(r, crew)

			Expect(envText(envOf(d), envAccessBrokerSubject)).To(Equal("system:serviceaccount:" + ns + ":tools-credential-broker"))
			Expect(envText(envOf(d), envInternalPort)).To(Equal("8081"))
			Expect(d.Spec.Template.Labels).To(HaveKeyWithValue(netpol.LabelCredentialLease, valueTrue))
			Expect(d.Spec.Selector.MatchLabels).NotTo(HaveKey(netpol.LabelCredentialLease), "the selector is immutable")
			Expect(d.Spec.Template.Spec.Containers[0].Ports).To(ContainElement(
				corev1.ContainerPort{Name: portNameInternal, ContainerPort: 8081, Protocol: corev1.ProtocolTCP}))

			Expect(r.reconcileService(ctx, crew)).To(Succeed())
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "lease-discussion"}, svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(2))
			Expect(svc.Spec.Ports[1].Port).To(Equal(int32(8081)))

			Expect(r.reconcileNetworkPolicies(ctx, crew)).To(Succeed())
			p := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "km-crew-lease-gateway-lease"}, p)).To(Succeed())
			Expect(p.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue(netpol.LabelCrew, "lease"))
			Expect(p.Spec.Ingress[0].From).To(HaveLen(1))
			Expect(p.Spec.Ingress[0].From[0].PodSelector.MatchLabels).To(
				Equal(map[string]string{"app.kubernetes.io/component": netpol.ComponentMCPGateway}))
			Expect(p.Spec.Ingress[0].Ports[0].Port.IntVal).To(Equal(int32(8081)))
			Expect(p.Spec.Ingress[1].Ports[0].Port.IntVal).To(Equal(int32(8080)))
		})

		It("leaves the lease listener off when the namespace has no single tool gateway", func() {
			crew := crewOf("none", tokenAccess(true))
			create(crew)
			Expect(envNamed(envOf(deploymentOf(crewReconciler(), crew)), envAccessBrokerSubject)).To(BeEmpty())
			create(gatewayOf("tools-a"))
			create(gatewayOf("tools-b"))
			Expect(envNamed(envOf(deploymentOf(crewReconciler(), crew)), envAccessBrokerSubject)).To(BeEmpty())
		})

		It("lets a gateway that verifies no Kubernetes token still check the tool gateway's token", func() {
			access := tokenAccess(true)
			off := false
			access.TokenReview = &off
			rules := gatewayAccessRules(access)
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].Resources).To(Equal([]string{"tokenreviews"}))
			Expect(gatewayAccessRules(tokenAccess(false))).To(HaveLen(2), "the default is unchanged")
		})

		It("removes the lease policy when the crew stops passing tokens", func() {
			crew := crewOf("stop", tokenAccess(true))
			create(crew)
			r := crewReconciler()
			Expect(r.reconcileNetworkPolicies(ctx, crew)).To(Succeed())
			name := types.NamespacedName{Namespace: ns, Name: "km-crew-stop-gateway-lease"}
			Expect(k8sClient.Get(ctx, name, &networkingv1.NetworkPolicy{})).To(Succeed())
			crew.Spec.Discussion.Access.ServiceTokens[0].PassToTools = false
			Expect(r.reconcileNetworkPolicies(ctx, crew)).To(Succeed())
			Expect(errors.IsNotFound(k8sClient.Get(ctx, name, &networkingv1.NetworkPolicy{}))).To(BeTrue())
		})
	})

	Context("the CallerCredentials condition", func() {
		condition := func(r *CrewReconciler, crew *kubemootv1alpha1.Crew) *metav1.Condition {
			GinkgoHelper()
			Expect(r.reconcileCallerCredentials(ctx, crew)).To(Succeed())
			return meta.FindStatusCondition(crew.Status.Conditions, conditionTypeCallerCredentials)
		}

		It("is False when a server needs the person's token and no verifier passes it", func() {
			crew := crewOf("c1", tokenAccess(false))
			create(crew)
			create(requiredServer(ns))
			c := condition(crewReconciler(), crew)
			Expect(c).NotTo(BeNil())
			Expect(c.Status).To(Equal(metav1.ConditionFalse))
			Expect(c.Reason).To(Equal(reasonNoTokenPassingVerifier))
			Expect(c.Message).To(ContainSubstring("passToTools"))
		})

		It("is False when the server names a verifier the crew does not have", func() {
			crew := crewOf("c2", tokenAccess(true))
			create(crew)
			server := requiredServer(ns)
			server.Spec.Registry.CallerCredential.Verifier = testMismatchName
			create(server)
			Expect(condition(crewReconciler(), crew).Reason).To(Equal(reasonUnknownVerifier))
		})

		It("is False with no single tool gateway to lease", func() {
			crew := crewOf("c3", tokenAccess(true))
			create(crew)
			create(requiredServer(ns))
			Expect(condition(crewReconciler(), crew).Reason).To(Equal(reasonNoSingleToolGateway))
		})

		It("is False when a second crew of the namespace also keeps tokens", func() {
			crew := crewOf("c4", tokenAccess(true))
			create(crew)
			create(crewOf("c4b", tokenAccess(true)))
			create(gatewayOf("tools"))
			create(requiredServer(ns))
			Expect(condition(crewReconciler(), crew).Reason).To(Equal(reasonSeveralTokenCrews))
		})

		It("ignores a Required server that no tool gateway of the namespace fronts", func() {
			crew := crewOf("c6", tokenAccess(true))
			create(crew)
			gw := gatewayOf("tools")
			gw.Spec.MCPServerSelector = &metav1.LabelSelector{MatchLabels: map[string]string{credTeamLabel: "a"}}
			create(gw)
			create(requiredServer(ns))
			Expect(condition(crewReconciler(), crew)).To(BeNil())
		})

		It("is True when the pieces are there, and gone when the server is", func() {
			crew := crewOf("c5", tokenAccess(true))
			create(crew)
			create(gatewayOf("tools"))
			server := requiredServer(ns)
			create(server)
			r := crewReconciler()
			c := condition(r, crew)
			Expect(c.Status).To(Equal(metav1.ConditionTrue))
			Expect(c.Reason).To(Equal(reasonCallerCredentialsReady))
			Expect(k8sClient.Delete(ctx, server)).To(Succeed())
			Expect(condition(r, crew)).To(BeNil())
		})
	})

	Context("the tool gateway", func() {
		deployment := func(gw *kubemootv1alpha1.MCPGateway) *appsv1.Deployment {
			GinkgoHelper()
			r := gatewayReconciler()
			d := r.buildDeployment(gw)
			Expect(r.applyCredentialBroker(ctx, gw, d)).To(Succeed())
			return d
		}
		brokerAccount := func(name string) error {
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &corev1.ServiceAccount{})
		}

		It("is unchanged when no server it fronts needs the person's token", func() {
			gw := gatewayOf("tools")
			create(gw)
			create(crewOf("c", tokenAccess(true)))
			r := gatewayReconciler()
			plain := r.buildDeployment(gw)
			Expect(deployment(gw).Spec).To(Equal(plain.Spec))
			Expect(errors.IsNotFound(brokerAccount("tools-credential-broker"))).To(BeTrue())
		})

		It("runs as its own account with a projected token for the broker and the broker's address", func() {
			gw := gatewayOf("tools")
			create(gw)
			create(crewOf("c", tokenAccess(true)))
			create(requiredServer(ns))
			d := deployment(gw)

			Expect(d.Spec.Template.Spec.ServiceAccountName).To(Equal("tools-credential-broker"))
			Expect(brokerAccount("tools-credential-broker")).To(Succeed())
			Expect(d.Spec.Template.Spec.Volumes).To(HaveLen(1))
			src := d.Spec.Template.Spec.Volumes[0].Projected.Sources[0].ServiceAccountToken
			Expect(src.Audience).To(Equal("kubemoot-credential-broker"))
			Expect(src.Path).To(Equal("token"))
			c := d.Spec.Template.Spec.Containers[0]
			Expect(c.VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: credentialBrokerVolume, MountPath: "/var/run/secrets/kubemoot/credential-broker", ReadOnly: true}))
			Expect(envText(c.Env, envCredentialBroker)).To(Equal("http://c-discussion." + ns + ".svc:8081"))
		})

		It("sets no broker address when two crews keep tokens", func() {
			gw := gatewayOf("tools")
			create(gw)
			create(crewOf("c1", tokenAccess(true)))
			create(crewOf("c2", tokenAccess(true)))
			create(requiredServer(ns))
			Expect(envNamed(deployment(gw).Spec.Template.Spec.Containers[0].Env, envCredentialBroker)).To(BeEmpty())
		})

		It("removes its account when no server needs the token any more", func() {
			gw := gatewayOf("tools")
			create(gw)
			server := requiredServer(ns)
			create(server)
			deployment(gw)
			Expect(brokerAccount("tools-credential-broker")).To(Succeed())
			Expect(k8sClient.Delete(ctx, server)).To(Succeed())
			deployment(gw)
			Expect(errors.IsNotFound(brokerAccount("tools-credential-broker"))).To(BeTrue())
		})

		It("passes callerCredential through the registration, and for a server in mode Off only when it relays the ids", func() {
			server := requiredServer(ns)
			payload := gatewayReconciler().buildRegistrationPayload(kubemootv1alpha1.ImplementationKubemoot, server)
			Expect(payload["callerCredential"]).To(Equal(map[string]interface{}{
				"mode": "Required", "verifier": credVerifier, "relayContext": true,
				"accountName": "Example", "onForbidden": []string{"https://fallback.example.com/mcp"},
			}))
			server.Spec.Registry.CallerCredential.Mode = kubemootv1alpha1.CallerCredentialOff
			payload = gatewayReconciler().buildRegistrationPayload(kubemootv1alpha1.ImplementationKubemoot, server)
			Expect(payload["callerCredential"]).To(Equal(map[string]interface{}{"mode": "Off", "relayContext": true}))
			server.Spec.Registry.CallerCredential.RelayContext = false
			payload = gatewayReconciler().buildRegistrationPayload(kubemootv1alpha1.ImplementationKubemoot, server)
			Expect(payload).NotTo(HaveKey("callerCredential"))
		})
	})
})
