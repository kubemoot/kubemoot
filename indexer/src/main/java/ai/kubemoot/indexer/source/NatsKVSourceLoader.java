package ai.kubemoot.indexer.source;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.Nats;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.document.Document;
import org.springframework.stereotype.Component;

import java.io.IOException;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.StringJoiner;

/**
 * Loads documents from a NATS KV bucket.
 *
 * <p>Reads a JSON array of resume objects from the configured bucket/key and
 * converts each entry into a Document for embedding. Each entry becomes one
 * document (no chunking needed - they are small).
 *
 * <p>The array is a combined pool written by the operator's resume_sync.go.
 * Each element is either an agent resume (has a "role" field, no "kind") or a
 * skill resume with discriminator {@code "kind":"skill"}. A pure-agent array
 * (no skills defined) is handled identically to the pre-Skills behaviour.
 *
 * <p>Environment variables:
 * <ul>
 *   <li>{@code NATS_URL} - NATS server URL</li>
 *   <li>{@code KUBEMOOT_NATS_KV_BUCKET} - KV bucket name</li>
 *   <li>{@code KUBEMOOT_NATS_KV_KEY} - Key within the bucket</li>
 * </ul>
 */
@Component
public class NatsKVSourceLoader implements SourceLoader {

    private static final Logger logger = LoggerFactory.getLogger(NatsKVSourceLoader.class);

    private final ObjectMapper objectMapper = new ObjectMapper();

    private static final String SOURCE_NATS_KV = "nats-kv";

    public NatsKVSourceLoader() {
        // No initialization required; the ObjectMapper is created at field
        // declaration and Spring instantiates this component via this constructor.
    }

    @Override
    public boolean supports(String sourceType) {
        return SOURCE_NATS_KV.equalsIgnoreCase(sourceType);
    }

    @Override
    public List<Document> load() {
        String natsUrl = System.getenv("NATS_URL");
        String bucket = System.getenv("KUBEMOOT_NATS_KV_BUCKET");
        String key = System.getenv("KUBEMOOT_NATS_KV_KEY");

        if (natsUrl == null || natsUrl.isBlank()) {
            throw new IllegalArgumentException("NATS_URL is required for nats-kv source");
        }
        if (bucket == null || bucket.isBlank()) {
            throw new IllegalArgumentException("KUBEMOOT_NATS_KV_BUCKET is required for nats-kv source");
        }
        if (key == null || key.isBlank()) {
            throw new IllegalArgumentException("KUBEMOOT_NATS_KV_KEY is required for nats-kv source");
        }

        logger.info("Reading from NATS KV: bucket={}, key={}, url={}", bucket, key, natsUrl);

        try (Connection nc = Nats.connect(natsUrl)) {
            KeyValue kv = nc.keyValue(bucket);
            byte[] value = kv.get(key).getValue();

            List<Map<String, Object>> entries = objectMapper.readValue(value,
                    new TypeReference<>() {});

            logger.info("Parsed {} resume entries from NATS KV", entries.size());

            List<Document> documents = new ArrayList<>();
            for (Map<String, Object> entry : entries) {
                Document doc = buildDocument(entry, bucket, key);
                if (doc != null) {
                    documents.add(doc);
                }
            }

            logger.info("Created {} documents from NATS KV resume entries", documents.size());
            return documents;

        } catch (IOException e) {
            throw new java.io.UncheckedIOException("Failed to read from NATS KV bucket=" + bucket + " key=" + key, e);
        } catch (Exception e) {
            throw new IllegalStateException("Failed to read from NATS KV bucket=" + bucket + " key=" + key, e);
        }
    }

    /**
     * Dispatch a single resume-pool entry to the correct document builder.
     * Returns null and logs a warning when the entry cannot be parsed (so the
     * caller skips it without crashing the indexing run).
     */
    Document buildDocument(Map<String, Object> entry, String bucket, String key) {
        if (entry == null) {
            logger.warn("Skipping null resume entry in bucket={} key={}", bucket, key);
            return null;
        }
        Object kindObj = entry.get("kind");
        if ("skill".equals(kindObj)) {
            return buildSkillDocument(entry, bucket, key);
        }
        // Treat anything without kind="skill" as an agent resume (the pre-Skills
        // contract: agent elements have a "role" field and no "kind").
        return buildAgentDocument(entry, bucket, key);
    }

