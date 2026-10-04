package ai.kubemoot.indexer.source;

import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Tests for NatsKVSourceLoader resume text building and document dispatch.
 * NATS connectivity is not tested here (requires a running server).
 */
class NatsKVSourceLoaderTest {

    private static final String BUCKET = "test-bucket";
    private static final String KEY = "test-crew";

    private final NatsKVSourceLoader loader = new NatsKVSourceLoader();

    // ---------------------------------------------------------------------------
    // Existing agent-resume text tests (unchanged behaviour)
    // ---------------------------------------------------------------------------

    @Test
    void buildResumeText_fullResume() {
        Map<String, Object> resume = Map.of(
                "name", "k8s-agent",
                "role", "specialist",
                "description", "Kubernetes expert",
                "triageSummary", "Handles pod queries",
                "keywords", List.of("kubernetes", "pods"),
                "tools", List.of("kubectl_get", "kubectl_describe"),
                "channels", List.of("infrastructure"),
                "prompt", "WHEN asked about pods THEN list them with status"
        );

        String text = loader.buildResumeText(resume);

        assertTrue(text.contains("Agent: k8s-agent"), "should contain agent name");
        assertTrue(text.contains("Role: specialist"), "should contain role");
        assertTrue(text.contains("Description: Kubernetes expert"), "should contain description");
        assertTrue(text.contains("Summary: Handles pod queries"), "should contain summary");
        assertTrue(text.contains("Keywords: kubernetes, pods"), "should contain keywords");
        assertTrue(text.contains("Tools: kubectl_get, kubectl_describe"), "should contain tools");
        assertTrue(text.contains("Channels: infrastructure"), "should contain channels");
        assertTrue(text.contains("System prompt:"), "should contain the system prompt label");
        assertTrue(text.contains("WHEN asked about pods THEN list them with status"),
                "should embed the agent's full system prompt for richer ranking");
    }

    @Test
    void buildResumeText_minimalResume() {
        Map<String, Object> resume = Map.of(
                "name", "simple-agent",
                "role", "researcher"
        );

        String text = loader.buildResumeText(resume);

        assertTrue(text.contains("Agent: simple-agent"));
        assertTrue(text.contains("Role: researcher"));
        assertFalse(text.contains("Description:"), "empty description should not appear");
        assertFalse(text.contains("Keywords:"), "empty keywords should not appear");
        assertFalse(text.contains("Tools:"), "empty tools should not appear");
        assertFalse(text.contains("System prompt:"), "absent prompt should not appear");
    }

    @Test
    void buildResumeText_missingNameUsesDefault() {
        Map<String, Object> resume = Map.of("role", "specialist");

        String text = loader.buildResumeText(resume);

        assertTrue(text.contains("Agent: unknown"));
    }

    @Test
    void buildResumeText_emptyKeywordsNotIncluded() {
        Map<String, Object> resume = Map.of(
                "name", "test",
                "role", "specialist",
                "keywords", List.of()
        );

        String text = loader.buildResumeText(resume);

        assertFalse(text.contains("Keywords:"), "empty keyword list should not produce a line");
    }

    @Test
    void supports_natsKv() {
        assertTrue(loader.supports("nats-kv"));
        assertTrue(loader.supports("NATS-KV"));
        assertFalse(loader.supports("git"));
        assertFalse(loader.supports("url"));
    }

    // ---------------------------------------------------------------------------
    // Skill text builder tests
    // ---------------------------------------------------------------------------

    @Test
    void buildSkillText_withDescription() {
        Map<String, Object> entry = Map.of(
                "name", "summarise",
                "description", "Summarises a URL into bullet points",
                "kind", "skill",
                "order", 10
        );

        String text = loader.buildSkillText(entry);

        assertEquals("Skill: summarise\nDescription: Summarises a URL into bullet points", text);
    }

    @Test
    void buildSkillText_withoutDescription() {
        Map<String, Object> entry = Map.of(
                "name", "search",
                "kind", "skill"
        );

        String text = loader.buildSkillText(entry);

        assertEquals("Skill: search", text);
        assertFalse(text.contains("Description:"), "absent description must not appear");
    }

