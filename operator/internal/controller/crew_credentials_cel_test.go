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

// The Crew and MCPServer CRDs hold the rules of the per-person fields at the API server, so
// they hold where the admission webhook is not installed, and every field is optional.
var _ = Describe("Per-person access admission validation", func() {
	createObj := func(obj client.Object) error {
		err := k8sClient.Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		}
		return err
	}
	crewWith := func(name string, access *kubemootv1alpha1.DiscussionAccess) error {
		return createObj(&kubemootv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: credNamespace},
			Spec:       kubemootv1alpha1.CrewSpec{Discussion: &kubemootv1alpha1.DiscussionConfig{Access: access}},
		})
	}
	hashKey := &kubemootv1alpha1.HashKeySecretRef{Name: "hash"}
	verifier := func(mutate func(*kubemootv1alpha1.ServiceTokenVerifier)) *kubemootv1alpha1.DiscussionAccess {
		v := kubemootv1alpha1.ServiceTokenVerifier{Name: "oreilly", VerifyURL: credVerifyURL, SubjectClaim: "upstream_claims.sub"}
		mutate(&v)
		return &kubemootv1alpha1.DiscussionAccess{ServiceTokens: []kubemootv1alpha1.ServiceTokenVerifier{v}, HashKeySecretRef: hashKey}
	}
	invalid := func(err error, substring string) {
		GinkgoHelper()
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring(substring))
	}

	Describe("Crew spec.discussion.access.serviceTokens", func() {
		It("is optional: a crew with no access, or access with no verifier, is accepted as before", func() {
			Expect(crewWith("cel-st-none", nil)).To(Succeed())
			Expect(crewWith("cel-st-empty", &kubemootv1alpha1.DiscussionAccess{})).To(Succeed())
		})

		It("defaults a verifier to McpInitialize, 300 seconds, AnyAccepted, and no pass to tools", func() {
			Expect(crewWith("cel-st-defaults", verifier(func(*kubemootv1alpha1.ServiceTokenVerifier) {}))).To(Succeed())
			got := &kubemootv1alpha1.Crew{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: credNamespace, Name: "cel-st-defaults"}, got)).To(Succeed())
			v := got.Spec.Discussion.Access.ServiceTokens[0]
			Expect(v.VerifyMethod).To(Equal("McpInitialize"))
			Expect(v.MaxCacheSeconds).To(Equal(int32(300)))
			Expect(v.Authorize).To(Equal("AnyAccepted"))
			Expect(v.PassToTools).To(BeFalse())
			Expect(got.Spec.Discussion.Access.HashKeySecretRef.Key).To(Equal("hash-key"))
		})

		It("accepts the full form", func() {
			Expect(crewWith("cel-st-full", verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) {
				v.PassToTools = true
				v.MaxCacheSeconds = 900
				v.Audiences = []string{"a"}
				v.Authorize = "Subjects"
				v.Subjects = []string{"u1"}
			}))).To(Succeed())
		})

		It("rejects a verifyURL that is not https", func() {
			invalid(crewWith("cel-st-http", verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) {
				v.VerifyURL = "http://api.example.com/mcp"
			})), "verifyURL")
		})

		It("rejects a name that is not a DNS label, and the Kubernetes issuer", func() {
			invalid(crewWith("cel-st-name", verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) { v.Name = "O'Reilly" })), "name")
			invalid(crewWith("cel-st-k8s", verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) { v.Name = credKubernetesName })), "issuer")
		})

		It("rejects a duplicate name", func() {
			access := verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) { v.Audiences = []string{"a"} })
			access.ServiceTokens = append(access.ServiceTokens, access.ServiceTokens[0])
			err := crewWith("cel-st-dup", access)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
			Expect(err.Error()).To(ContainSubstring("Duplicate"))
		})

		It("rejects a claim path that is not dotted words", func() {
			invalid(crewWith("cel-st-claim", verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) {
				v.SubjectClaim = "upstream_claims..sub"
			})), "subjectClaim")
		})

		It("rejects a cache bound outside 1 to 900", func() {
			invalid(crewWith("cel-st-cache", verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) { v.MaxCacheSeconds = 901 })), "maxCacheSeconds")
		})

		It("rejects Subjects with no subject, and subjects without Subjects", func() {
			invalid(crewWith("cel-st-subj1", verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) {
				v.Authorize = "Subjects"
			})), "at least one subject")
			invalid(crewWith("cel-st-subj2", verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) {
				v.Subjects = []string{"u1"}
			})), "only used with authorize Subjects")
		})

		It("rejects verifiers without the hash key Secret", func() {
			access := verifier(func(*kubemootv1alpha1.ServiceTokenVerifier) {})
			access.HashKeySecretRef = nil
			invalid(crewWith("cel-st-nokey", access), "hashKeySecretRef")
		})

		It("rejects two verifiers when one has no audiences", func() {
			access := verifier(func(v *kubemootv1alpha1.ServiceTokenVerifier) { v.Audiences = []string{"a"} })
			second := access.ServiceTokens[0]
			second.Name = "second"
			second.Audiences = nil
			access.ServiceTokens = append(access.ServiceTokens, second)
			invalid(crewWith("cel-st-aud", access), "audiences")
		})
	})

	Describe("MCPServer spec.registry.callerCredential", func() {
		serverWith := func(name string, cc *kubemootv1alpha1.CallerCredential) error {
			return createObj(&kubemootv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: credNamespace},
				Spec: kubemootv1alpha1.MCPServerSpec{
					ExternalEndpoint: credExternalURL,
					Registry:         &kubemootv1alpha1.RegistryConfig{CallerCredential: cc},
				},
			})
		}
		required := func(mutate func(*kubemootv1alpha1.CallerCredential)) *kubemootv1alpha1.CallerCredential {
			cc := &kubemootv1alpha1.CallerCredential{Mode: kubemootv1alpha1.CallerCredentialRequired, Verifier: "oreilly"}
			mutate(cc)
			return cc
		}

		It("is optional and defaults to Off with no relay", func() {
			Expect(serverWith("cel-cc-none", nil)).To(Succeed())
			Expect(serverWith("cel-cc-empty", &kubemootv1alpha1.CallerCredential{})).To(Succeed())
			got := &kubemootv1alpha1.MCPServer{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: credNamespace, Name: "cel-cc-empty"}, got)).To(Succeed())
			Expect(got.Spec.Registry.CallerCredential.Mode).To(Equal(kubemootv1alpha1.CallerCredentialOff))
			Expect(got.Spec.Registry.CallerCredential.RelayContext).To(BeFalse())
		})

		It("accepts https fallbacks and loopback http", func() {
			Expect(serverWith("cel-cc-ok", required(func(cc *kubemootv1alpha1.CallerCredential) {
				cc.OnForbidden = []string{credVerifyURL, "http://localhost:8080/mcp", "http://127.0.0.1/mcp", "http://[::1]:9/mcp"}
			}))).To(Succeed())
		})

		It("rejects an onForbidden endpoint that is plain http to a non-loopback host", func() {
			for i, bad := range []string{"http://api.example.com/mcp", "http://localhost.evil.example/mcp", "ftp://x", "http://127.0.0.1.evil.example/mcp"} {
				cc := required(func(cc *kubemootv1alpha1.CallerCredential) { cc.OnForbidden = []string{bad} })
				invalid(serverWith("cel-cc-bad-"+string(rune('a'+i)), cc), "https")
			}
		})

		It("rejects more than four fallbacks", func() {
			cc := required(func(cc *kubemootv1alpha1.CallerCredential) {
				cc.OnForbidden = []string{"https://a", "https://b", "https://c", "https://d", "https://e"}
			})
			Expect(apierrors.IsInvalid(serverWith("cel-cc-many", cc))).To(BeTrue())
		})

		It("rejects mode Required with no verifier", func() {
			invalid(serverWith("cel-cc-nover", &kubemootv1alpha1.CallerCredential{Mode: kubemootv1alpha1.CallerCredentialRequired}), "verifier")
		})

		It("rejects mode Required on a transport other than http", func() {
			err := createObj(&kubemootv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "cel-cc-sse", Namespace: credNamespace},
				Spec: kubemootv1alpha1.MCPServerSpec{
					ExternalEndpoint: credExternalURL, Transport: kubemootv1alpha1.TransportSSE,
					Registry: &kubemootv1alpha1.RegistryConfig{CallerCredential: required(func(*kubemootv1alpha1.CallerCredential) {})},
				},
			})
			invalid(err, "transport http")
		})

		It("rejects a mode that is not Off or Required", func() {
			Expect(apierrors.IsInvalid(serverWith("cel-cc-mode", &kubemootv1alpha1.CallerCredential{Mode: "Sometimes"}))).To(BeTrue())
		})
	})
})
