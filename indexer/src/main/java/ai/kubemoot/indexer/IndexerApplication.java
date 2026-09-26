package ai.kubemoot.indexer;

import ai.kubemoot.indexer.config.IndexerConfig;
import ai.kubemoot.indexer.service.IndexerService;
import ai.kubemoot.indexer.service.IndexerService.IndexResult;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.boot.CommandLineRunner;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;

/**
 * Kubemoot Indexer - Spring AI based document and MCP catalog indexer.
 *
 * Runs as a Kubernetes Job to index documents from various sources
 * (git, s3, url, mcp-registry) into a vector store for RAG queries.
 *
 * <p>Supports intelligent re-indexing with checksum-based change detection:
 * <ul>
 *   <li>KUBEMOOT_LAST_CHECKSUM - Previous checksum for comparison</li>
 *   <li>KUBEMOOT_FORCE_REINDEX - Set to "true" to bypass checksum check</li>
 * </ul>
 *
 * <p>Reports status back to the controller via Job annotations:
 * <ul>
 *   <li>kubemoot.ai/status - "indexed" or "unchanged"</li>
 *   <li>kubemoot.ai/checksum - New checksum (if indexed)</li>
 *   <li>kubemoot.ai/document-count - Number of documents processed</li>
 *   <li>kubemoot.ai/chunk-count - Number of chunks created</li>
 * </ul>
 */
@SpringBootApplication
@EnableConfigurationProperties(IndexerConfig.class)
public class IndexerApplication {

    private static final Logger logger = LoggerFactory.getLogger(IndexerApplication.class);

    public static void main(String[] args) {
        System.exit(SpringApplication.exit(SpringApplication.run(IndexerApplication.class, args)));
    }

    @Bean
    CommandLineRunner run(IndexerService indexerService, IndexerConfig config) {
        return args -> {
            long startTime = System.currentTimeMillis();

            logger.info("=".repeat(50));
            logger.info("Kubemoot Indexer Starting");
            logger.info("Source: {}", config.sourceType());
            logger.info("VectorStore: {} -> {}", config.vectorstoreType(), config.vectorstoreCollection());
            logger.info("Embedding: {} @ {}", config.embeddingModel(), config.embeddingEndpoint());
            if (config.lastChecksum() != null && !config.lastChecksum().isBlank()) {
                logger.info("Last Checksum: {}", config.lastChecksum());
            }
            logger.info("Force Reindex: {}", config.forceReindex());
            logger.info("=".repeat(50));

            try {
                IndexResult result = indexerService.index();

                long elapsed = System.currentTimeMillis() - startTime;
                logger.info("=".repeat(50));
                if (result.indexed()) {
                    logger.info("Indexing complete in {}ms", elapsed);
                    logger.info("Documents: {}", result.documentCount());
                    logger.info("Chunks: {}", result.chunkCount());
                    if (result.checksum() != null) {
                        logger.info("Checksum: {}", result.checksum());
                    }
                } else {
                    logger.info("Indexing skipped in {}ms", elapsed);
                    logger.info("Reason: {}", result.message());
                }
                logger.info("=".repeat(50));

            } catch (Exception e) {
                logger.error("Indexing failed", e);
                throw e;
            }
        };
    }
}