    /**
     * Build a Document from an agent resume entry.
     * Metadata: kind=agent, agent_name=<name>, source, bucket, key.
     */
    private Document buildAgentDocument(Map<String, Object> entry, String bucket, String key) {
        Object name = entry.get("name");
        if (name == null) {
            logger.warn("Skipping agent resume with missing name in bucket={} key={}", bucket, key);
            return null;
        }
        String text = buildResumeText(entry);
        Map<String, Object> metadata = new HashMap<>();
        metadata.put("source", SOURCE_NATS_KV);
        metadata.put("bucket", bucket);
        metadata.put("key", key);
        metadata.put("kind", "agent");
        metadata.put("agent_name", name);
        return new Document(text, metadata);
    }

    /**
     * Build a Document from a skill resume entry.
     * Metadata: kind=skill, skill_name=<name>, source, bucket, key.
     * agent_name is intentionally NOT set on skill documents.
     *
     * <p>Text format mirrors the operator's BuildSkillResumeText in
     * kubemoot/operator/internal/controller/resume_hash.go. Keep the two in sync.
     */
    private Document buildSkillDocument(Map<String, Object> entry, String bucket, String key) {
        Object name = entry.get("name");
        if (name == null) {
            logger.warn("Skipping skill resume with missing name in bucket={} key={}", bucket, key);
            return null;
        }
        String text = buildSkillText(entry);
        Map<String, Object> metadata = new HashMap<>();
        metadata.put("source", SOURCE_NATS_KV);
        metadata.put("bucket", bucket);
        metadata.put("key", key);
        metadata.put("kind", "skill");
        metadata.put("skill_name", name);
        return new Document(text, metadata);
    }

    /**
     * Build the embedded text for a skill resume entry.
     *
     * <p>Format mirrors the operator's BuildSkillResumeText() in
     * kubemoot/operator/internal/controller/resume_hash.go.
     * Any change to the format here must be reflected there and vice versa.
     */
    String buildSkillText(Map<String, Object> entry) {
        StringJoiner joiner = new StringJoiner("\n");
        joiner.add("Skill: " + entry.getOrDefault("name", "unknown"));
        String description = (String) entry.get("description");
        if (description != null && !description.isBlank()) {
            joiner.add("Description: " + description);
        }
        return joiner.toString();
    }

    /**
     * Build a human-readable text representation of an agent resume,
     * matching the format from the operator's BuildResumeText() in
     * kubemoot/operator/internal/controller/resume_hash.go.
     */
    @SuppressWarnings("unchecked")
    String buildResumeText(Map<String, Object> resume) {
        StringJoiner joiner = new StringJoiner("\n");

        joiner.add("Agent: " + resume.getOrDefault("name", "unknown"));
        joiner.add("Role: " + resume.getOrDefault("role", "specialist"));

        String description = (String) resume.get("description");
        if (description != null && !description.isBlank()) {
            joiner.add("Description: " + description);
        }

        String summary = (String) resume.get("triageSummary");
        if (summary != null && !summary.isBlank()) {
            joiner.add("Summary: " + summary);
        }

        Object keywords = resume.get("keywords");
        if (keywords instanceof List<?> list && !list.isEmpty()) {
            joiner.add("Keywords: " + String.join(", ", (List<String>) list));
        }

        Object tools = resume.get("tools");
        if (tools instanceof List<?> list && !list.isEmpty()) {
            joiner.add("Tools: " + String.join(", ", (List<String>) list));
        }

        Object channels = resume.get("channels");
        if (channels instanceof List<?> list && !list.isEmpty()) {
            joiner.add("Channels: " + String.join(", ", (List<String>) list));
        }

        // The agent's full system prompt - the richest statement of what it does -
        // folded in LAST so it adds semantic signal for top-K ranking without
        // displacing the structured fields above. Embed-only: the resume query
        // returns agent names, so this never reaches a triage LLM prompt.
        String prompt = (String) resume.get("prompt");
        if (prompt != null && !prompt.isBlank()) {
            joiner.add("System prompt:\n" + prompt);
        }

        return joiner.toString();
    }
}
