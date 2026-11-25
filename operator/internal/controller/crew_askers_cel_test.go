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

// defaultResourceType is the resource type the CRD defaults.
const defaultResourceType = "crew"

var _ = Describe("Crew spec.discussion.access.askers admission validation", func() {
	const pemKey = "-----BEGIN PUBLIC KEY-----\nMFkw\n-----END PUBLIC KEY-----\n"
	hops := []kubemootv1alpha1.TrustedHop{{Name: askersHop, PublicKeys: []string{pemKey}}}
	hash := &kubemootv1alpha1.HashKeySecretRef{Name: askersHash}
	rbac := &kubemootv1alpha1.AskerRule{KubernetesRBAC: &kubemootv1alpha1.KubernetesRBACAskers{}}

	create := func(name string, access *kubemootv1alpha1.DiscussionAccess, accounts ...kubemootv1alpha1.ConnectedAccount) error {
		crew := &kubemootv1alpha1.Crew{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: credNamespace},
			Spec: kubemootv1alpha1.CrewSpec{
				Discussion:        &kubemootv1alpha1.DiscussionConfig{Access: access},
				ConnectedAccounts: accounts,
			},
		}
		err := k8sClient.Create(ctx, crew)
		if err == nil {
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, crew) })
		}
		return err
	}
	refused := func(err error, substring string) {
		GinkgoHelper()
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring(substring))
	}
	library := kubemootv1alpha1.ConnectedAccount{
		Name: celAccount, DisplayName: "Example Library", Scopes: []string{"openid"},
		AuthorizeURL: "https://auth.example.org/a", TokenURL: "https://auth.example.org/t", TokenHosts: []string{"mcp.example.org"},
	}

	It("accepts each kind", func() {
		Expect(create("askers-rbac", &kubemootv1alpha1.DiscussionAccess{TrustedHops: hops, Askers: rbac})).To(Succeed())
		Expect(create("askers-account", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash, Askers: &kubemootv1alpha1.AskerRule{ConnectedAccount: celAccount},
		}, library)).To(Succeed())
		Expect(create("askers-groups", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash, Askers: &kubemootv1alpha1.AskerRule{IssuerGroups: []string{"oidc:staff"}},
		})).To(Succeed())
		Expect(create("askers-any", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash, Askers: &kubemootv1alpha1.AskerRule{AnySignedIn: &kubemootv1alpha1.AnySignedInAskers{}},
		})).To(Succeed())
		Expect(create("askers-external", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash,
			Askers: &kubemootv1alpha1.AskerRule{External: &kubemootv1alpha1.ExternalAskers{URL: "https://pdp.example.org/access/v1/evaluation"}},
		})).To(Succeed())
	})

	It("defaults the external rule's names", func() {
		Expect(create("askers-defaults", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash,
			Askers: &kubemootv1alpha1.AskerRule{External: &kubemootv1alpha1.ExternalAskers{URL: askersPDP}},
		})).To(Succeed())
		got := &kubemootv1alpha1.Crew{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: credNamespace, Name: "askers-defaults"}, got)).To(Succeed())
		e := got.Spec.Discussion.Access.Askers.External
		Expect([]string{e.SubjectType, e.ResourceType, e.Action}).To(Equal([]string{"user", defaultResourceType, "ask"}))
	})

	It("refuses a rule with no kind or with two", func() {
		refused(create("askers-none", &kubemootv1alpha1.DiscussionAccess{TrustedHops: hops, Askers: &kubemootv1alpha1.AskerRule{}}), "exactly one")
		refused(create("askers-two", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash,
			Askers: &kubemootv1alpha1.AskerRule{KubernetesRBAC: &kubemootv1alpha1.KubernetesRBACAskers{}, AnySignedIn: &kubemootv1alpha1.AnySignedInAskers{}},
		}), "exactly one")
	})

	It("refuses trusted hops with no rule and a rule with no hops", func() {
		refused(create("askers-norule", &kubemootv1alpha1.DiscussionAccess{TrustedHops: hops}), "trustedHops needs askers")
		refused(create("askers-nohops", &kubemootv1alpha1.DiscussionAccess{Askers: rbac}), "askers needs trustedHops")
	})

	It("needs the hash key for every kind but kubernetesRBAC", func() {
		refused(create("askers-nohash", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, Askers: &kubemootv1alpha1.AskerRule{AnySignedIn: &kubemootv1alpha1.AnySignedInAskers{}},
		}), "hashKeySecretRef")
	})

	It("needs the named connected account to be declared", func() {
		refused(create("askers-undeclared", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash, Askers: &kubemootv1alpha1.AskerRule{ConnectedAccount: celAccount},
		}), "spec.connectedAccounts")
	})

	It("refuses an empty group list, a bad account name and a plain http decision point", func() {
		refused(create("askers-nogroups", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash, Askers: &kubemootv1alpha1.AskerRule{IssuerGroups: []string{}},
		}), "exactly one")
		Expect(apierrors.IsInvalid(create("askers-badname", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash, Askers: &kubemootv1alpha1.AskerRule{ConnectedAccount: "Not A Label"},
		}, library))).To(BeTrue())
		Expect(apierrors.IsInvalid(create("askers-http", &kubemootv1alpha1.DiscussionAccess{
			TrustedHops: hops, HashKeySecretRef: hash,
			Askers: &kubemootv1alpha1.AskerRule{External: &kubemootv1alpha1.ExternalAskers{URL: "http://pdp.example.org/x"}},
		}))).To(BeTrue())
	})
})
