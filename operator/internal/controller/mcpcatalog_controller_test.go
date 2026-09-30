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
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

var _ = Describe("MCPCatalog Controller", func() {
	Context("When reconciling official registry catalog", func() {
		const catalogName = "test-official-registry"
		const qualityPolicyName = "test-quality-policy"

		ctx := context.Background()

		catalogNamespacedName := types.NamespacedName{
			Name:      catalogName,
			Namespace: "default",
		}

		policyNamespacedName := types.NamespacedName{
			Name:      qualityPolicyName,
			Namespace: "default",
		}

		var mockServer *httptest.Server

		BeforeEach(func() {
			// Create mock registry server with new nested format
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{
					"servers": [
						{
							"server": {
								"name": "test-kubernetes-mcp",
								"description": "Kubernetes MCP server for testing",
								"version": "1.0.0",
								"repository": {"url": "https://github.com/kubernetes/mcp-kubernetes-server", "source": "github"},
								"packages": [{"registryType": "npm", "identifier": "@kubernetes/mcp-server", "transport": {"type": "stdio"}}]
							},
							"_meta": {}
						},
						{
							"server": {
								"name": "test-blocked-mcp",
								"description": "A blocked MCP server",
								"version": "0.1.0",
								"repository": {"url": "https://github.com/blocked/mcp-server", "source": "github"},
								"packages": [{"registryType": "npm", "identifier": "@blocked/mcp-server", "transport": {"type": "stdio"}}]
							},
							"_meta": {}
						}
					]
				}`))
			}))

			// Create quality policy - match by name since registry doesn't have author field
			policy := &aiv1alpha1.MCPQualityPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      qualityPolicyName,
					Namespace: "default",
				},
				Spec: aiv1alpha1.MCPQualityPolicySpec{
					Allowing: []aiv1alpha1.AllowingEntry{
						{Name: "test-kubernetes-mcp"},
					},
					Blocking: []aiv1alpha1.BlockingEntry{
						{
							Name: &aiv1alpha1.StringMatcher{
								Type:  aiv1alpha1.MatcherTypeExact,
								Value: "test-blocked-mcp",
							},
							Reason: "Test blocking rule",
						},
					},
				},
			}
			err := k8sClient.Get(ctx, policyNamespacedName, &aiv1alpha1.MCPQualityPolicy{})
			if errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			}

			// Create catalog
			catalog := &aiv1alpha1.MCPCatalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      catalogName,
					Namespace: "default",
				},
				Spec: aiv1alpha1.MCPCatalogSpec{
					Type:             aiv1alpha1.CatalogTypeOfficialRegistry,
					URL:              mockServer.URL,
					QualityPolicyRef: qualityPolicyName,
					MaxServers:       100,
				},
			}
			err = k8sClient.Get(ctx, catalogNamespacedName, &aiv1alpha1.MCPCatalog{})
			if errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, catalog)).To(Succeed())
			}
		})

		AfterEach(func() {
			mockServer.Close()

			// Cleanup catalog
			catalog := &aiv1alpha1.MCPCatalog{}
			if err := k8sClient.Get(ctx, catalogNamespacedName, catalog); err == nil {
				Expect(k8sClient.Delete(ctx, catalog)).To(Succeed())
			}

			// Cleanup policy
			policy := &aiv1alpha1.MCPQualityPolicy{}
			if err := k8sClient.Get(ctx, policyNamespacedName, policy); err == nil {
				Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
			}
		})

		It("should sync from official registry and apply quality policy", func() {
			By("Reconciling the catalog")
			reconciler := &MCPCatalogReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				HTTPClient: mockServer.Client(),
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: catalogNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			// Verify status
			catalog := &aiv1alpha1.MCPCatalog{}
			Expect(k8sClient.Get(ctx, catalogNamespacedName, catalog)).To(Succeed())
			Expect(catalog.Status.Phase).To(Equal("Ready"))
			Expect(catalog.Status.ServersDiscovered).To(Equal(2))
			Expect(catalog.Status.ServersAllowed).To(Equal(1)) // kubernetes allowed
			Expect(catalog.Status.ServersBlocked).To(Equal(1)) // blocked-author blocked
		})
	})

	Context("When updating catalog status via catalogStatusUpdate", func() {
		const statusCatalogName = "test-status-catalog"

		ctx := context.Background()

		statusNamespacedName := types.NamespacedName{
			Name:      statusCatalogName,
			Namespace: "default",
		}

		BeforeEach(func() {
			catalog := &aiv1alpha1.MCPCatalog{
				ObjectMeta: metav1.ObjectMeta{
					Name:      statusCatalogName,
					Namespace: "default",
				},
				Spec: aiv1alpha1.MCPCatalogSpec{
					Type: aiv1alpha1.CatalogTypeOfficialRegistry,
					URL:  "http://example.invalid",
				},
			}
			err := k8sClient.Get(ctx, statusNamespacedName, &aiv1alpha1.MCPCatalog{})
			if errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, catalog)).To(Succeed())
			}
		})

		AfterEach(func() {
			catalog := &aiv1alpha1.MCPCatalog{}
			if err := k8sClient.Get(ctx, statusNamespacedName, catalog); err == nil {
				Expect(k8sClient.Delete(ctx, catalog)).To(Succeed())
			}
		})

		It("should write phase, message, and counts from the update struct", func() {
			reconciler := &MCPCatalogReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			catalog := &aiv1alpha1.MCPCatalog{}
			Expect(k8sClient.Get(ctx, statusNamespacedName, catalog)).To(Succeed())

			_, err := reconciler.updateStatusWithServers(ctx, catalog, catalogStatusUpdate{
				phase:      "Ready",
				message:    "synced",
				servers:    []aiv1alpha1.DiscoveredServer{{Name: "a"}, {Name: "b"}},
				discovered: 5,
				allowed:    2,
				blocked:    3,
			})
			Expect(err).NotTo(HaveOccurred())

			updated := &aiv1alpha1.MCPCatalog{}
			Expect(k8sClient.Get(ctx, statusNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal("Ready"))
			Expect(updated.Status.Message).To(Equal("synced"))
			Expect(updated.Status.ServersDiscovered).To(Equal(5))
			Expect(updated.Status.ServersAllowed).To(Equal(2))
			Expect(updated.Status.ServersBlocked).To(Equal(3))
			Expect(updated.Status.DiscoveredServers).To(HaveLen(2))
		})

		It("should truncate stored servers to 50 and default counts to zero via updateStatus", func() {
			reconciler := &MCPCatalogReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			catalog := &aiv1alpha1.MCPCatalog{}
			Expect(k8sClient.Get(ctx, statusNamespacedName, catalog)).To(Succeed())

			servers := make([]aiv1alpha1.DiscoveredServer, 60)
			for i := range servers {
				servers[i] = aiv1alpha1.DiscoveredServer{Name: "server"}
			}

			_, err := reconciler.updateStatus(ctx, catalog, "Pending", "many servers", servers)
			Expect(err).NotTo(HaveOccurred())

			updated := &aiv1alpha1.MCPCatalog{}
			Expect(k8sClient.Get(ctx, statusNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal("Pending"))
			Expect(updated.Status.DiscoveredServers).To(HaveLen(50))
			Expect(updated.Status.ServersDiscovered).To(Equal(0))
			Expect(updated.Status.ServersAllowed).To(Equal(0))
			Expect(updated.Status.ServersBlocked).To(Equal(0))
		})
	})

	Context("When testing version constraint matching", func() {
		It("should match caret constraints", func() {
			Expect(matchVersionConstraint("^1.2.0", "1.2.0")).To(BeTrue())
			Expect(matchVersionConstraint("^1.2.0", "1.5.0")).To(BeTrue())
			Expect(matchVersionConstraint("^1.2.0", "2.0.0")).To(BeFalse())
		})

		It("should match tilde constraints", func() {
			Expect(matchVersionConstraint("~1.2.0", "1.2.0")).To(BeTrue())
			Expect(matchVersionConstraint("~1.2.0", "1.2.5")).To(BeTrue())
			Expect(matchVersionConstraint("~1.2.0", "1.3.0")).To(BeFalse())
		})

		It("should match >= constraints", func() {
			Expect(matchVersionConstraint(">=1.0.0", "1.0.0")).To(BeTrue())
			Expect(matchVersionConstraint(">=1.0.0", "2.0.0")).To(BeTrue())
			Expect(matchVersionConstraint(">=1.0.0", "0.9.0")).To(BeFalse())
		})

		It("should match wildcard constraints", func() {
			Expect(matchVersionConstraint("1.x", "1.0.0")).To(BeTrue())
			Expect(matchVersionConstraint("1.x", "1.9.9")).To(BeTrue())
			Expect(matchVersionConstraint("1.x", "2.0.0")).To(BeFalse())
		})
	})

	Context("When testing string matcher", func() {
		It("should match exact strings case-insensitively", func() {
			matcher := aiv1alpha1.StringMatcher{Type: aiv1alpha1.MatcherTypeExact, Value: "test"}
			Expect(matchStringMatcher(matcher, "test")).To(BeTrue())
			Expect(matchStringMatcher(matcher, "TEST")).To(BeTrue())
			Expect(matchStringMatcher(matcher, "other")).To(BeFalse())
		})

		It("should match glob patterns", func() {
			matcher := aiv1alpha1.StringMatcher{Type: aiv1alpha1.MatcherTypeGlob, Value: "test-*"}
			Expect(matchStringMatcher(matcher, "test-server")).To(BeTrue())
			Expect(matchStringMatcher(matcher, "test-")).To(BeTrue())
			Expect(matchStringMatcher(matcher, "other")).To(BeFalse())
		})

		It("should negate matches when Negate is true", func() {
			matcher := aiv1alpha1.StringMatcher{Type: aiv1alpha1.MatcherTypeExact, Value: "test", Negate: true}
			Expect(matchStringMatcher(matcher, "test")).To(BeFalse())
			Expect(matchStringMatcher(matcher, "other")).To(BeTrue())
		})
	})
})
