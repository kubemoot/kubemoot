package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import org.junit.jupiter.api.Test;

import java.nio.file.Path;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.CopyOnWriteArrayList;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.when;

/** A crew resumes path that is not a valid path is an unreadable file: every analyst reviews. */
class DiscussionOrchestratorMalformedResumesPathTest extends CoordinatorThreadTestBase {

    @Override
    protected String crewResumesPath(Path written) {
        return "/etc/kubemoot/crew\0resumes.json";
    }

    @Override
    protected void configureDiscuss(AgentProperties.Discuss discuss) {
        when(discuss.reviewConvene()).thenReturn(Optional.of("domain"));
    }

    @Test
    void theMalformedPathDoesNotThrowAndNoCatalogIsLoaded() {
        assertNull(orchestrator.loadCrewResumes());
    }

    @Test
    void everyAnalystReviewsWhenTheResumesCannotBeRead() {
        var state = new DiscussionOrchestrator.ThreadState("thread-3");
        state.userQuery = "Describe the proxmox nodes.";
        state.advisoryTechnologies = new CopyOnWriteArrayList<>(List.of("proxmox"));

        var plan = orchestrator.reviewPlan(state, orchestrator.analystNamesFromResumes());

        assertTrue(plan.circle().isEmpty(), "an empty circle wakes every analyst");
        assertEquals(ReviewConvening.BASIS_ALL, plan.basis());
    }
}
