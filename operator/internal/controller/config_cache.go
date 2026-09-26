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
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

// DefaultKubemootConfigName is the expected name for the singleton KubemootConfig
const DefaultKubemootConfigName = "default"

// Fallback defaults when no KubemootConfig exists
const (
	FallbackIndexerImage           = "ghcr.io/kubemoot/indexer:latest"
	FallbackQueryServiceImage      = "ghcr.io/kubemoot/query-service:latest"
	FallbackAgentRuntimeImage      = "ghcr.io/kubemoot/agent-runtime:latest"
	FallbackMcpGatewayImage        = "ghcr.io/kubemoot/mcp-gateway:latest"
	FallbackMcpBridgeImage         = "ghcr.io/kubemoot/mcp-bridge:latest"
	FallbackDiscussionGatewayImage = "ghcr.io/kubemoot/discussion-gateway:latest"
	FallbackDoclingServeImage      = "quay.io/docling-project/docling-serve-cpu:latest"
	FallbackFitnessRunnerImage     = "ghcr.io/kubemoot/fitness-runner:latest"
	FallbackVectorStoreType        = "pgvector"
	FallbackEmbeddingModel         = "nomic-embed"
)

// ConfigCache provides thread-safe access to the KubemootConfig
// This cache is shared among all controllers to avoid hardcoded defaults
type ConfigCache struct {
	mu     sync.RWMutex
	config *kubemootv1alpha1.KubemootConfig
}

// NewConfigCache creates a new ConfigCache instance
func NewConfigCache() *ConfigCache {
	return &ConfigCache{}
}

// Update stores a new KubemootConfig in the cache
func (c *ConfigCache) Update(config *kubemootv1alpha1.KubemootConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config = config.DeepCopy()
}

// Clear removes the cached configuration
func (c *ConfigCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config = nil
}

// GetConfig returns the current KubemootConfig or nil if not set
func (c *ConfigCache) GetConfig() *kubemootv1alpha1.KubemootConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config == nil {
		return nil
	}
	return c.config.DeepCopy()
}

// GetIndexerImage returns the indexer image from config or fallback
func (c *ConfigCache) GetIndexerImage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Images.Indexer != "" {
		return c.config.Spec.Images.Indexer
	}
	return FallbackIndexerImage
}

// GetQueryServiceImage returns the query service image from config or fallback
func (c *ConfigCache) GetQueryServiceImage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Images.QueryService != "" {
		return c.config.Spec.Images.QueryService
	}
	return FallbackQueryServiceImage
}

// GetAgentRuntimeImage returns the agent runtime image from config or fallback
func (c *ConfigCache) GetAgentRuntimeImage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Images.AgentRuntime != "" {
		return c.config.Spec.Images.AgentRuntime
	}
	return FallbackAgentRuntimeImage
}

// GetMcpGatewayImage returns the MCP gateway image from config or fallback
func (c *ConfigCache) GetMcpGatewayImage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Images.McpGateway != "" {
		return c.config.Spec.Images.McpGateway
	}
	return FallbackMcpGatewayImage
}

// GetMcpBridgeImage returns the MCP bridge image from config or fallback
func (c *ConfigCache) GetMcpBridgeImage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Images.McpBridge != "" {
		return c.config.Spec.Images.McpBridge
	}
	return FallbackMcpBridgeImage
}

// GetDiscussionGatewayImage returns the discussion gateway image from config or fallback
func (c *ConfigCache) GetDiscussionGatewayImage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Images.DiscussionGateway != "" {
		return c.config.Spec.Images.DiscussionGateway
	}
	return FallbackDiscussionGatewayImage
}

// GetDoclingServeImage returns the docling-serve image from config or fallback
func (c *ConfigCache) GetDoclingServeImage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Images.DoclingServe != "" {
		return c.config.Spec.Images.DoclingServe
	}
	return FallbackDoclingServeImage
}

// GetFitnessRunnerImage returns the fitness runner image from config or fallback
func (c *ConfigCache) GetFitnessRunnerImage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Images.FitnessRunner != "" {
		return c.config.Spec.Images.FitnessRunner
	}
	return FallbackFitnessRunnerImage
}

// Prime loads the "default" KubemootConfig once, before the manager's controllers
// start, so the first reconciles of Agents, Crews, and RAGSources already see the
// configured images and pull secrets rather than the fallbacks. A missing config is
// not an error: the cache stays on fallbacks until the KubemootConfig controller
// sees one.
func (c *ConfigCache) Prime(ctx context.Context, reader client.Reader) error {
	config := &kubemootv1alpha1.KubemootConfig{}
	err := reader.Get(ctx, client.ObjectKey{Name: DefaultKubemootConfigName}, config)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	c.Update(config)
	return nil
}

// GetImagePullSecrets returns the image pull secrets from config or default
func (c *ConfigCache) GetImagePullSecrets() []corev1.LocalObjectReference {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && len(c.config.Spec.Defaults.ImagePullSecrets) > 0 {
		// Return a copy to avoid mutation
		secrets := make([]corev1.LocalObjectReference, len(c.config.Spec.Defaults.ImagePullSecrets))
		copy(secrets, c.config.Spec.Defaults.ImagePullSecrets)
		return secrets
	}
	// No fallback: the public registry needs no pull secret. A private mirror
	// declares its secret in KubemootConfig.spec.defaults.imagePullSecrets.
	return nil
}

// GetVectorStoreType returns the default vector store type from config or fallback
func (c *ConfigCache) GetVectorStoreType() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Defaults.VectorStoreType != "" {
		return c.config.Spec.Defaults.VectorStoreType
	}
	return FallbackVectorStoreType
}

// GetEmbeddingModel returns the default embedding model from config or fallback
func (c *ConfigCache) GetEmbeddingModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Defaults.EmbeddingModel != "" {
		return c.config.Spec.Defaults.EmbeddingModel
	}
	return FallbackEmbeddingModel
}

// GetVectorStoreEndpoint returns the default vector store endpoint from config
// Returns empty string when not configured (disables automatic tool indexing)
func (c *ConfigCache) GetVectorStoreEndpoint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil {
		return c.config.Spec.Defaults.VectorStoreEndpoint
	}
	return ""
}

// GetOTelCollectorEndpoint returns the default OTel collector endpoint from config
func (c *ConfigCache) GetOTelCollectorEndpoint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil {
		return c.config.Spec.Defaults.OTelCollectorEndpoint
	}
	return ""
}

// IsConfigured returns true if a KubemootConfig has been loaded
func (c *ConfigCache) IsConfigured() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config != nil
}

// GetSchedulerConfig returns the scheduler configuration or nil if not set
func (c *ConfigCache) GetSchedulerConfig() *kubemootv1alpha1.SchedulerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.config != nil && c.config.Spec.Defaults.Scheduler != nil {
		return c.config.Spec.Defaults.Scheduler.DeepCopy()
	}
	return nil
}

// IsSchedulerEnabled returns true if the scheduler is configured and enabled
func (c *ConfigCache) IsSchedulerEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config != nil &&
		c.config.Spec.Defaults.Scheduler != nil &&
		c.config.Spec.Defaults.Scheduler.Enabled
}
