package ai.kubemoot.agent.rag;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Tests for ResumeSearchClient.isAgentMetadata - the guard that prevents
 * skill docs from being misclassified as agents in the vector path.
 *
 * Plain JUnit 5, no @QuarkusTest, no network calls.
 */
class ResumeSearchClientSkillsTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void isAgentMetadata_withAgentName_returnsTrue() throws Exception {
        var metadata = MAPPER.readTree("{\"agent_name\":\"k8s-metrics\",\"kind\":\"agent\"}");
        assertTrue(ResumeSearchClient.isAgentMetadata(metadata),
                "node with agent_name is an agent doc");
    }

    @Test
    void isAgentMetadata_skillDocNoAgentName_returnsFalse() throws Exception {
        // Skill doc: has kind=skill and skill_name but no agent_name
        var metadata = MAPPER.readTree("{\"kind\":\"skill\",\"skill_name\":\"k8s-runbook\"}");
        assertFalse(ResumeSearchClient.isAgentMetadata(metadata),
                "skill doc without agent_name must not be classified as an agent");
    }

    @Test
    void isAgentMetadata_nullMetadata_returnsFalse() {
        assertFalse(ResumeSearchClient.isAgentMetadata(null),
                "null metadata must not be classified as an agent");
    }

    @Test
    void isAgentMetadata_emptyNode_returnsFalse() throws Exception {
        var metadata = MAPPER.readTree("{}");
        assertFalse(ResumeSearchClient.isAgentMetadata(metadata),
                "empty metadata object must not be classified as an agent");
    }

    @Test
    void isAgentMetadata_agentNameFieldPresent_alwaysTrue_regardlessOfKind() throws Exception {
        // agent_name present = agent doc, regardless of what kind says
        var metadata = MAPPER.readTree("{\"agent_name\":\"some-agent\"}");
        assertTrue(ResumeSearchClient.isAgentMetadata(metadata));
    }
}
