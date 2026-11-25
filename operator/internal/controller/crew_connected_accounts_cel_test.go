/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The Crew and MCPServer CRDs hold the connected-account rules at the API server, so they
// hold where the admission webhook is not installed.
// celAccount is the account name the admission tests use.
const (
	celAccount = "library"
	celHost    = "mcp.example.org"
)

var _ = Describe("Connected account admission validation", func() {
	createObj := func(obj client.Object) error {
		err := k8sClient.Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		}
		return err
	}
	invalid := func(err error, substring string) {
		GinkgoHelper()
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring(substring))
	}
	account := func(mutate func(*kubemootv1alpha1.ConnectedAccount)) kubemootv1alpha1.ConnectedAccount {
		a := kubemootv1alpha1.ConnectedAccount{
			Name: celAccount, DisplayName: "Example Library", Purpose: "Search the library with your own subscription.",
			Scopes:       []string{"openid", "search"},
			AuthorizeURL: "https://auth.example.org/oauth2/authorize", TokenURL: "https://auth.example.org/oauth2/token",
			RevocationURL: "https://auth.example.org/oauth2/revoke", TokenHosts: []string{celHost},
		}
		if mutate != nil {
			mutate(&a)
		}
		return a
	}
	crewWith := func(name string, accounts ...kubemootv1alpha1.ConnectedAccount) error {
		return createObj(&kubemootv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: credNamespace},
			Spec:       kubemootv1alpha1.CrewSpec{ConnectedAccounts: accounts},
		})
	}

	Describe("Crew spec.connectedAccounts", func() {
		It("is optional", func() {
			Expect(crewWith("ca-none")).To(Succeed())
		})

		It("accepts a full declaration and defaults usage to Required", func() {
			Expect(crewWith("ca-ok", account(nil))).To(Succeed())
			got := &kubemootv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: credNamespace, Name: "ca-ok"}, got)).To(Succeed())
			Expect(got.Spec.ConnectedAccounts[0].Usage).To(Equal(kubemootv1alpha1.ConnectedAccountRequired))
		})

		It("accepts the Optional usage and no revocation endpoint", func() {
			Expect(crewWith("ca-optional", account(func(a *kubemootv1alpha1.ConnectedAccount) {
				a.Usage = kubemootv1alpha1.ConnectedAccountOptional
				a.RevocationURL = ""
			}))).To(Succeed())
		})

		It("refuses a name that is not a DNS label", func() {
			for i, bad := range []string{"Library", "my_lib", "-lib", "lib-", "has space", ""} {
				err := crewWith("ca-name-"+string(rune('a'+i)), account(func(a *kubemootv1alpha1.ConnectedAccount) { a.Name = bad }))
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "name %q: %v", bad, err)
			}
		})

		It("refuses an authorize, token or revocation URL that is not https", func() {
			invalid(crewWith("ca-http-auth", account(func(a *kubemootv1alpha1.ConnectedAccount) {
				a.AuthorizeURL = "http://auth.example.org/authorize"
			})), "authorizeURL must be https")
			invalid(crewWith("ca-http-token", account(func(a *kubemootv1alpha1.ConnectedAccount) {
				a.TokenURL = "http://auth.example.org/token"
			})), "tokenURL must be https")
			invalid(crewWith("ca-http-revoke", account(func(a *kubemootv1alpha1.ConnectedAccount) {
				a.RevocationURL = "ftp://auth.example.org/revoke"
			})), "revocationURL must be https")
		})

		It("refuses an https URL with no host", func() {
			invalid(crewWith("ca-nohost", account(func(a *kubemootv1alpha1.ConnectedAccount) { a.AuthorizeURL = "https://" })), "authorizeURL must be https")
			invalid(crewWith("ca-nohost-path", account(func(a *kubemootv1alpha1.ConnectedAccount) { a.TokenURL = "https:///token" })), "tokenURL must be https")
		})

		It("requires the endpoints and tokenHosts", func() {
			Expect(apierrors.IsInvalid(crewWith("ca-no-auth", account(func(a *kubemootv1alpha1.ConnectedAccount) { a.AuthorizeURL = "" })))).To(BeTrue())
			Expect(apierrors.IsInvalid(crewWith("ca-no-token", account(func(a *kubemootv1alpha1.ConnectedAccount) { a.TokenURL = "" })))).To(BeTrue())
			invalid(crewWith("ca-no-hosts", account(func(a *kubemootv1alpha1.ConnectedAccount) { a.TokenHosts = nil })), "tokenHosts")
			invalid(crewWith("ca-empty-hosts", account(func(a *kubemootv1alpha1.ConnectedAccount) { a.TokenHosts = []string{} })), "tokenHosts")
		})

		It("refuses a token host that is not a bare lower-case host name", func() {
			for i, bad := range []string{"https://mcp.example.org", "mcp.example.org/path", "mcp.example.org:443", "MCP.example.org", "*.example.org"} {
				err := crewWith("ca-host-"+string(rune('a'+i)), account(func(a *kubemootv1alpha1.ConnectedAccount) { a.TokenHosts = []string{bad} }))
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "host %q: %v", bad, err)
			}
		})

		It("refuses a usage other than Required or Optional", func() {
			err := crewWith("ca-usage", account(func(a *kubemootv1alpha1.ConnectedAccount) { a.Usage = "Sometimes" }))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		})

		It("refuses two declarations of one name in a crew", func() {
			err := crewWith("ca-dup", account(nil), account(nil))
			Expect(apierrors.IsInvalid(err) || apierrors.IsBadRequest(err)).To(BeTrue(), "got %v", err)
		})
	})

	Describe("MCPServer spec.registry.callerCredential.account", func() {
		serverWith := func(name string, cc *kubemootv1alpha1.CallerCredential) error {
			return createObj(&kubemootv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: credNamespace},
				Spec: kubemootv1alpha1.MCPServerSpec{
					ExternalEndpoint: credExternalURL,
					Registry:         &kubemootv1alpha1.RegistryConfig{CallerCredential: cc},
				},
			})
		}

		It("accepts an account with mode Required", func() {
			Expect(serverWith("ca-srv-ok", &kubemootv1alpha1.CallerCredential{
				Mode: kubemootv1alpha1.CallerCredentialRequired, Account: celAccount})).To(Succeed())
		})

		It("still accepts a verifier with mode Required", func() {
			Expect(serverWith("ca-srv-verifier", &kubemootv1alpha1.CallerCredential{
				Mode: kubemootv1alpha1.CallerCredentialRequired, Verifier: credVerifier})).To(Succeed())
		})

		It("refuses an account and a verifier together", func() {
			invalid(serverWith("ca-srv-both", &kubemootv1alpha1.CallerCredential{
				Mode: kubemootv1alpha1.CallerCredentialRequired, Verifier: credVerifier, Account: celAccount}), "mutually exclusive")
		})

		It("refuses an account without mode Required", func() {
			invalid(serverWith("ca-srv-off", &kubemootv1alpha1.CallerCredential{Account: celAccount}), "account needs mode Required")
			invalid(serverWith("ca-srv-off-explicit", &kubemootv1alpha1.CallerCredential{
				Mode: kubemootv1alpha1.CallerCredentialOff, Account: celAccount}), "account needs mode Required")
		})

		It("refuses mode Required with neither", func() {
			invalid(serverWith("ca-srv-neither", &kubemootv1alpha1.CallerCredential{
				Mode: kubemootv1alpha1.CallerCredentialRequired}), "verifier or account is required")
		})

		It("refuses an account that is not a DNS label", func() {
			err := serverWith("ca-srv-badname", &kubemootv1alpha1.CallerCredential{
				Mode: kubemootv1alpha1.CallerCredentialRequired, Account: "My_Library"})
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		})
	})
})
