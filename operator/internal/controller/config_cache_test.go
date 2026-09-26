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
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

func TestConfigCache_Prime(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Run("no KubemootConfig keeps fallbacks", func(t *testing.T) {
		cache := NewConfigCache()
		reader := fake.NewClientBuilder().WithScheme(scheme).Build()
		if err := cache.Prime(ctx, reader); err != nil {
			t.Fatalf("missing config must not be an error, got %v", err)
		}
		if cache.GetConfig() != nil {
			t.Errorf("cache must stay empty without a config")
		}
	})

	t.Run("existing default config is loaded", func(t *testing.T) {
		cache := NewConfigCache()
		cfg := &kubemootv1alpha1.KubemootConfig{
			ObjectMeta: metav1.ObjectMeta{Name: DefaultKubemootConfigName},
			Spec: kubemootv1alpha1.KubemootConfigSpec{
				Images: kubemootv1alpha1.ImageConfig{AgentRuntime: "registry.example/agent-runtime:1.2.3"},
				Defaults: kubemootv1alpha1.DefaultConfig{
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: "mirror-pull-secret"}},
				},
			},
		}
		reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).Build()
		if err := cache.Prime(ctx, reader); err != nil {
			t.Fatal(err)
		}
		if got := cache.GetAgentRuntimeImage(); got != "registry.example/agent-runtime:1.2.3" {
			t.Errorf("image not primed, got %q", got)
		}
		if got := cache.GetImagePullSecrets(); len(got) != 1 || got[0].Name != "mirror-pull-secret" {
			t.Errorf("pull secrets not primed, got %v", got)
		}
	})

	t.Run("other read errors are returned", func(t *testing.T) {
		cache := NewConfigCache()
		boom := errors.New("api server unavailable")
		reader := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return boom
			},
		}).Build()
		if err := cache.Prime(ctx, reader); !errors.Is(err, boom) {
			t.Errorf("want the read error surfaced, got %v", err)
		}
		if cache.GetConfig() != nil {
			t.Errorf("cache must stay empty after a failed read")
		}
	})
}

func TestConfigCache_ImageGetters_Fallback(t *testing.T) {
	cache := NewConfigCache()

	tests := []struct {
		name     string
		getter   func() string
		expected string
	}{
		{"IndexerFallback", cache.GetIndexerImage, FallbackIndexerImage},
		{"QueryServiceFallback", cache.GetQueryServiceImage, FallbackQueryServiceImage},
		{"AgentRuntimeFallback", cache.GetAgentRuntimeImage, FallbackAgentRuntimeImage},
		{"McpGatewayFallback", cache.GetMcpGatewayImage, FallbackMcpGatewayImage},
		{"McpBridgeFallback", cache.GetMcpBridgeImage, FallbackMcpBridgeImage},
		{"DoclingServeFallback", cache.GetDoclingServeImage, FallbackDoclingServeImage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.getter(); got != tt.expected {
				t.Errorf("got %s, want %s", got, tt.expected)
			}
		})
	}
}

func TestConfigCache_ImageGetters_Configured(t *testing.T) {
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{
			Images: kubemootv1alpha1.ImageConfig{
				Indexer:      "custom/indexer:v1",
				QueryService: "custom/query:v1",
				AgentRuntime: "custom/agent:v1",
				McpGateway:   "custom/gateway:v1",
				McpBridge:    "custom/bridge:v1",
				DoclingServe: "custom/docling:v1",
			},
		},
	})

	tests := []struct {
		name     string
		getter   func() string
		expected string
	}{
		{"Indexer", cache.GetIndexerImage, "custom/indexer:v1"},
		{"QueryService", cache.GetQueryServiceImage, "custom/query:v1"},
		{"AgentRuntime", cache.GetAgentRuntimeImage, "custom/agent:v1"},
		{"McpGateway", cache.GetMcpGatewayImage, "custom/gateway:v1"},
		{"McpBridge", cache.GetMcpBridgeImage, "custom/bridge:v1"},
		{"DoclingServe", cache.GetDoclingServeImage, "custom/docling:v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.getter(); got != tt.expected {
				t.Errorf("got %s, want %s", got, tt.expected)
			}
		})
	}
}

func TestConfigCache_GetImagePullSecrets_Fallback(t *testing.T) {
	cache := NewConfigCache()
	secrets := cache.GetImagePullSecrets()
	if len(secrets) != 0 {
		t.Errorf("expected no fallback pull secret (public registry), got %v", secrets)
	}
}

func TestConfigCache_GetImagePullSecrets_FromConfig(t *testing.T) {
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{
			Defaults: kubemootv1alpha1.DefaultConfig{
				ImagePullSecrets: []corev1.LocalObjectReference{
					{Name: "my-secret"},
				},
			},
		},
	})
	secrets := cache.GetImagePullSecrets()
	if len(secrets) != 1 || secrets[0].Name != "my-secret" {
		t.Errorf("expected my-secret, got %v", secrets)
	}
}

func TestConfigCache_GetVectorStoreType(t *testing.T) {
	cache := NewConfigCache()
	if got := cache.GetVectorStoreType(); got != FallbackVectorStoreType {
		t.Errorf("fallback: got %s, want %s", got, FallbackVectorStoreType)
	}

	cache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{
			Defaults: kubemootv1alpha1.DefaultConfig{
				VectorStoreType: "qdrant",
			},
		},
	})
	if got := cache.GetVectorStoreType(); got != "qdrant" {
		t.Errorf("configured: got %s, want qdrant", got)
	}
}

func TestConfigCache_GetEmbeddingModel(t *testing.T) {
	cache := NewConfigCache()
	if got := cache.GetEmbeddingModel(); got != FallbackEmbeddingModel {
		t.Errorf("fallback: got %s, want %s", got, FallbackEmbeddingModel)
	}

	cache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{
			Defaults: kubemootv1alpha1.DefaultConfig{
				EmbeddingModel: "bge-large",
			},
		},
	})
	if got := cache.GetEmbeddingModel(); got != "bge-large" {
		t.Errorf("configured: got %s, want bge-large", got)
	}
}

func TestConfigCache_IsConfigured(t *testing.T) {
	cache := NewConfigCache()
	if cache.IsConfigured() {
		t.Error("expected not configured when new")
	}

	cache.Update(&kubemootv1alpha1.KubemootConfig{})
	if !cache.IsConfigured() {
		t.Error("expected configured after Update")
	}
}

func TestConfigCache_Clear_ResetsToNil(t *testing.T) {
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{
			Images: kubemootv1alpha1.ImageConfig{Indexer: "custom:v1"},
		},
	})
	cache.Clear()

	if cache.IsConfigured() {
		t.Error("expected not configured after Clear")
	}
	if got := cache.GetIndexerImage(); got != FallbackIndexerImage {
		t.Errorf("expected fallback after Clear, got %s", got)
	}
}

func TestConfigCache_IsSchedulerEnabled(t *testing.T) {
	cache := NewConfigCache()
	if cache.IsSchedulerEnabled() {
		t.Error("expected false when nil")
	}

	cache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{
			Defaults: kubemootv1alpha1.DefaultConfig{
				Scheduler: &kubemootv1alpha1.SchedulerConfig{
					Enabled: true,
				},
			},
		},
	})
	if !cache.IsSchedulerEnabled() {
		t.Error("expected true when enabled")
	}
}
