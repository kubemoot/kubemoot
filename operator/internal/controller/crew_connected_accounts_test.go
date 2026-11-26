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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

var _ = Describe("Connected accounts: the ConnectedAccounts condition and the registration", func() {
	var ns, suffix string

	create := func(obj client.Object) {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
	}
	account := func(name, tokenURL string) kubemootv1alpha1.ConnectedAccount {
		return kubemootv1alpha1.ConnectedAccount{
			Name: name, DisplayName: "Example " + name, Scopes: []string{"search"},
			AuthorizeURL: "https://auth.example.org/authorize", TokenURL: tokenURL, TokenHosts: []string{celHost},
		}
	}
	crewIn := func(namespace, name string, accounts ...kubemootv1alpha1.ConnectedAccount) *kubemootv1alpha1.Crew {
		return &kubemootv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       kubemootv1alpha1.CrewSpec{ConnectedAccounts: accounts},
		}
	}
	accountServer := func(name, accountName string) *kubemootv1alpha1.MCPServer {
		return &kubemootv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: kubemootv1alpha1.MCPServerSpec{
				ExternalEndpoint: credExternalURL,
				Registry: &kubemootv1alpha1.RegistryConfig{CallerCredential: &kubemootv1alpha1.CallerCredential{
					Mode: kubemootv1alpha1.CallerCredentialRequired, Account: accountName,
				}},
			},
		}
	}
	gateway := func(name string) *kubemootv1alpha1.MCPGateway {
		return &kubemootv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	}
	reconciler := func(registered ...string) *CrewReconciler {
		set := RegisteredAccounts{}
		for _, n := range registered {
			set[n] = struct{}{}
		}
		return &CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), ConfigCache: NewConfigCache(), RegisteredAccounts: set}
	}
	condition := func(crew *kubemootv1alpha1.Crew) *metav1.Condition {
		return meta.FindStatusCondition(crew.Status.Conditions, conditionTypeConnectedAccounts)
	}
	judge := func(r *CrewReconciler, crew *kubemootv1alpha1.Crew) *metav1.Condition {
		GinkgoHelper()
		Expect(r.reconcileConnectedAccounts(ctx, crew)).To(Succeed())
		return condition(crew)
	}

	BeforeEach(func() {
		n := credNamespaceSeq.Add(1)
		ns = fmt.Sprintf("acct-%d", n)
		suffix = fmt.Sprintf("%d", n)
		create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	})

	It("carries no condition when the crew declares nothing, and drops a stale one", func() {
		crew := crewIn(ns, "plain")
		create(crew)
		Expect(judge(reconciler(), crew)).To(BeNil())

		setCrewCondition(crew, conditionTypeConnectedAccounts, metav1.ConditionFalse, reasonAccountConflict, "stale")
		Expect(judge(reconciler(), crew)).To(BeNil())
	})

	It("is Ready when every account is registered and nothing else is wrong", func() {
		name := "ready-" + suffix
		crew := crewIn(ns, "lib", account(name, "https://auth.example.org/token"))
		create(crew)
		got := judge(reconciler(name), crew)
		Expect(got).NotTo(BeNil())
		Expect(got.Status).To(Equal(metav1.ConditionTrue))
		Expect(got.Reason).To(Equal(reasonConnectedAccountsReady))
	})

	Describe("UnregisteredAccount", func() {
		It("names an account the install has not registered", func() {
			name := "unreg-" + suffix
			crew := crewIn(ns, "lib", account(name, "https://auth.example.org/token"))
			create(crew)
			got := judge(reconciler("some-other-account"), crew)
			Expect(got.Status).To(Equal(metav1.ConditionFalse))
			Expect(got.Reason).To(Equal(reasonUnregisteredAccount))
			Expect(got.Message).To(ContainSubstring(fmt.Sprintf("%q", name)))
		})

		It("is reported when the install registers none", func() {
			crew := crewIn(ns, "lib", account("none-"+suffix, "https://auth.example.org/token"))
			create(crew)
			Expect(judge(reconciler(), crew).Reason).To(Equal(reasonUnregisteredAccount))
			Expect(judge(&CrewReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}, crew).Reason).To(Equal(reasonUnregisteredAccount))
		})

		It("lists only the names that are missing", func() {
			have, lack := "have-"+suffix, "lack-"+suffix
			crew := crewIn(ns, "lib", account(have, "https://auth.example.org/token"), account(lack, "https://auth.example.org/token"))
			create(crew)
			got := judge(reconciler(have), crew)
			Expect(got.Message).To(ContainSubstring(lack))
			Expect(got.Message).NotTo(ContainSubstring(have))
		})
	})

	Describe("Conflict", func() {
		var other string

		BeforeEach(func() {
			other = fmt.Sprintf("acct-other-%s", suffix)
			create(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: other}})
		})

		It("reports both crews when two declare one name for different services", func() {
			name := "shared-" + suffix
			first := crewIn(ns, "first", account(name, "https://auth.example.org/token"))
			second := crewIn(other, "second", account(name, "https://elsewhere.example.net/token"))
			create(first)
			create(second)
			r := reconciler(name)

			got := judge(r, first)
			Expect(got.Status).To(Equal(metav1.ConditionFalse))
			Expect(got.Reason).To(Equal(reasonAccountConflict))
			Expect(got.Message).To(ContainSubstring(name))
			Expect(got.Message).To(ContainSubstring(other + "/second"))
			Expect(judge(r, second).Reason).To(Equal(reasonAccountConflict))
		})

		It("also conflicts on a different authorizeURL", func() {
			name := "authz-" + suffix
			first := crewIn(ns, "first", account(name, "https://auth.example.org/token"))
			differs := account(name, "https://auth.example.org/token")
			differs.AuthorizeURL = "https://auth.example.org/other-authorize"
			second := crewIn(other, "second", differs)
			create(first)
			create(second)
			Expect(judge(reconciler(name), first).Reason).To(Equal(reasonAccountConflict))
		})

		It("also conflicts on a different set of token hosts or revocation endpoint", func() {
			name := "hosts-" + suffix
			first := crewIn(ns, "first", account(name, "https://auth.example.org/token"))
			wider := account(name, "https://auth.example.org/token")
			wider.TokenHosts = []string{celHost, "other.example.net"}
			second := crewIn(other, "second", wider)
			create(first)
			create(second)
			r := reconciler(name)
			Expect(judge(r, first).Reason).To(Equal(reasonAccountConflict))

			second.Spec.ConnectedAccounts[0].TokenHosts = []string{celHost}
			second.Spec.ConnectedAccounts[0].RevocationURL = "https://auth.example.org/revoke"
			Expect(k8sClient.Update(ctx, second)).To(Succeed())
			Expect(judge(r, first).Reason).To(Equal(reasonAccountConflict))
		})

		It("treats the same hosts in another order as one service", func() {
			name := "order-hosts-" + suffix
			a, b := account(name, "https://auth.example.org/token"), account(name, "https://auth.example.org/token")
			a.TokenHosts = []string{"a.example.org", "b.example.org"}
			b.TokenHosts = []string{"b.example.org", "a.example.org"}
			first, second := crewIn(ns, "first", a), crewIn(other, "second", b)
			create(first)
			create(second)
			Expect(judge(reconciler(name), first).Reason).To(Equal(reasonConnectedAccountsReady))
		})

		It("reaches the peers of an account's old name when it is renamed", func() {
			name := "old-" + suffix
			renamed := crewIn(ns, "renamed", account("new-"+suffix, "https://auth.example.org/token"))
			peer := crewIn(other, "peer", account(name, "https://auth.example.org/token"))
			create(peer)
			before := crewIn(ns, "renamed", account(name, "https://auth.example.org/token"))
			Expect(sharedAccountRequests(before)).To(ConsistOf(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(peer)}))
			Expect(sharedAccountRequests(renamed)).To(BeEmpty())
		})

		It("is not a conflict when both crews declare the same service", func() {
			name := "same-" + suffix
			first := crewIn(ns, "first", account(name, "https://auth.example.org/token"))
			second := crewIn(other, "second", account(name, "https://auth.example.org/token"))
			create(first)
			create(second)
			r := reconciler(name)
			Expect(judge(r, first).Reason).To(Equal(reasonConnectedAccountsReady))
			Expect(judge(r, second).Reason).To(Equal(reasonConnectedAccountsReady))
		})

		It("clears once the other crew no longer disagrees", func() {
			name := "clears-" + suffix
			first := crewIn(ns, "first", account(name, "https://auth.example.org/token"))
			second := crewIn(other, "second", account(name, "https://elsewhere.example.net/token"))
			create(first)
			create(second)
			r := reconciler(name)
			Expect(judge(r, first).Reason).To(Equal(reasonAccountConflict))

			second.Spec.ConnectedAccounts[0].TokenURL = "https://auth.example.org/token"
			Expect(k8sClient.Update(ctx, second)).To(Succeed())
			Expect(judge(r, first).Reason).To(Equal(reasonConnectedAccountsReady))
		})

		It("ignores a crew that declares only other names", func() {
			name := "ignored-" + suffix
			first := crewIn(ns, "first", account(name, "https://auth.example.org/token"))
			unrelated := crewIn(other, "unrelated", account("unrelated-"+suffix, "https://elsewhere.example.net/token"))
			create(first)
			create(unrelated)
			Expect(judge(reconciler(name), first).Reason).To(Equal(reasonConnectedAccountsReady))
		})

		It("takes a conflict before an unregistered name", func() {
			name := "order-" + suffix
			first := crewIn(ns, "first", account(name, "https://auth.example.org/token"))
			second := crewIn(other, "second", account(name, "https://elsewhere.example.net/token"))
			create(first)
			create(second)
			Expect(judge(reconciler(), first).Reason).To(Equal(reasonAccountConflict))
		})

		It("re-reconciles the crews that share a name with a changed crew, in any namespace", func() {
			name := "queue-" + suffix
			changed := crewIn(ns, "changed", account(name, "https://auth.example.org/token"))
			peer := crewIn(other, "peer", account(name, "https://elsewhere.example.net/token"))
			stranger := crewIn(other, "stranger", account("stranger-"+suffix, "https://auth.example.org/token"))
			create(changed)
			create(peer)
			create(stranger)

			reqs := sharedAccountRequests(changed)
			Expect(reqs).To(ConsistOf(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(peer)}))
			Expect(sharedAccountRequests(crewIn(ns, "no-accounts"))).To(BeEmpty())
			Expect(sharedAccountRequests(&corev1.Namespace{})).To(BeEmpty())
		})
	})

	Describe("UnknownAccount", func() {
		It("names a server whose account no crew of the namespace declares", func() {
			name := "known-" + suffix
			crew := crewIn(ns, "lib", account(name, "https://auth.example.org/token"))
			create(crew)
			create(accountServer("srv", "missing-"+suffix))
			got := judge(reconciler(name), crew)
			Expect(got.Status).To(Equal(metav1.ConditionFalse))
			Expect(got.Reason).To(Equal(reasonUnknownAccount))
			Expect(got.Message).To(ContainSubstring("srv"))
			Expect(got.Message).To(ContainSubstring("missing-" + suffix))
		})

		It("is reported on a crew that declares nothing, and absent when the server has no account", func() {
			crew := crewIn(ns, "plain")
			create(crew)
			create(accountServer("srv", "missing-"+suffix))
			Expect(judge(reconciler(), crew).Reason).To(Equal(reasonUnknownAccount))

			plain := crewIn(ns, "plain2")
			create(plain)
			Expect(k8sClient.Delete(ctx, accountServer("srv", "missing-"+suffix))).To(Succeed())
			Expect(judge(reconciler(), plain)).To(BeNil())
		})

		It("clears once a crew of the namespace declares the account", func() {
			name := "later-" + suffix
			first := crewIn(ns, "first")
			create(first)
			create(accountServer("srv", name))
			create(gateway("gw"))
			Expect(judge(reconciler(name), first).Reason).To(Equal(reasonUnknownAccount))

			create(crewIn(ns, "second", account(name, "https://auth.example.org/token")))
			Expect(judge(reconciler(name), first)).To(BeNil(), "a crew that declares nothing carries no condition once nothing is unknown")
		})

		It("does not count a server that takes a verifier, or one in mode Off", func() {
			crew := crewIn(ns, "lib", account("fine-"+suffix, "https://auth.example.org/token"))
			create(crew)
			create(requiredServer(ns))
			Expect(judge(reconciler("fine-"+suffix), crew).Reason).To(Equal(reasonConnectedAccountsReady))
		})

		It("is checked after a conflict and an unregistered name", func() {
			name := "ranked-" + suffix
			crew := crewIn(ns, "lib", account(name, "https://auth.example.org/token"))
			create(crew)
			create(accountServer("srv", "missing-"+suffix))
			Expect(judge(reconciler(), crew).Reason).To(Equal(reasonUnregisteredAccount))
		})
	})

	Describe("NoSingleToolGateway", func() {
		It("is reported when a server takes the crew's account and the namespace has no gateway", func() {
			name := "gw-" + suffix
			crew := crewIn(ns, "lib", account(name, "https://auth.example.org/token"))
			create(crew)
			create(accountServer("srv", name))
			got := judge(reconciler(name), crew)
			Expect(got.Status).To(Equal(metav1.ConditionFalse))
			Expect(got.Reason).To(Equal(reasonNoSingleToolGateway))
		})

		It("is reported with two gateways, and is Ready with exactly one", func() {
			name := "gws-" + suffix
			crew := crewIn(ns, "lib", account(name, "https://auth.example.org/token"))
			create(crew)
			create(accountServer("srv", name))
			first, second := gateway("one"), gateway("two")
			create(first)
			Expect(judge(reconciler(name), crew).Reason).To(Equal(reasonConnectedAccountsReady))
			create(second)
			Expect(judge(reconciler(name), crew).Reason).To(Equal(reasonNoSingleToolGateway))
		})

		It("does not matter when no server takes the crew's account", func() {
			name := "nosrv-" + suffix
			crew := crewIn(ns, "lib", account(name, "https://auth.example.org/token"))
			create(crew)
			create(&kubemootv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "plain-srv", Namespace: ns},
				Spec:       kubemootv1alpha1.MCPServerSpec{ExternalEndpoint: credExternalURL},
			})
			Expect(judge(reconciler(name), crew).Reason).To(Equal(reasonConnectedAccountsReady))
		})
	})

	Describe("the registration with the tool gateway", func() {
		It("carries the account and no verifier", func() {
			got := callerCredentialPayload(accountServer("srv", "library"))
			Expect(got).To(HaveKeyWithValue("mode", "Required"))
			Expect(got).To(HaveKeyWithValue("account", "library"))
			Expect(got).NotTo(HaveKey("verifier"))
			Expect(got).To(HaveKeyWithValue("relayContext", false))
		})

		It("still carries the verifier and no account for a service token server", func() {
			server := requiredServer(ns)
			got := callerCredentialPayload(server)
			Expect(got).To(HaveKeyWithValue("verifier", credVerifier))
			Expect(got).NotTo(HaveKey("account"))
		})

		It("wires the tool gateway for an account server, and keeps it out of the verifier condition", func() {
			server := accountServer("srv", "library")
			Expect(takesPersonsToken(server)).To(BeTrue())
			Expect(requiredCallerCredential(server)).To(BeFalse())
			Expect(anyCallerCredentialRequired([]kubemootv1alpha1.MCPServer{*server})).To(BeTrue())
			Expect(requiredCallerCredential(requiredServer(ns))).To(BeTrue())
		})

		It("adds the optional fields beside the account", func() {
			server := accountServer("srv", "library")
			cc := server.Spec.Registry.CallerCredential
			cc.AccountName, cc.OnForbidden = "Library", []string{"https://fallback.example.org/mcp"}
			got := callerCredentialPayload(server)
			Expect(got).To(HaveKeyWithValue("accountName", "Library"))
			Expect(got).To(HaveKey("onForbidden"))
		})
	})

	Describe("which accounts the install registered", func() {
		It("reads a comma separated list and ignores blanks", func() {
			get := func(v string) func(string) string {
				return func(key string) string {
					if key == ConnectedAccountsRegisteredEnv {
						return v
					}
					return ""
				}
			}
			Expect(RegisteredAccountsFromEnv(get("library, other ,,"))).To(HaveLen(2))
			Expect(RegisteredAccountsFromEnv(get("library"))).To(HaveKey("library"))
			Expect(RegisteredAccountsFromEnv(get(""))).To(BeEmpty())
			Expect(RegisteredAccountsFromEnv(get(" , "))).To(BeEmpty())
		})
	})
})

// sharedAccountRequests runs the connected-account watch's mapping for an object.
func sharedAccountRequests(obj client.Object) []reconcile.Request {
	return crewsSharingAccounts(ctx, k8sClient, obj)
}
