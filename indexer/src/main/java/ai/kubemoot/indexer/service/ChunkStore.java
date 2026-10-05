package ai.kubemoot.indexer.service;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.document.Document;
import org.springframework.ai.vectorstore.VectorStore;
import org.springframework.stereotype.Component;
import org.springframework.transaction.PlatformTransactionManager;
import org.springframework.transaction.support.TransactionTemplate;

import java.util.List;
import java.util.Set;
import java.util.stream.Collectors;

/**
 * Writes the chunks of a collection so the collection afterwards holds exactly those chunks.
 *
 * <p>Chunks are upserted under deterministic ids, then rows the run did not produce
 * (old versions of changed files, files that no longer exist) are removed. Both steps
 * run in one transaction, so a query sees the old rows or the new rows, never an empty
 * or doubled collection, and a failed run leaves the previous rows in place.
 */
@Component
public class ChunkStore {

    private static final Logger logger = LoggerFactory.getLogger(ChunkStore.class);

    private final VectorStore vectorStore;
    private final StaleChunkPruner pruner;
    private final TransactionTemplate transaction;
    private final String collection;

    public ChunkStore(VectorStore vectorStore,
                      StaleChunkPruner pruner,
                      PlatformTransactionManager transactionManager,
                      IndexerConfig config) {
        this.vectorStore = vectorStore;
        this.pruner = pruner;
        this.transaction = new TransactionTemplate(transactionManager);
        this.collection = config.vectorstoreCollection();
    }

    /**
     * Replaces the collection's contents with the given chunks.
     *
     * @return the chunks as stored, with their deterministic ids
     */
    public List<Document> replace(List<Document> chunks) {
        List<Document> identified = ChunkIds.assign(collection, chunks);
        Set<String> keep = identified.stream().map(Document::getId).collect(Collectors.toSet());
        transaction.executeWithoutResult(status -> {
            vectorStore.add(identified);
            int removed = pruner.pruneExcept(keep);
            logger.info("Stored {} chunks in collection '{}', removed {} stale rows",
                identified.size(), collection, removed);
        });
        return identified;
    }
}
