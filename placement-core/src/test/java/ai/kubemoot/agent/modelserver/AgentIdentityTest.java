package ai.kubemoot.agent.modelserver;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

/** The identity demand and history entries carry: namespace/agent and namespace/crew. */
class AgentIdentityTest {

    @Test
    void idsJoinNamespaceAndName() {
        var id = new AgentIdentity("crew-ns", "triager", "pilot");
        assertEquals("crew-ns/triager", id.agentId());
        assertEquals("crew-ns/pilot", id.crewId());
    }

    @Test
    void missingPartsBecomeEmpty() {
        var id = new AgentIdentity(null, "a", null);
        assertEquals("", id.namespace());
        assertEquals("", id.crew());
        assertEquals("/a", id.agentId());
        assertEquals("/", id.crewId());
    }
}
