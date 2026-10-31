package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * The orchestrator and the reducer agree. The real orchestrator runs threads through
 * their phases (the model and the bus are faked, nothing else), every decision it makes
 * is written to a fake publisher, and after each step the state the orchestrator holds
 * must equal the state the reducer rebuilds from the events written so far.
 */
class CoordinatorEventRoundTripTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String RESUMES = """
            [{"name":"k8s-metrics","description":"Kubernetes metrics tooler","tools":["pods_top"]},
             {"name":"k8s-workloads","description":"Kubernetes workloads tooler","tools":["resources_list"]},
             {"name":"k8s-analyst","role":"analyst","description":"Reads what the toolers gathered","tools":[]}]
            """;
    private static final String SELECT_THREE =
            "{\"selected\":[\"k8s-metrics\",\"k8s-workloads\",\"k8s-analyst\"],\"brief\":\"Check pods.\","
                    + "\"technologies\":[\"kubernetes\"],\"skills\":[]}";
    private static final String SELECT_TWO =
            "{\"selected\":[\"k8s-metrics\",\"k8s-workloads\"],\"brief\":\"Check pods.\",\"technologies\":[]}";
    private static final String SELECT_NONE = "{\"selected\":[],\"brief\":\"Four.\",\"technologies\":[]}";

    private DiscussionOrchestrator orchestrator;
    private RecordingEventLog eventLog;
    private AgentProperties.TriageModel triageModel;
    private volatile String selection = SELECT_THREE;

    @TempDir
    Path dir;

    @BeforeEach
    void setUp() throws Exception {
        build(true);
    }

    /** A fresh orchestrator and event log; {@code hasAnalysts} is the crew's setting that forces the review phase. */
    private void build(boolean hasAnalysts) throws Exception {
        Path resumes = dir.resolve("crew-resumes.json");
        Files.writeString(resumes, RESUMES);
        System.setProperty("kubemoot.discuss.crew-resumes-path", resumes.toString());

        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns", "crew"));
        var chatService = mock(ChatService.class);
        when(chatService.simpleLlmCallWithTokens(any(), anyString())).thenAnswer(call -> {
            String prompt = call.getArgument(1);
            String text = prompt.contains("Discussion thread") ? "Eleven pods." : selection;
            return new ChatService.SimpleLlmResult(text, 100, 20);
        });

        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        triageModel = mock(AgentProperties.TriageModel.class);
        when(properties.discuss()).thenReturn(discuss);
        when(properties.agentName()).thenReturn("coordinator");
        when(properties.triageModel()).thenReturn(triageModel);
        when(triageModel.timeoutSeconds()).thenReturn(120);
        when(discuss.coordinator()).thenReturn(true);
        when(discuss.answerDirectly()).thenReturn(true);
        when(discuss.hasAnalysts()).thenReturn(hasAnalysts);
        when(discuss.channels()).thenReturn(Optional.of("general"));
        when(discuss.settleSeconds()).thenReturn(5);
        when(discuss.minEvalSeconds()).thenReturn(20);
        when(discuss.minReviewSeconds()).thenReturn(10);
        when(discuss.advisoryTimeoutSeconds()).thenReturn(10);
        when(discuss.evaluationTimeoutSeconds()).thenReturn(15);
        when(discuss.reviewTimeoutSeconds()).thenReturn(15);
        when(discuss.synthesisTimeoutSeconds()).thenReturn(90);
        when(discuss.evalGraceSeconds()).thenReturn(10);
        when(discuss.conversationMaxTurns()).thenReturn(10);
        when(discuss.conversationTtlMinutes()).thenReturn(1440);
        when(discuss.conversationKvBucket()).thenReturn("kubemoot_conversations");
        when(discuss.resumePreFilterTopK()).thenReturn(5);

        orchestrator = new DiscussionOrchestrator(natsProvider, properties, chatService,
                new DiscussionMetrics(new SimpleMeterRegistry()), mock(LatencyTracker.class),
                mock(ResumeSearchClient.class));
        eventLog = new RecordingEventLog();
        orchestrator.useEventRecorder(eventLog.recorder());
    }

    @AfterEach
    void tearDown() {
        System.clearProperty("kubemoot.discuss.crew-resumes-path");
    }

    // --- the threads ---

    @Test
    void aThreadThroughAdvisoryEvaluationReviewSynthesisReopenPauseAndStop() throws Exception {
        String t = "t-full";
        send(t, "human", "thread_start", "How many pods run?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        assertEquals("ADVISORY", orchestrator.threadForTest(t).phase.name());
        agreesNow(t, "an external thread_start opened the thread and entered ADVISORY");

        advanceThroughAdvisory(t);
        agreesNow(t, "advisory convened the subcommittee and started the evaluation");

        send(t, "k8s-metrics", "triaging", "", null);
        agreesNow(t, "a triaging signal opens a deadline");
        send(t, "k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}");
        agreesNow(t, "an evaluating signal sets the evaluation deadline");
        send(t, "k8s-workloads", "waiting", "", "{\"model\":\"qwen3:14b\"}");
        agreesNow(t, "a waiting signal records the model it waits for");
        send(t, "k8s-metrics", "agree", "11 pods", "{\"role\":\"researcher\"}");
        send(t, "k8s-workloads", "agree", "12 pods", null);
        send(t, "other-d", "contribution", "also 12", null);
        send(t, "other-e", "block", "stop", null);
        agreesNow(t, "both toolers agreed, a contribution counted as an agree, and one agent blocked");

        settle(t, EVALUATION_TO_REVIEW);
        agreesNow(t, "evaluation settled into review with the analyst on the roster");

        send(t, "k8s-analyst", "concern", "check restarts", null);
        agreesNow(t, "the analyst signalled in review");
        settle(t, REVIEW_TO_SYNTHESIS);
        awaitTypes(t, 1, "thread_closed");
        agreesNow(t, "synthesis closed the thread");

        send(t, "human", "reply", "And in kube-system?", null);
        agreesNow(t, "a human reply reopened the thread");
        send(t, "k8s-metrics", "evaluating", "", null);
        send(t, "dashboard", "thread_pause", "", null);
        agreesNow(t, "paused with the earlier phase saved");
        send(t, "dashboard", "thread_resume", "", null);
        agreesNow(t, "resumed to the saved phase");
        send(t, "dashboard", "stop_requested", "", null);
        awaitTypes(t, 2, "thread_closed");
        agreesNow(t, "a stop forced synthesis and the thread closed again");
        assertEquals(1, orchestrator.threadForTest(t).trace.round(), "one reopen, so round 1");
    }

    @Test
    void aThreadWithOneAgreeSkipsReviewAndRecordsFailureCapacityAndStandAside() throws Exception {
        build(false);
        String t = "t-skip";
        selection = SELECT_TWO;
        send(t, "human", "thread_start", "Count nodes", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        agreesNow(t, "evaluation started with two toolers");

        send(t, "k8s-metrics", "agree", "3 nodes", null);
        send(t, "k8s-workloads", "stand_aside", "no gpu", "{\"reason\":\"gpu-busy\",\"model\":\"qwen3:14b\"}");
        send(t, "other-a", "stand_aside", "too big", "{\"reason\":\"model-too-large\",\"model\":\"qwen3:32b\"}");
        send(t, "other-b", "failure", "tool down", "{\"failedTool\":\"pods_top, resources_list\"}");
        send(t, "other-c", "decline", "n/a", null);
        agreesNow(t, "agree, capacity stand-asides, a failure and a decline");

        settle(t, EVALUATION_TO_REVIEW);
        awaitTypes(t, 1, "thread_closed");
        agreesNow(t, "a lone tooler agree skipped review and closed the thread");
        assertTrue(eventLog.events().stream().anyMatch(e -> "skip_review".equals(e.data().path("reason").asText())),
                "the skip is recorded as the reason synthesis began");
    }

    @Test
    void aDirectAnswerIsRecordedAsASubcommitteeOfNobodyAndClosesTheThread() throws Exception {
        String t = "t-direct";
        selection = SELECT_NONE;
        send(t, "human", "thread_start", "What is 2+2?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now());
        awaitTypes(t, 1, "thread_closed");
        agreesNow(t, "the coordinator answered directly");
        var convened = eventLog.events().stream().filter(e -> "subcommittee_convened".equals(e.type())).findFirst();
        assertEquals("direct", convened.orElseThrow().data().path("path").asText());
    }

    @Test
    void anAgentPastItsDeadlineGetsAFailureTheCoordinatorRecords() throws Exception {
        String t = "t-timeout";
        send(t, "human", "thread_start", "Slow question", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        when(triageModel.timeoutSeconds()).thenReturn(-5);
        send(t, "k8s-metrics", "triaging", "", null);
        send("", "k8s-workloads", "waking", "", null);
        agreesNow(t, "triaging and waking set deadlines");

        settle(t, EVALUATION_TO_REVIEW);
        agreesNow(t, "the expired agents were failed by the coordinator");
        assertTrue(eventLog.events().stream().anyMatch(e -> "failure_recorded".equals(e.type())
                && e.data().path("synthesized").asBoolean()), "a synthesized failure is in the log");
    }

    @Test
    void aHeartbeatWritesNoEventAndMovesOnlyTheDeadline() throws Exception {
        String t = "t-beat";
        send(t, "human", "thread_start", "Long question", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        send(t, "k8s-metrics", "evaluating", "", null);
        int before = eventLog.events().size();

        send(t, "k8s-metrics", "heartbeat", "", null);

        assertEquals(before, eventLog.events().size(), "a heartbeat writes nothing");
        assertEquals(withoutDeadlines(reduced(t)), withoutDeadlines(ThreadView.of(orchestrator.threadForTest(t))));
    }

    @Test
    void replayingTheWholeLogTwiceGivesTheSameStateAsOnce() throws Exception {
        String t = "t-replay";
        selection = SELECT_TWO;
        send(t, "human", "thread_start", "Count", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        send(t, "k8s-metrics", "agree", "1", null);
        send(t, "k8s-workloads", "agree", "1", null);
        settle(t, EVALUATION_TO_REVIEW);
        send(t, "k8s-analyst", "agree", "checked", null);
        settle(t, REVIEW_TO_SYNTHESIS);
        awaitTypes(t, 1, "thread_closed");

        var once = CoordinatorStateReducer.reduce(forThread(t));
        var twice = CoordinatorStateReducer.reduce(concat(forThread(t), forThread(t)));

        assertEquals(once.view(), twice.view());
        assertEquals(ThreadView.of(orchestrator.threadForTest(t)), once.view());
    }

    @Test
    void everyEventIsWrittenToTheThreadsOwnSubject() throws Exception {
        String t = "t-subject";
        send(t, "human", "thread_start", "Count", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");

        assertEquals(List.of("kubemoot.coordinator.ns.crew." + t, "kubemoot.coordinator.ns.crew." + t),
                eventLog.subjects(), "thread_opened and phase_entered");
    }

    // --- steps ---

    private static final String EVALUATION_TO_REVIEW = "evaluation";
    private static final String REVIEW_TO_SYNTHESIS = "review";

    private void send(String threadId, String agent, String type, String content, String metadataJson)
            throws Exception {
        ObjectNode msg = MAPPER.createObjectNode();
        msg.put("messageId", java.util.UUID.randomUUID().toString());
        msg.put("threadId", threadId);
        msg.put("agentName", agent);
        msg.put("messageType", type);
        msg.put("content", content);
        msg.put("channel", "broadcast");
        msg.put("timestamp", Instant.now().toString());
        if (metadataJson != null) {
            msg.set("metadata", MAPPER.readTree(metadataJson));
        }
        orchestrator.handleMessage(MAPPER.writeValueAsString(msg));
    }

    /** Runs the ADVISORY step for real: the model call, the selection, the roster; waits for its last event. */
    private void advanceThroughAdvisory(String t) throws InterruptedException {
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now());
        awaitTypes(t, 1, "evaluation_started");
    }

    /** Runs the phase's settle as if enough time had passed, so the floor and the quiet period are over. */
    private void settle(String t, String phase) throws InterruptedException {
        int before = forThread(t).size();
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now().plusSeconds(60));
        await(() -> forThread(t).size() > before, phase + " settle wrote nothing");
    }

    private void agreesNow(String t, String what) {
        assertEquals(reduced(t), ThreadView.of(orchestrator.threadForTest(t)), what);
    }

    private ThreadView reduced(String t) {
        return CoordinatorStateReducer.reduce(forThread(t)).view();
    }

    private List<CoordinatorEvent> forThread(String t) {
        return eventLog.events().stream().filter(e -> e.threadId().equals(t)).toList();
    }

    private static List<CoordinatorEvent> concat(List<CoordinatorEvent> a, List<CoordinatorEvent> b) {
        var all = new java.util.ArrayList<>(a);
        all.addAll(b);
        return all;
    }

    private void awaitTypes(String t, int count, String type) throws InterruptedException {
        await(() -> forThread(t).stream().filter(e -> e.type().equals(type)).count() >= count,
                "no " + type + " #" + count + " for " + t + ", got " + eventLog.types());
    }

    private static void await(BooleanSupplier condition, String failure) throws InterruptedException {
        Instant deadline = Instant.now().plus(Duration.ofSeconds(20));
        while (!condition.getAsBoolean()) {
            if (Instant.now().isAfter(deadline)) {
                throw new AssertionError(failure);
            }
            Thread.sleep(5);
        }
    }

    private static ThreadView withoutDeadlines(ThreadView v) {
        var pending = new java.util.TreeMap<String, Long>();
        v.roster().pending().keySet().forEach(agent -> pending.put(agent, 0L));
        return new ThreadView(v.ids(), v.position(), v.selection(),
                new ThreadView.Roster(v.roster().members(), v.roster().signalled(), pending),
                v.signals(), v.capacity(), v.tokens());
    }
}