    @Test
    void buildSkillText_emptyDescriptionOmitted() {
        Map<String, Object> entry = new HashMap<>();
        entry.put("name", "translate");
        entry.put("description", "   ");
        entry.put("kind", "skill");

        String text = loader.buildSkillText(entry);

        assertFalse(text.contains("Description:"), "blank description must not appear");
    }

    // ---------------------------------------------------------------------------
    // buildDocument dispatch tests
    // ---------------------------------------------------------------------------

    @Test
    void buildDocument_agentEntry_producesAgentDocument() {
        Map<String, Object> entry = Map.of(
                "name", "k8s-agent",
                "role", "specialist",
                "description", "Kubernetes expert"
        );

        Document doc = loader.buildDocument(entry, BUCKET, KEY);

        assertNotNull(doc, "agent entry must produce a document");
        assertEquals("agent", doc.getMetadata().get("kind"), "kind must be agent");
        assertEquals("k8s-agent", doc.getMetadata().get("agent_name"), "agent_name must be set");
        assertNull(doc.getMetadata().get("skill_name"), "skill_name must not be set on agent doc");
        assertTrue(doc.getText().contains("Agent: k8s-agent"), "text must contain agent prefix");
        assertEquals(BUCKET, doc.getMetadata().get("bucket"));
        assertEquals(KEY, doc.getMetadata().get("key"));
    }

    @Test
    void buildDocument_skillEntry_producesSkillDocument() {
        Map<String, Object> entry = Map.of(
                "name", "summarise",
                "description", "Summarises a URL",
                "kind", "skill",
                "order", 10
        );

        Document doc = loader.buildDocument(entry, BUCKET, KEY);

        assertNotNull(doc, "skill entry must produce a document");
        assertEquals("skill", doc.getMetadata().get("kind"), "kind must be skill");
        assertEquals("summarise", doc.getMetadata().get("skill_name"), "skill_name must be set");
        assertNull(doc.getMetadata().get("agent_name"), "agent_name must not be set on skill doc");
        assertTrue(doc.getText().startsWith("Skill: summarise"), "text must start with Skill prefix");
        assertEquals(BUCKET, doc.getMetadata().get("bucket"));
        assertEquals(KEY, doc.getMetadata().get("key"));
    }

    @Test
    void buildDocument_nullEntry_returnsNull() {
        Document doc = loader.buildDocument(null, BUCKET, KEY);

        assertNull(doc, "null entry must be skipped");
    }

    @Test
    void buildDocument_skillEntryMissingName_returnsNull() {
        Map<String, Object> entry = Map.of(
                "description", "No name provided",
                "kind", "skill"
        );

        Document doc = loader.buildDocument(entry, BUCKET, KEY);

        assertNull(doc, "skill entry without name must be skipped");
    }

    @Test
    void buildDocument_agentEntryMissingName_returnsNull() {
        Map<String, Object> entry = Map.of(
                "role", "specialist",
                "description", "No name provided"
        );

        Document doc = loader.buildDocument(entry, BUCKET, KEY);

        assertNull(doc, "agent entry without name must be skipped");
    }

    // ---------------------------------------------------------------------------
    // Combined array tests (the key integration scenario)
    // ---------------------------------------------------------------------------

    @Test
    void pureAgentArray_producesOnlyAgentDocuments() {
        List<Map<String, Object>> entries = List.of(
                Map.of("name", "agent-a", "role", "specialist"),
                Map.of("name", "agent-b", "role", "researcher")
        );

        List<Document> docs = entries.stream()
                .map(e -> loader.buildDocument(e, BUCKET, KEY))
                .filter(d -> d != null)
                .toList();

        assertEquals(2, docs.size(), "all agent entries must produce documents");
        assertTrue(docs.stream().allMatch(d -> "agent".equals(d.getMetadata().get("kind"))),
                "all documents must have kind=agent");
        assertTrue(docs.stream().noneMatch(d -> d.getMetadata().containsKey("skill_name")),
                "no document must have skill_name");
        assertTrue(docs.stream().allMatch(d -> d.getMetadata().containsKey("agent_name")),
                "every document must have agent_name");
    }

