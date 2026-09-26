package ai.kubemoot.indexer.service;

import ai.kubemoot.indexer.config.IndexerConfig;
import ai.kubemoot.indexer.source.SourceLoader;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.document.Document;
import org.springframework.ai.transformer.splitter.TokenTextSplitter;
import org.springframework.ai.vectorstore.VectorStore;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Service;

import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;

/**
 * Main indexer service that orchestrates document loading, chunking, and storage.
 *
 * <p>Supports checksum-based change detection to skip re-indexing when source
 * data hasn't changed. The indexer will:
 * <ol>
 *   <li>Calculate a checksum from source metadata (fast, no full pull)</li>
 *   <li>Compare with previous checksum from KUBEMOOT_LAST_CHECKSUM env var</li>
 *   <li>Skip indexing if unchanged (unless KUBEMOOT_FORCE_REINDEX=true)</li>
 *   <li>Report status via Job annotations for the controller to read</li>
 * </ol>
 */
@Service
public class IndexerService {

    private static final Logger logger = LoggerFactory.getLogger(IndexerService.class);

    private final IndexerConfig config;
    private final List<SourceLoader> sourceLoaders;
    private final VectorStore vectorStore;
    private final ChecksumService checksumService;
    private final KubernetesService kubernetesService;
    private final JdbcClient jdbcClient;

    public IndexerService(IndexerConfig config,
                          List<SourceLoader> sourceLoaders,
                          VectorStore vectorStore,
                          ChecksumService checksumService,
                          KubernetesService kubernetesService,
                          JdbcClient jdbcClient) {
        this.config = config;
        this.sourceLoaders = sourceLoaders;
        this.vectorStore = vectorStore;
        this.checksumService = checksumService;
        this.kubernetesService = kubernetesService;
        this.jdbcClient = jdbcClient;
    }

    /**
     * Result of the indexing operation.
     */
    public record IndexResult(
        boolean indexed,
        int documentCount,
        int chunkCount,
        String checksum,
        String message
    ) {
        public static IndexResult unchanged() {
            return new IndexResult(false, 0, 0, null, "Source unchanged, skipped indexing");
        }

        public static IndexResult indexed(int documentCount, int chunkCount, String checksum) {
            return new IndexResult(true, documentCount, chunkCount, checksum,
                String.format("Indexed %d documents (%d chunks)", documentCount, chunkCount));
        }

        public static IndexResult empty() {
            return new IndexResult(true, 0, 0, null, "No documents to index");
        }
    }

    /**
     * Run the indexing process with checksum-based change detection.
     *
     * @return IndexResult containing status, counts, and checksum
     */
    public IndexResult index() {
        // Step 0: Check if source has changed (using metadata checksum)
        logger.info("Step 0: Checking source checksum...");
        String currentChecksum = checksumService.calculateChecksum();

        if (!checksumService.hasSourceChanged()) {
            logger.info("Source unchanged, skipping indexing");
            kubernetesService.recordUnchanged();
            return IndexResult.unchanged();
        }

        // Step 1: Find the appropriate source loader
        logger.info("Step 1: Loading documents from source...");
        SourceLoader loader = sourceLoaders.stream()
            .filter(l -> l.supports(config.sourceType()))
            .findFirst()
            .orElseThrow(() -> new IllegalArgumentException(
                "No source loader found for type: " + config.sourceType()));

        List<Document> documents = loader.load();
        logger.info("Loaded {} documents", documents.size());

        if (documents.isEmpty()) {
            logger.warn("No documents to index");
            kubernetesService.recordIndexed(currentChecksum, 0, 0);
            return IndexResult.empty();
        }

        // Step 2: Chunk documents (skip for MCP registry - already small)
        List<Document> chunks;
        if ("mcp-registry".equalsIgnoreCase(config.sourceType())) {
            logger.info("Step 2: Skipping chunking for MCP registry (tools are already small)");
            chunks = documents;
        } else {
            logger.info("Step 2: Chunking documents...");
            TokenTextSplitter splitter = new TokenTextSplitter(
                config.chunkSize(),
                config.chunkOverlap(),
                5,      // minChunkSizeChars
                10000,  // maxNumChunks
                true,   // keepSeparator
                List.of('.', '?', '!', ';', ':', '\n')
            );
            chunks = splitter.apply(documents);
            logger.info("Created {} chunks", chunks.size());
        }

        // Step 3: Store in vector database (embedding happens automatically)
        logger.info("Step 3: Storing vectors (embedding + storage)...");

        // Add collection name to metadata for filtering
        for (Document chunk : chunks) {
            chunk.getMetadata().put("collection", config.vectorstoreCollection());
        }

        // For nats-kv sources, truncate old embeddings before re-indexing.
        // Resumes are always a full replacement, not incremental additions.
        if ("true".equalsIgnoreCase(System.getenv("KUBEMOOT_TRUNCATE_BEFORE_INDEX"))) {
            String collection = config.vectorstoreCollection();
            String tableName = "data_" + collection;
            try {
                long deleted = jdbcClient.sql("DELETE FROM " + tableName)
                        .update();
                logger.info("Truncated {} existing rows from collection '{}' before re-indexing", deleted, collection);
            } catch (Exception e) {
                logger.info("Table '{}' does not exist yet — skipping truncation (first index run)", tableName);
            }
        }

        vectorStore.add(chunks);
        logger.info("Stored {} vectors in collection '{}'", chunks.size(), config.vectorstoreCollection());

        // Step 4: Extract topics for auto-discovery
        logger.info("Step 4: Extracting topics for auto-discovery...");
        List<String> topics = extractTopics(documents);
        logger.info("Extracted {} topics: {}", topics.size(), topics);

        // Record success with checksum and topics
        kubernetesService.recordIndexed(currentChecksum, documents.size(), chunks.size());
        kubernetesService.recordTopics(topics);

        return IndexResult.indexed(documents.size(), chunks.size(), currentChecksum);
    }

    /**
     * Extract top keywords from the document corpus using document frequency.
     * These topics are stored on the RAGSource status for auto-discovery matching.
     */
    List<String> extractTopics(List<Document> documents) {
        Set<String> stopWords = Set.of(
            "the", "and", "for", "that", "this", "with", "are", "from", "has", "have",
            "was", "were", "will", "can", "not", "but", "all", "any", "each", "more",
            "other", "some", "such", "than", "too", "very", "just", "about", "also",
            "been", "being", "does", "done", "into", "its", "may", "only", "our",
            "out", "over", "own", "same", "should", "they", "use", "used", "using",
            "when", "where", "which", "who", "how", "what", "you", "your", "one",
            "two", "new", "see", "set", "get", "way", "make", "like", "time", "need",
            "know", "take", "come", "could", "them", "then", "these", "those", "would",
            "there", "their", "after", "before", "between", "during", "without",
            "following", "example", "default", "note", "file", "name", "type", "value",
            "true", "false", "string", "number", "list", "must", "first"
        );

        Map<String, Integer> termFreq = new HashMap<>();
        for (Document doc : documents) {
            Set<String> docTerms = Arrays.stream(doc.getText().toLowerCase()
                    .split("[^a-z0-9]+"))
                .filter(w -> w.length() >= 3)
                .filter(w -> !stopWords.contains(w))
                .collect(Collectors.toSet());

            for (String term : docTerms) {
                termFreq.merge(term, 1, Integer::sum);
            }
        }

        return termFreq.entrySet().stream()
            .sorted(Map.Entry.<String, Integer>comparingByValue().reversed())
            .limit(30)
            .map(Map.Entry::getKey)
            .toList();
    }
}
