package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;
import java.util.concurrent.CopyOnWriteArrayList;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.when;

/** With the archetype's review set to all, the coordinator wakes every analyst as it did before. */
class DiscussionOrchestratorReviewAllTest extends CoordinatorThreadTestBase {

    @Override
    protected void configureDiscuss(AgentProperties.Discuss discuss) {
        when(discuss.reviewConvene()).thenReturn(Optional.of("all"));
    }

    @Test
    void everyAnalystIsWokenWhenTheCoordinatorSelectedNone() {
        var state = new DiscussionOrchestrator.ThreadState("thread-2");
        state.advisoryTechnologies = new CopyOnWriteArrayList<>(List.of("kubernetes"));
        state.agreeSignals.put("k8s-metrics", "data");

        var plan = orchestrator.reviewPlan(state, orchestrator.analystNamesFromResumes());
        assertTrue(plan.circle().isEmpty());
        assertEquals(ReviewConvening.BASIS_ALL, plan.basis());
    }
}
