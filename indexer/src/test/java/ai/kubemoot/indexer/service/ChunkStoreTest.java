package ai.kubemoot.indexer.service;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;
import org.springframework.ai.vectorstore.VectorStore;
import org.springframework.transaction.PlatformTransactionManager;
import org.springframework.transaction.TransactionDefinition;
import org.springframework.transaction.support.SimpleTransactionStatus;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.anySet;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/**
 * Replace semantics against an in-memory table with the same upsert-on-id and
 * delete-not-in-set behaviour the pgvector table has.
 */
class ChunkStoreTest {

    private final Map<String, String> table = new LinkedHashMap<>();
    private final List<String> events = new ArrayList<>();
    private PlatformTransactionManager txManager;
    private VectorStore vectorStore;
    private ChunkStore store;

    @BeforeEach
    void setUp() {
        txManager = mock(PlatformTransactionManager.class);
        when(txManager.getTransaction(org.mockito.ArgumentMatchers.any(TransactionDefinition.class)))
            .thenReturn(new SimpleTransactionStatus());
        vectorStore = mock(VectorStore.class);
        doAnswer(inv -> {
            List<Document> docs = inv.getArgument(0);
            docs.forEach(d -> table.put(d.getId(), d.getText()));
            events.add("add");
            return null;
        }).when(vectorStore).add(anyList());
        StaleChunkPruner pruner = keep -> {
            int before = table.size();
            table.keySet().removeIf(id -> !keep.contains(id));
            events.add("prune");
            return before - table.size();
        };
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.vectorstoreCollection()).thenReturn("kubectl_reference");
        store = new ChunkStore(vectorStore, pruner, txManager, config);
    }

    private static List<Document> docs(String... texts) {
        return java.util.Arrays.stream(texts).map(t -> new Document(t, Map.of())).toList();
    }

    @Test
    void forcedReindexTwiceKeepsOneCopyOfEachChunk() {
        store.replace(docs("a", "b", "c"));
        store.replace(docs("a", "b", "c"));
        store.replace(docs("a", "b", "c"));

        assertEquals(3, table.size());
    }

    @Test
    void changedFileReplacesItsOldChunk() {
        store.replace(docs("a", "b-old", "c"));
        store.replace(docs("a", "b-new", "c"));

        assertEquals(3, table.size());
        assertEquals(Set.of("a", "b-new", "c"), Set.copyOf(table.values()));
    }

    @Test
    void removedFileDisappears() {
        store.replace(docs("a", "b", "c"));
        store.replace(docs("a", "c"));

        assertEquals(Set.of("a", "c"), Set.copyOf(table.values()));
    }

    @Test
    void unchangedSourceLeavesRowIdentical() {
        store.replace(docs("a", "b"));
        Map<String, String> first = new LinkedHashMap<>(table);
        store.replace(docs("a", "b"));

        assertEquals(first, table);
    }

    @Test
    void duplicateTextWithinOneRunIsKeptAsSeparateRowsAcrossRuns() {
        store.replace(docs("x", "x"));
        store.replace(docs("x", "x"));

        assertEquals(2, table.size());
    }

    @Test
    void writesBeforePrunesSoTheCollectionIsNeverEmpty() {
        store.replace(docs("a"));

        assertEquals(List.of("add", "prune"), events);
    }

    @Test
    void failedWriteRollsBackAndNeverPrunes() {
        store.replace(docs("a", "b"));
        doThrow(new IllegalStateException("embedding down")).when(vectorStore).add(anyList());
        StaleChunkPruner pruner = mock(StaleChunkPruner.class);
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.vectorstoreCollection()).thenReturn("kubectl_reference");
        ChunkStore failing = new ChunkStore(vectorStore, pruner, txManager, config);

        assertThrows(IllegalStateException.class, () -> failing.replace(docs("z")));

        verify(pruner, never()).pruneExcept(anySet());
        verify(txManager).rollback(org.mockito.ArgumentMatchers.any());
        assertEquals(Set.of("a", "b"), Set.copyOf(table.values()));
    }
}
