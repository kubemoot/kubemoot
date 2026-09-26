package ai.kubemoot.indexer.config;

import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.boot.context.properties.bind.DefaultValue;

import java.util.List;

/**
 * Configuration for Kubemoot Indexer.
 * Maps KUBEMOOT_* environment variables to configuration properties.
 *
 * Environment variables (same as Python indexer):
 *   KUBEMOOT_SOURCE_TYPE          - Source type: git, s3, url, mcp-registry
 *   KUBEMOOT_GIT_URL              - Git repository URL
 *   KUBEMOOT_GIT_BRANCH           - Git branch (default: main)
 *   KUBEMOOT_GIT_PATHS            - Comma-separated paths to index
 *   KUBEMOOT_S3_BUCKET            - S3 bucket name
 *   KUBEMOOT_S3_PREFIX            - S3 key prefix
 *   KUBEMOOT_S3_REGION            - S3 region (default: us-east-1)
 *   KUBEMOOT_URL                  - URL to fetch documents from
 *   KUBEMOOT_MCP_REGISTRY_URL     - MCP registry URL (e.g., https://mcp.run/api/v1)
 *   KUBEMOOT_MCP_REGISTRY_FILTER  - Comma-separated categories to filter
 *   KUBEMOOT_DOCUMENT_URLS        - Comma-separated document URLs (PDF, DOCX, PPTX, images)
 *   KUBEMOOT_DOCLING_ENDPOINT     - Docling-serve endpoint (default: http://localhost:5001)
 *   KUBEMOOT_DOCUMENT_DISABLE_OCR - "true" to skip OCR for text-based PDFs
 *   KUBEMOOT_NATS_KV_BUCKET       - NATS KV bucket name (for nats-kv source)
 *   KUBEMOOT_NATS_KV_KEY          - NATS KV key (for nats-kv source)
 *   KUBEMOOT_VECTORSTORE_TYPE     - Vector store: pgvector (default)
 *   KUBEMOOT_VECTORSTORE_ENDPOINT - Vector store connection string
 *   KUBEMOOT_VECTORSTORE_COLLECTION - Collection/table name
 *   KUBEMOOT_VECTORSTORE_DIMENSIONS - Embedding dimensions (default: 768)
 *   KUBEMOOT_EMBEDDING_ENDPOINT   - Embedding API endpoint (Ollama)
 *   KUBEMOOT_EMBEDDING_MODEL      - Embedding model name
 *   KUBEMOOT_CHUNK_SIZE           - Chunk size in characters (default: 512)
 *   KUBEMOOT_CHUNK_OVERLAP        - Chunk overlap in characters (default: 50)
 */
@ConfigurationProperties(prefix = "kubemoot")
public record IndexerConfig(
    // Source configuration
    @DefaultValue("git") String sourceType,

    // Git source
    String gitUrl,
    @DefaultValue("main") String gitBranch,
    List<String> gitPaths,
    String gitToken,

    // S3 source
    String s3Bucket,
    @DefaultValue("") String s3Prefix,
    @DefaultValue("us-east-1") String s3Region,
    String s3Endpoint,

    // URL source
    String url,

    // MCP Registry source
    String mcpRegistryUrl,
    List<String> mcpRegistryFilter,

    // NATS KV source
    String natsKvBucket,
    String natsKvKey,

    // Document source
    List<String> documentUrls,
    @DefaultValue("http://localhost:5001") String doclingEndpoint,
    @DefaultValue("false") String documentDisableOcr,

    // Vector store configuration
    @DefaultValue("pgvector") String vectorstoreType,
    String vectorstoreEndpoint,
    @DefaultValue("default") String vectorstoreCollection,
    @DefaultValue("768") int vectorstoreDimensions,

    // Embedding configuration
    @DefaultValue("http://localhost:11434") String embeddingEndpoint,
    @DefaultValue("nomic-embed-text") String embeddingModel,

    // Chunking configuration
    @DefaultValue("512") int chunkSize,
    @DefaultValue("50") int chunkOverlap,

    // File filtering
    List<String> includePatterns,
    List<String> excludePatterns,
    @DefaultValue("100000") int maxFileSize,

    // Working directory
    @DefaultValue("/data") String workDir,

    // Checksum-based change detection
    // KUBEMOOT_LAST_CHECKSUM - Previous checksum to compare against (from controller)
    String lastChecksum,

    // KUBEMOOT_FORCE_REINDEX - "true" to skip checksum comparison and always reindex
    @DefaultValue("false") String forceReindex
) {
    // Default patterns if not specified
    // Include both root-level (*.ext) and recursive (**/*.ext) patterns
    // because Java glob **/*.ext requires a path separator and won't match
    // files directly in the base directory (e.g., LLMS-FULL.md)
    public List<String> includePatterns() {
        if (includePatterns == null || includePatterns.isEmpty()) {
            return List.of(
                "*.md", "*.yaml", "*.yml", "*.json",
                "*.py", "*.go", "*.java", "*.js", "*.ts",
                "*.tf", "*.adoc", "*.asciidoc", "*.rst", "*.txt",
                "**/*.md", "**/*.yaml", "**/*.yml", "**/*.json",
                "**/*.py", "**/*.go", "**/*.java", "**/*.js", "**/*.ts",
                "**/*.tf", "**/*.adoc", "**/*.asciidoc", "**/*.rst", "**/*.txt"
            );
        }
        return includePatterns;
    }

    public List<String> excludePatterns() {
        if (excludePatterns == null || excludePatterns.isEmpty()) {
            return List.of(
                ".git/**", "node_modules/**", "__pycache__/**", ".venv/**",
                "venv/**", "build/**", "dist/**", ".gradle/**", "target/**"
            );
        }
        return excludePatterns;
    }
}
