package ai.kubemoot.indexer.service;

import org.springframework.ai.document.Document;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;
import java.util.UUID;

/**
 * Gives every chunk a deterministic id so that writing the same chunk twice
 * targets the same row (the vector store upserts on id).
 *
 * <p>The id is a name-based UUID over the collection, the SHA-256 of the chunk text,
 * and the occurrence number of that text within the batch. Identical text in two
 * files therefore stays two rows, while re-running over unchanged content yields the
 * same ids in the same order.
 */
final class ChunkIds {

    private ChunkIds() {
    }

    /**
     * Returns copies of the chunks carrying deterministic ids.
     *
     * @param collection the vector store collection the chunks belong to
     * @param chunks     the chunks in source order
     * @return chunks with the same text and metadata and a stable id
     */
    static List<Document> assign(String collection, List<Document> chunks) {
        Map<String, Integer> occurrences = new HashMap<>();
        List<Document> result = new ArrayList<>(chunks.size());
        for (Document chunk : chunks) {
            String text = chunk.getText() == null ? "" : chunk.getText();
            String digest = sha256(text);
            int occurrence = occurrences.merge(digest, 1, Integer::sum) - 1;
            String id = idFor(collection, digest, occurrence);
            result.add(Document.builder()
                .id(id)
                .text(text)
                .metadata(chunk.getMetadata())
                .build());
        }
        return result;
    }

    static String idFor(String collection, String contentDigest, int occurrence) {
        String name = collection + '\0' + contentDigest + '\0' + occurrence;
        return UUID.nameUUIDFromBytes(name.getBytes(StandardCharsets.UTF_8)).toString();
    }

    private static String sha256(String text) {
        try {
            byte[] hash = MessageDigest.getInstance("SHA-256").digest(text.getBytes(StandardCharsets.UTF_8));
            return HexFormat.of().formatHex(hash);
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 is unavailable", e);
        }
    }
}
