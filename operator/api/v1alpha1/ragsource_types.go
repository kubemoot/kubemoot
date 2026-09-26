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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RAGSourceType defines the type of source
type RAGSourceType string

const (
	// RAGSourceTypeGit is a git repository source
	RAGSourceTypeGit RAGSourceType = "git"
	// RAGSourceTypeS3 is an S3 bucket source
	RAGSourceTypeS3 RAGSourceType = "s3"
	// RAGSourceTypeURL is a URL source
	RAGSourceTypeURL RAGSourceType = "url"
	// RAGSourceTypeMCPRegistry is an MCP registry source for tool indexing
	RAGSourceTypeMCPRegistry RAGSourceType = "mcp-registry"
	// RAGSourceTypeDocument is a document source (PDF, DOCX, PPTX, images) converted via docling
	RAGSourceTypeDocument RAGSourceType = "document"
	// RAGSourceTypeNatsKV is a NATS KV bucket source
	RAGSourceTypeNatsKV RAGSourceType = "nats-kv"
)

// VectorStoreType defines the type of vector store
type VectorStoreType string

const (
	// VectorStorePgvector is PostgreSQL with pgvector extension
	VectorStorePgvector VectorStoreType = "pgvector"
	// VectorStoreQdrant is Qdrant vector database
	VectorStoreQdrant VectorStoreType = "qdrant"
	// VectorStoreMilvus is Milvus vector database
	VectorStoreMilvus VectorStoreType = "milvus"
	// VectorStoreChroma is Chroma vector database
	VectorStoreChroma VectorStoreType = "chroma"
)

