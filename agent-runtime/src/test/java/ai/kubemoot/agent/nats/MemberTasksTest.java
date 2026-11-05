package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** One task per convened member: reading them, recording them, and what each member reads. */
class MemberTasksTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final Set<String> MEMBERS = Set.of("compute", "k8s-workloads");

    private static JsonNode json(String text) {
        try {
            return MAPPER.readTree(text);
        } catch (Exception e) {
            throw new IllegalArgumentException(e);
        }
    }

    // --- parsing ---

    @Test
    void aTaskForEveryMemberLeavesNoFallback() {
        var t = MemberTasks.parse(json("""
                {"memberTasks": {"compute": "Filter and sort.", "k8s-workloads": "List all Deployments."}}"""),
                MEMBERS, "shared");
        assertEquals(Map.of("compute", "Filter and sort.", "k8s-workloads", "List all Deployments."), t.tasks());
        assertTrue(t.fallbacks().isEmpty());
        assertTrue(t.ignored().isEmpty());
        assertEquals("Filter and sort.", t.forMember("compute"));
        assertEquals("List all Deployments.", t.forMember("k8s-workloads"));
    }

    @Test
    void membersWithoutATaskFallBackToTheSharedBrief() {
        var t = MemberTasks.parse(json("""
                {"memberTasks": {"k8s-workloads": "List all Deployments."}}"""), MEMBERS, "shared");
        assertEquals(List.of("compute"), t.fallbacks());
        assertEquals("shared", t.forMember("compute"));
        assertEquals("List all Deployments.", t.forMember("k8s-workloads"));
    }

    @Test
    void noTasksAtAllMakesEveryMemberFallBack() {
        for (String body : List.of("{}", "{\"memberTasks\": null}", "{\"memberTasks\": \"text\"}",
                "{\"memberTasks\": [\"a\"]}", "{\"memberTasks\": {}}")) {
            var t = MemberTasks.parse(json(body), MEMBERS, "shared");
            assertFalse(t.hasTasks(), body);
            assertEquals(List.of("compute", "k8s-workloads"), t.fallbacks(), body);
            assertEquals("shared", t.forMember("compute"), body);
            assertEquals("shared", t.combined(), body);
        }
        assertFalse(MemberTasks.parse(null, MEMBERS, "shared").hasTasks());
    }

    @Test
    void aTaskForANameThatIsNotAMemberIsIgnoredAndRecorded() {
        var t = MemberTasks.parse(json("""
                {"memberTasks": {"compute": "Sort.", "ghost": "Do things.", "alpha": "More."}}"""),
                MEMBERS, "shared");
        assertEquals(Map.of("compute", "Sort."), t.tasks());
        assertEquals(List.of("alpha", "ghost"), t.ignored());
        assertEquals(List.of("k8s-workloads"), t.fallbacks());
    }

    @Test
    void emptyBlankNullAndNonTextTasksCountAsNoTask() {
        var t = MemberTasks.parse(json("""
                {"memberTasks": {"compute": "   ", "k8s-workloads": null}}"""), MEMBERS, "shared");
        assertFalse(t.hasTasks());
        assertEquals(List.of("compute", "k8s-workloads"), t.fallbacks());
        var numeric = MemberTasks.parse(json("{\"memberTasks\": {\"compute\": 5, \"k8s-workloads\": \"\"}}"),
                MEMBERS, "shared");
        assertFalse(numeric.hasTasks());
    }

    @Test
    void taskTextIsTrimmed() {
        var t = MemberTasks.parse(json("{\"memberTasks\": {\"compute\": \"  Sort.\\n\"}}"), MEMBERS, "");
        assertEquals("Sort.", t.forMember("compute"));
    }

    // --- the combined text ---

    @Test
    void theCombinedTextIsTheSharedBriefOrElseEachTaskOnItsOwnLine() {
        var withBrief = MemberTasks.parse(json("{\"memberTasks\": {\"compute\": \"Sort.\"}}"), MEMBERS, "shared");
        assertEquals("shared", withBrief.combined());
        var noBrief = MemberTasks.parse(json("""
                {"memberTasks": {"compute": "Sort.", "k8s-workloads": "List."}}"""), MEMBERS, "");
        assertEquals("compute: Sort.\nk8s-workloads: List.", noBrief.combined());
        assertEquals("", MemberTasks.NONE.combined());
    }

    // --- the event ---

    @Test
    void theEventCarriesTheTasksAndWhoFellBack() {
        var t = MemberTasks.parse(json("""
                {"memberTasks": {"compute": "Sort.", "ghost": "x"}}"""), MEMBERS, "shared");
        var data = MAPPER.createObjectNode();
        t.writeTo(data, true);
        assertEquals("Sort.", data.path("memberTasks").path("compute").asText());
        assertEquals("shared", data.path("sharedBrief").asText());
        assertEquals("k8s-workloads", data.path("taskFallbacks").get(0).asText());
        assertEquals("ghost", data.path("ignoredTasks").get(0).asText());

        var back = MemberTasks.fromEvent(data);
        assertEquals(t, back);
    }

    @Test
    void theEventWithoutTasksListsEveryMemberAsAFallbackOnTheReasoningPathOnly() {
        var none = MemberTasks.parse(json("{}"), MEMBERS, "shared");
        var reasoned = MAPPER.createObjectNode();
        none.writeTo(reasoned, true);
        assertFalse(reasoned.has("memberTasks"));
        assertEquals(2, reasoned.path("taskFallbacks").size());

        var other = MAPPER.createObjectNode();
        none.writeTo(other, false);
        assertTrue(other.isEmpty(), "a fallback or direct path records no task fields");
        assertFalse(MemberTasks.fromEvent(other).hasTasks());
    }

    @Test
    void theRecorderWritesTheTasksOnSubcommitteeConvenedAndTheReducerReadsThemBack() {
        var log = new RecordingEventLog();
        var recorder = log.recorder();
        var s = new ThreadState("t1");
        s.crew = "crew";
        s.userQuery = "q";
        s.primaryChannel = "general";
        s.innerCircle = new java.util.HashSet<>(MEMBERS);
        s.memberTasks = MemberTasks.parse(json("{\"memberTasks\": {\"compute\": \"Sort.\"}}"), MEMBERS, "shared");
        var reasoning = new DiscussionOrchestrator.ReasoningResult(List.of("compute", "k8s-workloads"), "shared",
                List.of(), 1, 1, 1, s.memberTasks);
        var outcome = new DiscussionOrchestrator.AdvisoryOutcome(List.of(), List.of(), s.memberTasks.combined(), 1);

        recorder.opened(s, "request");
        recorder.subcommitteeConvened(s, reasoning, outcome);

        var convened = log.events().stream()
                .filter(e -> CoordinatorEvent.SUBCOMMITTEE_CONVENED.equals(e.type())).findFirst().orElseThrow();
        assertEquals("shared", convened.data().path("brief").asText());
        assertEquals("Sort.", convened.data().path("memberTasks").path("compute").asText());
        assertEquals("k8s-workloads", convened.data().path("taskFallbacks").get(0).asText());

        var state = CoordinatorStateReducer.reduce(log.events());
        assertEquals(s.memberTasks, state.memberTasks);
        assertEquals(s.memberTasks, ThreadRebuilder.build(state).memberTasks);
    }

    // --- delivery ---

    private static String advisory(Map<String, String> tasks, String shared) throws Exception {
        var meta = new HashMap<String, Object>();
        if (tasks != null) {
            meta.put("memberTasks", tasks);
            meta.put("sharedBrief", shared);
        }
        var msg = new HashMap<String, Object>();
        msg.put("messageType", "advisory");
        msg.put("threadId", "t1");
        msg.put("agentName", "coordinator");
        msg.put("messageId", "adv");
        msg.put("content", "ALL: compute: Sort. k8s-workloads: List.");
        msg.put("metadata", meta);
        return MAPPER.writeValueAsString(msg);
    }

    private static final String READY = """
            {"messageType":"advisory_ready","threadId":"t1","agentName":"coordinator","messageId":"rdy","content":"Technologies: kubernetes."}""";

    private static String read(String agent, String advisoryJson) throws Exception {
        var sub = subscriber(agent);
        assertNull(sub.admitForEvaluation(advisoryJson));
        var evaluation = sub.admitForEvaluation(READY);
        assertNotNull(evaluation);
        return evaluation.conversation();
    }

    private static DiscussionSubscriber subscriber(String agent) {
        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", null));
        return new DiscussionSubscriber(natsProvider, mock(ChatService.class),
                new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry()),
                DiscussionSubscriberHelpersTest.stubProperties(agent, "kubernetes", null, false),
                "http://localhost:11434");
    }

    @Test
    void eachMemberReadsOnlyItsOwnTask() throws Exception {
        String adv = advisory(Map.of("compute", "Sort.", "k8s-workloads", "List all Deployments."), "");
        String compute = read("compute", adv);
        assertTrue(compute.contains("Sort."));
        assertFalse(compute.contains("List all Deployments."));
        assertFalse(compute.contains("ALL:"));

        String workloads = read("k8s-workloads", adv);
        assertTrue(workloads.contains("List all Deployments."));
        assertFalse(workloads.contains("Sort."));
        assertFalse(workloads.contains("compute"));
    }

    @Test
    void aMemberWithoutATaskReadsTheSharedBriefAndNoOtherTask() throws Exception {
        String adv = advisory(Map.of("compute", "Sort."), "Shared framing.");
        String workloads = read("k8s-workloads", adv);
        assertTrue(workloads.contains("Shared framing."));
        assertFalse(workloads.contains("Sort."));
    }

    @Test
    void anAdvisoryWithoutTasksIsReadAsSentAndTheReadyMessageCarriesNoTasks() throws Exception {
        String text = read("k8s-workloads", advisory(null, null));
        assertTrue(text.contains("ALL: compute: Sort. k8s-workloads: List."));
    }

    @Test
    void theAdvisoryMessageCarriesTheTasksOnlyWhenThereAreSome() {
        var state = new ThreadState("t1");
        var meta = new HashMap<String, Object>();
        DiscussionOrchestrator.putMemberTasks(meta, state);
        DiscussionOrchestrator.putMemberTasks(meta, null);
        assertTrue(meta.isEmpty());

        state.memberTasks = MemberTasks.parse(json("{\"memberTasks\": {\"compute\": \"Sort.\"}}"), MEMBERS, "shared");
        DiscussionOrchestrator.putMemberTasks(meta, state);
        assertEquals(Map.of("compute", "Sort."), meta.get("memberTasks"));
        assertEquals("shared", meta.get("sharedBrief"));
    }
}
