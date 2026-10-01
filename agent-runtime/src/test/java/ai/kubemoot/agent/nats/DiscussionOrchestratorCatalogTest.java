package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for the pure (static) capability-catalog helpers in
 * DiscussionOrchestrator: distinguishing skill vs agent resume nodes and
 * formatting the catalog string. No NATS / orchestrator state involved.
 */
class DiscussionOrchestratorCatalogTest {

    private static final ObjectMapper M = new ObjectMapper();

    private static JsonNode json(String s) {
        try {
            return M.readTree(s);
        } catch (Exception e) {
            throw new RuntimeException(e);
        }
    }

    @Test
    void isSkillNode_distinguishesSkillsFromAgents() {
        assertTrue(DiscussionOrchestrator.isSkillNode(json("{\"kind\":\"skill\",\"name\":\"gpu-sop\"}")),
                "kind=skill is a skill");
        assertFalse(DiscussionOrchestrator.isSkillNode(json("{\"name\":\"k8s-metrics\"}")),
                "a node with no kind field is an agent");
        assertFalse(DiscussionOrchestrator.isSkillNode(json("{\"kind\":\"agent\",\"name\":\"x\"}")),
                "kind=agent is not a skill");
    }

    @Test
    void appendSkillCatalogEntry_formatsNameAndDescription() {
        var sb = new StringBuilder();
        DiscussionOrchestrator.appendSkillCatalogEntry(sb, json("{\"name\":\"gpu-sop\",\"description\":\"GPU runbook\"}"));
        assertEquals("- gpu-sop: GPU runbook\n", sb.toString());
    }

    @Test
    void appendSkillCatalogEntry_skipsEntriesWithNoName() {
        var sb = new StringBuilder();
        DiscussionOrchestrator.appendSkillCatalogEntry(sb, json("{\"description\":\"nameless\"}"));
        assertEquals("", sb.toString(), "an entry with no name is skipped");
    }

    @Test
    void appendSkillCatalogEntry_nameOnlyOmitsColon() {
        var sb = new StringBuilder();
        DiscussionOrchestrator.appendSkillCatalogEntry(sb, json("{\"name\":\"bare\"}"));
        assertEquals("- bare\n", sb.toString());
    }

    @Test
    void buildCatalogString_appendsSkillsSectionOnlyWhenPresent() {
        var agents = new StringBuilder("- a1\n- a2\n");
        assertEquals("- a1\n- a2\n",
                DiscussionOrchestrator.buildCatalogString(agents, new StringBuilder()),
                "no skills -> the agents section is returned unchanged");

        var withSkills = DiscussionOrchestrator.buildCatalogString(
                new StringBuilder("- a1\n"), new StringBuilder("- s1\n"));
        assertTrue(withSkills.startsWith("- a1\n"), "agents come first");
        assertTrue(withSkills.contains("Skills available:"), "the skills header is added");
        assertTrue(withSkills.contains("- s1\n"), "skills are appended");
    }

    // --- B0: cross-cutting always-candidate union (see Crew Evasion note) ---

    @Test
    void splitCsv_trimsAndDropsEmptyTokens() {
        assertEquals(java.util.List.of("compute"),
                DiscussionOrchestrator.splitCsv("compute"));
        assertEquals(java.util.List.of("compute", "planner"),
                DiscussionOrchestrator.splitCsv(" compute , planner "));
        assertEquals(java.util.List.of("compute", "planner"),
                DiscussionOrchestrator.splitCsv("compute,,planner,"),
                "empty tokens between/after commas are dropped");
        assertTrue(DiscussionOrchestrator.splitCsv("  ").isEmpty(),
                "a blank string yields no names");
    }

    @Test
    void unionAlwaysCandidates_appendsMissingKnownExtra() {
        var ranked = java.util.List.of("k8s-config", "obs-metrics");
        var result = DiscussionOrchestrator.unionAlwaysCandidates(
                ranked, java.util.List.of("compute"),
                java.util.List.of("k8s-config", "obs-metrics", "compute"));
        assertEquals(java.util.List.of("k8s-config", "obs-metrics", "compute"), result,
                "compute is unioned in after the domain-ranked candidates, order preserved");
    }

    @Test
    void unionAlwaysCandidates_doesNotDuplicateAlreadyRanked() {
        var ranked = java.util.List.of("compute", "k8s-config");
        var result = DiscussionOrchestrator.unionAlwaysCandidates(
                ranked, java.util.List.of("compute"),
                java.util.List.of("compute", "k8s-config"));
        assertEquals(java.util.List.of("compute", "k8s-config"), result,
                "an extra already present in the ranked set is not added twice");
    }

    @Test
    void unionAlwaysCandidates_skipsUnknownWhenValidating() {
        var ranked = java.util.List.of("k8s-config");
        var result = DiscussionOrchestrator.unionAlwaysCandidates(
                ranked, java.util.List.of("ghost"),
                java.util.List.of("k8s-config", "compute"));
        assertEquals(java.util.List.of("k8s-config"), result,
                "an always-candidate that is not a known crew agent is dropped");
    }

    @Test
    void unionAlwaysCandidates_addsExtrasWhenKnownNamesUnavailable() {
        var ranked = java.util.List.of("k8s-config");
        assertEquals(java.util.List.of("k8s-config", "compute"),
                DiscussionOrchestrator.unionAlwaysCandidates(
                        ranked, java.util.List.of("compute"), null),
                "null knownNames disables validation - extras are added as-is");
        assertEquals(java.util.List.of("k8s-config", "compute"),
                DiscussionOrchestrator.unionAlwaysCandidates(
                        ranked, java.util.List.of("compute"), java.util.List.of()),
                "empty knownNames also disables validation");
    }

    @Test
    void unionAlwaysCandidates_nullRankedTreatedAsEmpty() {
        assertEquals(java.util.List.of("compute"),
                DiscussionOrchestrator.unionAlwaysCandidates(null, java.util.List.of("compute"), null),
                "a null ranked list is treated as empty - extras still come through, no NPE");
    }

    @Test
    void unionAlwaysCandidates_noExtrasReturnsRankedUnchanged() {
        var ranked = java.util.List.of("k8s-config", "obs-metrics");
        assertSame(ranked, DiscussionOrchestrator.unionAlwaysCandidates(ranked, java.util.List.of(), null),
                "no always-candidates configured -> the ranked list is returned as-is");
        assertSame(ranked, DiscussionOrchestrator.unionAlwaysCandidates(ranked, null, null),
                "null extras -> the ranked list is returned as-is");
    }

    // --- artifactUnavailableNotice (honest-fail on an unreadable artifact) ---

    @Test
    void artifactUnavailableNotice_namesKeyAndForbidsFabrication() {
        String key = "homelab-pilot/thread1/k8s-config/agree-abc";
        String notice = DiscussionOrchestrator.artifactUnavailableNotice(key);
        assertTrue(notice.contains(key), "names the specific artifact key");
        assertTrue(notice.contains("UNAVAILABLE"), "flags the data as unavailable");
        assertTrue(notice.toLowerCase().contains("do not") && notice.toLowerCase().contains("fabricate"),
                "instructs the synthesizer not to fabricate");
        assertFalse(notice.contains("full data is in the file"),
                "must NOT keep the misleading 'the full data is in the file' wording");
    }
}
