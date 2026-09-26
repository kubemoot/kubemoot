package ai.kubemoot.indexer.service;

import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Tests for IndexerService.extractTopics term extraction.
 * The collaborators are not exercised here; extractTopics is a pure function
 * over the document corpus, so the service is built with null dependencies.
 */
class IndexerServiceTest {

    private final IndexerService service =
            new IndexerService(null, null, null, null, null, null);

    private static Document doc(String text) {
        return new Document(text, Map.of());
    }

    @Test
    void extractTopics_ranksFrequentTermsHighest() {
        List<Document> documents = List.of(
                doc("kubernetes kubernetes kubernetes operator"),
                doc("kubernetes operator scheduler"),
                doc("kubernetes scheduler embedding")
        );

        List<String> topics = service.extractTopics(documents);

        assertTrue(topics.contains("kubernetes"), "the most frequent term should appear");
        assertTrue(topics.indexOf("kubernetes") < topics.indexOf("embedding"),
                "a term in more documents should rank ahead of a rarer one");
    }

    @Test
    void extractTopics_excludesStopWordsAndShortTokens() {
        List<Document> documents = List.of(
                doc("the and for that pod ab xy scheduler")
        );

        List<String> topics = service.extractTopics(documents);

        assertFalse(topics.contains("the"), "stop words must be excluded");
        assertFalse(topics.contains("and"), "stop words must be excluded");
        assertFalse(topics.contains("ab"), "tokens shorter than 3 chars must be excluded");
        assertTrue(topics.contains("pod"), "a 3-char non-stop-word should be kept");
        assertTrue(topics.contains("scheduler"), "a regular term should be kept");
    }

    @Test
    void extractTopics_lowercasesAndSplitsOnNonAlphanumeric() {
        List<Document> documents = List.of(
                doc("Kubernetes,Operator;Scheduler")
        );

        List<String> topics = service.extractTopics(documents);

        assertTrue(topics.contains("kubernetes"), "terms are lowercased");
        assertTrue(topics.contains("operator"), "punctuation acts as a delimiter");
        assertTrue(topics.contains("scheduler"), "punctuation acts as a delimiter");
    }

    @Test
    void extractTopics_emptyCorpusReturnsEmptyList() {
        List<String> topics = service.extractTopics(List.of());

        assertTrue(topics.isEmpty(), "no documents yields no topics");
    }

    @Test
    void extractTopics_cappedAtThirtyTerms() {
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < 50; i++) {
            sb.append("term").append(i).append(' ');
        }
        List<Document> documents = List.of(doc(sb.toString()));

        List<String> topics = service.extractTopics(documents);

        assertTrue(topics.size() <= 30, "topic count is capped at 30");
    }
}
