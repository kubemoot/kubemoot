package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.TreeSet;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.when;

/**
 * A subcommittee is never analysts alone: when the selection holds only analysts, the
 * coordinator adds the data gatherers that best match the question and records it on the
 * subcommittee_convened event. The real orchestrator runs against a fake model and log.
 */
class CoordinatorDataGatherersTest extends CoordinatorThreadTestBase {

    private static final String CREW = """
            [{"name":"compute","role":"analyst","description":"Computes over gathered data","tools":["execute_code"]},
             {"name":"advisor","role":"analyst","description":"Explains concepts","tools":[]},
             {"name":"k8s-nodes","role":"tooler","description":"Node status and capacity","tools":["nodes_list"]},
             {"name":"k8s-workloads","role":"tooler","description":"Pods and requests","tools":["pods_list"]},
             {"name":"k8s-storage","role":"tooler","description":"Volumes","tools":["pvc_list"]},
             {"name":"search","role":"researcher","description":"Web search","tools":["web"]}]""";

    private static final String ANALYST_ONLY =
            "{\"selected\":[\"compute\"],\"brief\":\"Add it up.\",\"technologies\":[\"kubernetes\"],\"skills\":[]}";

    @Override
    protected String resumesJson() {
        return CREW;
    }

    private CoordinatorEvent convene(String t, String selected) throws Exception {
        selection = selected;
        send(t, "human", "thread_start", "Report total cluster memory capacity and requested",
                "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now());
        awaitTypes(t, 1, "subcommittee_convened");
        return forThread(t).stream().filter(e -> e.type().equals("subcommittee_convened")).findFirst().orElseThrow();
    }

    private static Set<String> members(CoordinatorEvent e) {
        var out = new TreeSet<String>();
        e.data().path("members").forEach(n -> out.add(n.asText()));
        return out;
    }

    private static List<String> added(CoordinatorEvent e) {
        var out = new ArrayList<String>();
        e.data().path("dataAgentsAdded").forEach(n -> out.add(n.asText()));
        return out;
    }

    @Test
    void anAnalystOnlySelectionGetsTheTwoBestMatchingGatherersAndSaysSo() throws Exception {
        when(resumeSearch.isAvailable()).thenReturn(true);
        when(resumeSearch.searchResumes(anyString(), anyInt()))
                .thenReturn(List.of("compute", "k8s-nodes", "advisor", "k8s-workloads", "k8s-storage"));

        var event = convene("t-memory", ANALYST_ONLY);

        assertEquals(Set.of("compute", "k8s-nodes", "k8s-workloads"), members(event));
        assertEquals(List.of("k8s-nodes", "k8s-workloads"), added(event));
        assertEquals("analysts_only", event.data().path("dataAgentsAddedReason").asText());
        assertEquals("reasoning", event.data().path("path").asText());
    }

    @Test
    void theGatherersAreRankedByTheQuestionAndTheBriefTogether() throws Exception {
        when(resumeSearch.isAvailable()).thenReturn(true);
        when(resumeSearch.searchResumes(anyString(), anyInt())).thenReturn(List.of("compute", "k8s-nodes"));

        convene("t-query", ANALYST_ONLY);

        var queries = org.mockito.ArgumentCaptor.forClass(String.class);
        org.mockito.Mockito.verify(resumeSearch, org.mockito.Mockito.atLeastOnce())
                .searchResumes(queries.capture(), anyInt());
        var last = queries.getAllValues().get(queries.getAllValues().size() - 1);
        assertTrue(last.contains("Report total cluster memory") && last.contains("Add it up."), last);
    }

    @Test
    void theRankingDecidesWhichGatherersWhenThereAreMoreThanTheLimit() throws Exception {
        when(resumeSearch.isAvailable()).thenReturn(true);
        when(resumeSearch.searchResumes(anyString(), anyInt()))
                .thenReturn(List.of("k8s-storage", "compute", "k8s-workloads", "k8s-nodes"));

        var event = convene("t-ranked", ANALYST_ONLY);

        assertEquals(List.of("k8s-storage", "k8s-workloads"), added(event));
        assertFalse(members(event).contains("k8s-nodes"));
        assertFalse(members(event).contains("search"), "a researcher is not a data gatherer");
    }

    @Test
    void withoutAResumeSearchTheGatherersComeInCrewOrder() throws Exception {
        var event = convene("t-unranked", ANALYST_ONLY);

        assertEquals(List.of("k8s-nodes", "k8s-workloads"), added(event));
    }

    @Test
    void aSelectionOfOnlyAnalystsFromTheFallbackPathAlsoGetsGatherers() throws Exception {
        when(resumeSearch.isAvailable()).thenReturn(true);
        // the pre-filter before the reasoning call, the pre-filter of the fallback, then the ranking
        when(resumeSearch.searchResumes(anyString(), anyInt())).thenReturn(List.of("advisor"), List.of("advisor"),
                List.of("advisor", "k8s-workloads", "k8s-nodes"));

        var event = convene("t-fallback", "not json at all");

        assertEquals("fallback", event.data().path("path").asText());
        assertEquals(List.of("k8s-workloads", "k8s-nodes"), added(event));
        assertTrue(members(event).containsAll(Set.of("advisor", "k8s-workloads", "k8s-nodes")));
    }

    @Test
    void aMixedSelectionIsLeftAlone() throws Exception {
        var event = convene("t-mixed",
                "{\"selected\":[\"compute\",\"k8s-nodes\"],\"brief\":\"b\",\"technologies\":[],\"skills\":[]}");

        assertEquals(Set.of("compute", "k8s-nodes"), members(event));
        assertFalse(event.data().has("dataAgentsAdded"));
        assertFalse(event.data().has("dataAgentsAddedReason"));
    }

    @Test
    void aSelectionOfGatherersOnlyIsLeftAlone() throws Exception {
        var event = convene("t-gatherers",
                "{\"selected\":[\"k8s-storage\"],\"brief\":\"b\",\"technologies\":[],\"skills\":[]}");

        assertEquals(Set.of("k8s-storage"), members(event));
        assertFalse(event.data().has("dataAgentsAdded"));
    }

    @Test
    void theAddedGatherersAreInTheRosterTheEvaluationWaitsFor() throws Exception {
        var event = convene("t-roster", ANALYST_ONLY);
        awaitTypes("t-roster", 1, "evaluation_started");

        var roster = new TreeSet<String>();
        forThread("t-roster").stream().filter(e -> e.type().equals("evaluation_started")).findFirst().orElseThrow()
                .data().path("roster").forEach(n -> roster.add(n.asText()));
        assertEquals(Set.copyOf(added(event)), roster, "analysts act in review, the gatherers in evaluation");
        agreesNow("t-roster", "the log and the live thread agree");
    }

    @Test
    void anEventWrittenBeforeTheFieldExistedReplaysWithNoGatherersAdded() throws Exception {
        var event = convene("t-replay", "{\"selected\":[\"k8s-nodes\"],\"brief\":\"b\",\"technologies\":[]}");
        var old = new CoordinatorEvent(event.eventId(), event.type(), event.threadId(), event.round(), event.at(),
                event.coordinator(), event.data().deepCopy());
        old.data().remove("dataAgentsAdded");
        old.data().remove("dataAgentsAddedReason");

        var state = CoordinatorStateReducer.reduce(List.of(old));

        assertEquals(Set.of("k8s-nodes"), state.innerCircle);
        assertTrue(state.dataAgentsAdded.isEmpty());
    }

    @Test
    void theAddedGatherersSurviveAReplayOfTheLog() throws Exception {
        convene("t-kept", ANALYST_ONLY);

        var state = CoordinatorStateReducer.reduce(forThread("t-kept"));

        assertEquals(Set.of("k8s-nodes", "k8s-workloads"), state.dataAgentsAdded);
    }
}