    @Test
    void combinedArray_agentsAndSkills_producesCorrectDocumentTypes() {
        List<Map<String, Object>> entries = List.of(
                Map.of("name", "k8s-agent", "role", "specialist", "description", "K8s expert"),
                Map.of("name", "git-agent", "role", "specialist"),
                Map.of("name", "summarise", "description", "Summarises URLs", "kind", "skill", "order", 10),
                Map.of("name", "search", "kind", "skill", "order", 20)
        );

        List<Document> docs = entries.stream()
                .map(e -> loader.buildDocument(e, BUCKET, KEY))
                .filter(d -> d != null)
                .toList();

        assertEquals(4, docs.size(), "all valid entries must produce documents");

        List<Document> agentDocs = docs.stream()
                .filter(d -> "agent".equals(d.getMetadata().get("kind")))
                .toList();
        List<Document> skillDocs = docs.stream()
                .filter(d -> "skill".equals(d.getMetadata().get("kind")))
                .toList();

        assertEquals(2, agentDocs.size(), "two agent documents expected");
        assertEquals(2, skillDocs.size(), "two skill documents expected");

        // agent docs carry agent_name, not skill_name
        assertTrue(agentDocs.stream().allMatch(d -> d.getMetadata().containsKey("agent_name")),
                "all agent docs must have agent_name");
        assertTrue(agentDocs.stream().noneMatch(d -> d.getMetadata().containsKey("skill_name")),
                "agent docs must not have skill_name");

        // skill docs carry skill_name, not agent_name
        assertTrue(skillDocs.stream().allMatch(d -> d.getMetadata().containsKey("skill_name")),
                "all skill docs must have skill_name");
        assertTrue(skillDocs.stream().noneMatch(d -> d.getMetadata().containsKey("agent_name")),
                "skill docs must not have agent_name");

        // text format checks
        Document summariseDoc = skillDocs.stream()
                .filter(d -> "summarise".equals(d.getMetadata().get("skill_name")))
                .findFirst()
                .orElseThrow();
        assertTrue(summariseDoc.getText().startsWith("Skill: summarise"),
                "skill text must start with Skill prefix");
        assertTrue(summariseDoc.getText().contains("Description: Summarises URLs"),
                "skill text must include description");
    }

    @Test
    void combinedArray_malformedEntrySkipped_otherEntriesStillIndexed() {
        // A malformed entry (null name on a skill) must be skipped; others proceed.
        Map<String, Object> malformedSkill = new HashMap<>();
        malformedSkill.put("kind", "skill");
        // intentionally no "name"

        List<Map<String, Object>> entries = List.of(
                Map.of("name", "good-agent", "role", "specialist"),
                malformedSkill,
                Map.of("name", "good-skill", "kind", "skill")
        );

        List<Document> docs = entries.stream()
                .map(e -> loader.buildDocument(e, BUCKET, KEY))
                .filter(d -> d != null)
                .toList();

        assertEquals(2, docs.size(), "malformed entry must be skipped; good entries indexed");
        assertTrue(docs.stream().anyMatch(d -> "good-agent".equals(d.getMetadata().get("agent_name"))),
                "good agent must be indexed");
        assertTrue(docs.stream().anyMatch(d -> "good-skill".equals(d.getMetadata().get("skill_name"))),
                "good skill must be indexed");
    }

    @Test
    void requireValue_returnsSetValue() {
        assertEquals("nats://nats:4222", NatsKVSourceLoader.requireValue("NATS_URL", "nats://nats:4222"));
    }

    @Test
    void requireValue_rejectsNullAndBlank() {
        IllegalArgumentException missing = assertThrows(IllegalArgumentException.class,
                () -> NatsKVSourceLoader.requireValue("KUBEMOOT_NATS_KV_KEY", null));
        assertEquals("KUBEMOOT_NATS_KV_KEY is required for nats-kv source", missing.getMessage());
        assertThrows(IllegalArgumentException.class,
                () -> NatsKVSourceLoader.requireValue("KUBEMOOT_NATS_KV_BUCKET", "  "));
    }

    @Test
    void buildResumeText_skipsEmptyListsAndNonListValues() {
        Map<String, Object> resume = new HashMap<>();
        resume.put("name", "a");
        resume.put("keywords", List.of());
        resume.put("tools", "not-a-list");
        String text = loader.buildResumeText(resume);
        assertEquals("Agent: a\nRole: specialist", text);
    }
}