// GitSource defines a git repository source
type GitSource struct {
	// URL is the git repository URL
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Branch is the git branch to use
	// +kubebuilder:default=main
	// +optional
	Branch string `json:"branch,omitempty"`

	// Paths are glob patterns for files to include
	// +optional
	Paths []string `json:"paths,omitempty"`

	// SecretRef references a secret containing git credentials
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// S3Source defines an S3 bucket source
type S3Source struct {
	// Bucket is the S3 bucket name
	// +kubebuilder:validation:Required
	Bucket string `json:"bucket"`

	// Prefix is the S3 key prefix to use
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// Region is the AWS region
	// +optional
	Region string `json:"region,omitempty"`

	// Endpoint is a custom S3 endpoint (for S3-compatible stores)
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// SecretRef references a secret containing AWS credentials
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// URLSource defines a URL source
type URLSource struct {
	// URLs is a list of URLs to fetch
	// +kubebuilder:validation:Required
	URLs []string `json:"urls"`
}

// DocumentSource defines a document source for rich document conversion
// Documents are converted to markdown via docling-serve sidecar
type DocumentSource struct {
	// URLs of documents to convert (PDF, DOCX, PPTX, HTML, images)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	URLs []string `json:"urls"`

	// DisableOCR skips OCR for text-based PDFs (faster)
	// +kubebuilder:default=false
	// +optional
	DisableOCR bool `json:"disableOcr,omitempty"`
}

// NatsKVSource defines a NATS KV bucket source
type NatsKVSource struct {
	// Bucket is the NATS KV bucket name
	// +kubebuilder:validation:Required
	Bucket string `json:"bucket"`

	// Key is the key within the bucket
	// +kubebuilder:validation:Required
	Key string `json:"key"`

	// ContentHash is a SHA-256 hash of the NATS KV data content.
	// Updated by the agent controller when resume data changes,
	// causing spec generation to increment and triggering re-indexing.
	// +optional
	ContentHash string `json:"contentHash,omitempty"`
}

// RAGMCPRegistrySource defines an MCP registry source for tool indexing in RAGSource
// This is distinct from MCPRegistrySource in MCPGateway which includes a Name field
type RAGMCPRegistrySource struct {
	// URL is the MCP registry API endpoint (e.g., https://api.mcp.run)
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Type is the registry protocol type (mcp-run, smithery, custom)
	// +kubebuilder:validation:Enum=mcp-run;smithery;custom
	// +kubebuilder:default=mcp-run
	// +optional
	Type string `json:"type,omitempty"`

	// AuthSecretRef references a secret for registry authentication
	// +optional
	AuthSecretRef string `json:"authSecretRef,omitempty"`

	// Filter restricts which servers/tools to index
	// +optional
	Filter *RAGMCPRegistryFilter `json:"filter,omitempty"`
}

// RAGMCPRegistryFilter defines filters for MCP registry indexing in RAGSource
type RAGMCPRegistryFilter struct {
	// Categories limits to specific tool categories (e.g., ["kubernetes", "database"])
	// +optional
	Categories []string `json:"categories,omitempty"`

	// MinRating filters by minimum tool rating (as string, e.g., "4.0")
	// +optional
	MinRating string `json:"minRating,omitempty"`
}

// SourceConfig defines the document source configuration
type SourceConfig struct {
	// Type is the source type (git, s3, url, mcp-registry, document, nats-kv)
	// +kubebuilder:validation:Enum=git;s3;url;mcp-registry;document;nats-kv
	// +kubebuilder:validation:Required
	Type RAGSourceType `json:"type"`

	// Git is the git source configuration
	// +optional
	Git *GitSource `json:"git,omitempty"`

	// S3 is the S3 source configuration
	// +optional
	S3 *S3Source `json:"s3,omitempty"`

	// URL is the URL source configuration
	// +optional
	URL *URLSource `json:"url,omitempty"`

	// MCPRegistry is the MCP registry source configuration for tool indexing
	// +optional
	MCPRegistry *RAGMCPRegistrySource `json:"mcpRegistry,omitempty"`

	// Document is the document source configuration for rich document conversion
	// +optional
	Document *DocumentSource `json:"document,omitempty"`

	// NatsKV is the NATS KV source configuration
	// +optional
	NatsKV *NatsKVSource `json:"natsKV,omitempty"`
}

// VectorStoreConfig defines the vector store configuration
type VectorStoreConfig struct {
	// Type is the vector store type
	// +kubebuilder:validation:Enum=pgvector;qdrant;milvus;chroma
	// +kubebuilder:validation:Required
	Type VectorStoreType `json:"type"`

	// Endpoint is the vector store connection endpoint
	// For pgvector: postgres://user:pass@host:5432/dbname
	// For qdrant: http://qdrant:6333
	// +kubebuilder:validation:Required
	Endpoint string `json:"endpoint"`

	// SecretRef references a secret containing credentials
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// Collection is the collection/table name for vectors
	// +kubebuilder:validation:Required
	Collection string `json:"collection"`

	// Dimensions is the vector dimension size (must match embedding model)
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=768
	// +optional
	Dimensions int32 `json:"dimensions,omitempty"`

	// DistanceMetric is the distance metric for similarity search
	// +kubebuilder:validation:Enum=cosine;euclidean;dotProduct
	// +kubebuilder:default=cosine
	// +optional
	DistanceMetric string `json:"distanceMetric,omitempty"`
}

// ChunkingConfig defines how documents are chunked
type ChunkingConfig struct {
	// ChunkSize is the target chunk size in characters
	// +kubebuilder:validation:Minimum=100
	// +kubebuilder:default=512
	// +optional
	ChunkSize int32 `json:"chunkSize,omitempty"`

	// ChunkOverlap is the overlap between chunks in characters
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=50
	// +optional
	ChunkOverlap int32 `json:"chunkOverlap,omitempty"`
}

// IndexerScriptSource defines where to get the indexer script from
type IndexerScriptSource struct {
	// ConfigMapRef references a ConfigMap containing the script
	// +optional
	ConfigMapRef *corev1.LocalObjectReference `json:"configMapRef,omitempty"`

	// Inline is the script content embedded in the RAGSource
	// +optional
	Inline string `json:"inline,omitempty"`

	// ScriptKey is the key in the ConfigMap (default: indexer.py)
	// +kubebuilder:default=indexer.py
	// +optional
	ScriptKey string `json:"scriptKey,omitempty"`
}

// QueryServiceConfig defines the query service deployment configuration
type QueryServiceConfig struct {
	// Enabled controls whether to auto-deploy a query service for this RAGSource
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Image overrides the query service container image for this RAGSource.
	// Empty means the KubemootConfig default (kept current by CI); no schema
	// default here, or every RAGSource would silently pin its own copy.
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas is the number of query service replicas
	// +kubebuilder:default=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Port is the HTTP port for the query service
	// +kubebuilder:default=8000
	// +optional
	Port int32 `json:"port,omitempty"`

	// Resources defines resource requirements for the query service
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// TopK is the default number of results to return
	// +kubebuilder:default=5
	// +optional
	TopK int32 `json:"topK,omitempty"`
}

// IndexerConfig defines the indexer job configuration
type IndexerConfig struct {
	// Image is the indexer container image. Leave empty to use the operator's
	// KubemootConfig image (Spec.Images.Indexer) — do NOT hardcode a default
	// here: a stale literal (was indexer:0.7.0) silently overrides KubemootConfig
	// because the Job-image priority is Spec.Indexer.Image > KubemootConfig, so a
	// CRD default makes the field always "set" and pins every RAGSource to a dead
	// tag. Empty → operator resolves the current indexer image.
	// +optional
	Image string `json:"image,omitempty"`

	// Script defines a custom indexer script (Mode 2 or 3)
	// If not specified, uses the default indexer (Mode 1: declarative)
	// +optional
	Script *IndexerScriptSource `json:"script,omitempty"`

	// Schedule is the cron schedule for re-indexing (optional)
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Delay is the duration to wait before first indexing (e.g., "5m", "1h", "30s")
	// This allows staggering of indexing jobs to avoid resource contention.
	// Format: Unix duration (1s, 5m, 2h, 1d)
	// +kubebuilder:validation:Pattern=`^(\d+[smhd])?$`
	// +optional
	Delay string `json:"delay,omitempty"`

	// ForceReindex bypasses checksum-based change detection and always runs indexing.
	// Default: false (use checksum to skip indexing if source unchanged)
	// +kubebuilder:default=false
	// +optional
	ForceReindex bool `json:"forceReindex,omitempty"`

	// Resources defines resource requirements for the indexer job
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Env is additional environment variables for the indexer
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// ServiceAccountName is the service account for the indexer job
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// TTLSecondsAfterFinished is how long to keep completed jobs
	// +kubebuilder:default=3600
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// BackoffLimit is the number of retries before marking as failed
	// +kubebuilder:default=3
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
}

// RAGSourceSpec defines the desired state of RAGSource
type RAGSourceSpec struct {
	// Source defines where to get documents from
	// +kubebuilder:validation:Required
	Source SourceConfig `json:"source"`

	// VectorStore defines where to store vectors
	// +kubebuilder:validation:Required
	VectorStore VectorStoreConfig `json:"vectorStore"`

	// EmbeddingModelRef references the EmbeddingModel to use
	// +kubebuilder:validation:Required
	EmbeddingModelRef string `json:"embeddingModelRef"`

	// Chunking defines how to chunk documents
	// +optional
	Chunking *ChunkingConfig `json:"chunking,omitempty"`

	// Indexer defines the indexer job configuration
	// If not specified, uses defaults (Kubemoot indexer image, declarative mode)
	// +optional
	Indexer *IndexerConfig `json:"indexer,omitempty"`

	// QueryService defines the query service deployment configuration
	// By default, a query service is auto-deployed for each RAGSource
	// +optional
	QueryService *QueryServiceConfig `json:"queryService,omitempty"`
}

// IndexingStats contains statistics from the last indexing run
type IndexingStats struct {
	// DocumentCount is the number of documents indexed
	DocumentCount int32 `json:"documentCount,omitempty"`

	// ChunkCount is the number of chunks created
	ChunkCount int32 `json:"chunkCount,omitempty"`

	// Duration is how long indexing took
	Duration string `json:"duration,omitempty"`

	// LastIndexed is when indexing last completed
	LastIndexed *metav1.Time `json:"lastIndexed,omitempty"`
}

// RAGSourceStatus defines the observed state of RAGSource
type RAGSourceStatus struct {
	// Phase is the current phase (Pending, Indexing, Ready, Error)
	Phase string `json:"phase,omitempty"`

	// Ready indicates if the RAG source is ready for queries
	Ready bool `json:"ready,omitempty"`

	// QueryEndpoint is the URL for the auto-deployed query service
	// Agents use this endpoint to perform semantic search
	// +optional
	QueryEndpoint string `json:"queryEndpoint,omitempty"`

	// IndexingStats contains statistics from the last indexing run
	// +optional
	IndexingStats *IndexingStats `json:"indexingStats,omitempty"`

	// LastJobName is the name of the last indexing job
	LastJobName string `json:"lastJobName,omitempty"`

	// NextIndexTime is when the next scheduled indexing will occur
	// +optional
	NextIndexTime *metav1.Time `json:"nextIndexTime,omitempty"`

	// DelayUntil is when the initial delay period ends
	// If set and in the future, indexing will not start until this time
	// +optional
	DelayUntil *metav1.Time `json:"delayUntil,omitempty"`

	// Topics are keywords extracted from the indexed content.
	// Used for automatic RAGSource-to-Agent matching via ragAutoDiscover.
	// Populated by the indexer during indexing.
	// +optional
	Topics []string `json:"topics,omitempty"`

	// ObservedGeneration is the generation of the spec that was last indexed.
	// Used to detect spec changes and trigger re-indexing automatically.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastIndexedChecksum is the checksum of the source data from the last successful indexing
	// Used to skip re-indexing when source hasn't changed
	// +optional
	LastIndexedChecksum string `json:"lastIndexedChecksum,omitempty"`

	// Message provides additional status information
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rag
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.type`
// +kubebuilder:printcolumn:name="VectorStore",type=string,JSONPath=`.spec.vectorStore.type`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Documents",type=integer,JSONPath=`.status.indexingStats.documentCount`
// +kubebuilder:printcolumn:name="QueryEndpoint",type=string,JSONPath=`.status.queryEndpoint`,priority=1
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RAGSource is the Schema for the ragsources API
type RAGSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RAGSourceSpec   `json:"spec,omitempty"`
	Status RAGSourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RAGSourceList contains a list of RAGSource
type RAGSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RAGSource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RAGSource{}, &RAGSourceList{})
}
