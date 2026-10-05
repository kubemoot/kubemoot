package ai.kubemoot.indexer.service;

import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;

import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ChunkIdsTest {

    private static Document doc(String text) {
        return new Document(text, Map.of("file_path", "a.md"));
    }

    private static List<String> ids(String collection, List<Document> chunks) {
        return ChunkIds.assign(collection, chunks).stream().map(Document::getId).toList();
    }

    @Test
    void sameInputYieldsSameIds() {
        List<Document> chunks = List.of(doc("one"), doc("two"));

        assertEquals(ids("kubectl_reference", chunks), ids("kubectl_reference", chunks));
    }

    @Test
    void idsAreValidUuids() {
        String id = ids("c", List.of(doc("one"))).get(0);

        assertEquals(id, UUID.fromString(id).toString());
    }

    @Test
    void differentTextOrCollectionYieldsDifferentIds() {
        assertNotEquals(ids("c", List.of(doc("one"))), ids("c", List.of(doc("two"))));
        assertNotEquals(ids("c", List.of(doc("one"))), ids("d", List.of(doc("one"))));
    }

    @Test
    void identicalTextInTheBatchStaysDistinctRows() {
        List<String> ids = ids("c", List.of(doc("license"), doc("license"), doc("license")));

        assertEquals(3, new HashSet<>(ids).size());
        assertEquals(ids, ids("c", List.of(doc("license"), doc("license"), doc("license"))));
    }

    @Test
    void keepsTextAndMetadata() {
        Document out = ChunkIds.assign("c", List.of(doc("one"))).get(0);

        assertEquals("one", out.getText());
        assertEquals("a.md", out.getMetadata().get("file_path"));
    }

    @Test
    void emptyBatchAndNullTextAreHandled() {
        assertTrue(ChunkIds.assign("c", List.of()).isEmpty());
        Document blank = Document.builder().media(new org.springframework.ai.content.Media(
            org.springframework.util.MimeTypeUtils.IMAGE_PNG, new org.springframework.core.io.ByteArrayResource(new byte[]{1}))).build();
        assertEquals(1, ChunkIds.assign("c", List.of(blank)).size());
    }
}
