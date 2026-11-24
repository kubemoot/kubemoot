package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.ConcurrentHashMap;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.when;

/** Which analysts the coordinator wakes for review, as its crew's archetype declares. */
class DiscussionOrchestratorReviewConveningTest extends CoordinatorThreadTestBase {

    private static final String PROXMOX_ADVISOR = "proxmox-advisor";
    private static final String K8S_ADVISOR = "k8s-advisor";
    private static final String PROXMOX_PVE = "proxmox-pve";

    private volatile String convene = "domain";

    @Override
    protected String resumesJson() {
        return """
                [{"name":"proxmox-pve","role":"tooler","domains":["proxmox"]},
                 {"name":"k8s-nodes","role":"tooler","domains":["kubernetes"]},
                 {"name":"proxmox-advisor","role":"analyst","domains":["proxmox"]},
                 {"name":"nvidia-gpu-advisor","role":"analyst","domains":["gpu"]},
                 {"name":"k8s-advisor","role":"analyst","domains":["kubernetes"]}]
                """;
    }

    @Override
    protected void configureDiscuss(AgentProperties.Discuss discuss) {
        when(discuss.reviewConvene()).thenReturn(Optional.of(convene));
        when(discuss.reviewFallback()).thenReturn(Optional.of("primary"));
        when(discuss.reviewGeneralists()).thenReturn(Optional.empty());
    }

    private ReviewConvening.Plan plan(DiscussionOrchestrator.ThreadState state) {
        return orchestrator.reviewPlan(state, orchestrator.analystNamesFromResumes());
    }

    private static DiscussionOrchestrator.ThreadState thread(List<String> technologies, String query,
                                                              String... agreed) {
        var state = new DiscussionOrchestrator.ThreadState("thread-1");
        state.userQuery = query;
        state.advisoryTechnologies = new CopyOnWriteArrayList<>(technologies);
        for (String agent : agreed) {
            state.agreeSignals.put(agent, "data");
        }
        return state;
    }

    @Test
    void aProxmoxQuestionWakesOnlyTheProxmoxAnalyst() {
        var state = thread(List.of("proxmox"), "Describe the proxmox nodes.", PROXMOX_PVE);

        var plan = plan(state);
        assertEquals(List.of(PROXMOX_ADVISOR), plan.circle());
        assertEquals(ReviewConvening.BASIS_DOMAIN, plan.basis());
    }

    @Test
    void theAnalystTheCoordinatorSelectedReviewsToo() {
        var state = thread(List.of("proxmox"), "q", PROXMOX_PVE);
        state.innerCircle = ConcurrentHashMap.newKeySet();
        state.innerCircle.addAll(Set.of(PROXMOX_PVE, K8S_ADVISOR));

        assertEquals(Set.of(PROXMOX_ADVISOR, K8S_ADVISOR), Set.copyOf(plan(state).circle()));
    }

    @Test
    void aQuestionNoDeclaredDomainTouchesStillGetsAReviewer() {
        var state = thread(List.of(), "Is the kubernetes version current?");

        var plan = plan(state);
        assertEquals(List.of(K8S_ADVISOR), plan.circle());
        assertEquals(ReviewConvening.BASIS_PRIMARY, plan.basis());
    }

    @Test
    void whenNothingCanRankTheAnalystsEveryAnalystReviews() {
        var state = thread(List.of(), "Hello");

        var plan = plan(state);
        assertTrue(plan.circle().isEmpty(), "empty means every analyst is woken");
        assertEquals(ReviewConvening.BASIS_FALLBACK_ALL, plan.basis());
    }

    @Test
    void aRestartedCoordinatorWakesTheRosterTheLogRestored() {
        var state = thread(List.of("proxmox"), "q", PROXMOX_PVE);
        state.phaseRoster.set(PhaseRoster.of(List.of(K8S_ADVISOR, "nvidia-gpu-advisor")));

        assertEquals(List.of(K8S_ADVISOR, "nvidia-gpu-advisor"), orchestrator.resumedReviewCircle(state),
                "the restored roster wins over a plan made again from the rebuilt thread");
    }

    @Test
    void aRestartedCoordinatorWithNoRosterOfAnalystsMakesThePlanAgain() {
        var state = thread(List.of("proxmox"), "q", PROXMOX_PVE);
        state.phaseRoster.set(PhaseRoster.of(List.of(PROXMOX_PVE)));

        assertEquals(List.of(PROXMOX_ADVISOR), orchestrator.resumedReviewCircle(state));
    }
}
