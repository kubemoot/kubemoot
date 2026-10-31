package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.rag.ResumeSearchClient;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import io.nats.client.Connection;
import io.nats.client.impl.Headers;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Optional;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.doAnswer;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/**
 * The real orchestrator with a fake model, a fake coordinator log and, when a test asks
 * for it, a fake bus that records what is published. Tests drive a thread by sending
 * messages and running the phase sweep, and compare what the orchestrator holds with what
 * the log says.
 */
abstract class CoordinatorThreadTestBase {

    /** A message the coordinator put on the bus: where, under which stream id (null: none), and what. */
    record Published(String subject, String streamId, JsonNode body) {
        String type() {
            return body.path("messageType").asText();
        }

        String messageId() {
            return body.path("messageId").asText();
        }
    }

    protected final List<Published> published = Collections.synchronizedList(new ArrayList<>());
    /** Events appended ("event:type") and messages published ("bus:type"), in the order they happened. */
    protected final List<String> timeline = Collections.synchronizedList(new ArrayList<>());

    /** True when the orchestrator should get a connection that records its publishes. */
    protected boolean withBus() {
        return false;
    }

    protected static final ObjectMapper MAPPER = new ObjectMapper();
    protected static final String RESUMES = """
            [{"name":"k8s-metrics","description":"Kubernetes metrics tooler","tools":["pods_top"]},
             {"name":"k8s-workloads","description":"Kubernetes workloads tooler","tools":["resources_list"]},
             {"name":"k8s-analyst","role":"analyst","description":"Reads what the toolers gathered","tools":[]}]
            """;
    protected static final String SELECT_THREE =
            "{\"selected\":[\"k8s-metrics\",\"k8s-workloads\",\"k8s-analyst\"],\"brief\":\"Check pods.\","
                    + "\"technologies\":[\"kubernetes\"],\"skills\":[]}";
    protected static final String SELECT_TWO =
            "{\"selected\":[\"k8s-metrics\",\"k8s-workloads\"],\"brief\":\"Check pods.\",\"technologies\":[]}";
    protected static final String SELECT_NONE = "{\"selected\":[],\"brief\":\"Four.\",\"technologies\":[]}";

    protected DiscussionOrchestrator orchestrator;
    protected RecordingEventLog eventLog;
    protected AgentProperties.TriageModel triageModel;
    protected volatile String selection = SELECT_THREE;

    @TempDir
    Path dir;

    @BeforeEach
    void setUp() throws Exception {
        build(true);
    }

    /** A fresh orchestrator and event log; {@code hasAnalysts} is the crew's setting that forces the review phase. */
    protected void build(boolean hasAnalysts) throws Exception {
        Path resumes = dir.resolve("crew-resumes.json");
        Files.writeString(resumes, RESUMES);
        System.setProperty("kubemoot.discuss.crew-resumes-path", resumes.toString());

        var natsProvider = mock(NatsConnectionProvider.class);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns", "crew"));
        if (withBus()) {
            Connection bus = fakeBus();
            when(natsProvider.getConnection()).thenReturn(bus);
        }
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
        var model = mock(AgentProperties.Model.class);
        when(model.endpoint()).thenReturn("http://rig0:11434");
        when(model.model()).thenReturn("qwen3:14b");
        when(properties.model()).thenReturn(model);
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
        eventLog.observeWith(timeline::add);
        orchestrator.useEventRecorder(eventLog.recorder());
    }

    @AfterEach
    void tearDown() {
        System.clearProperty("kubemoot.discuss.crew-resumes-path");
    }

    /** A connection whose publishes are recorded in {@link #published} and {@link #timeline}. */
    private Connection fakeBus() {
        var conn = mock(Connection.class);
        doAnswer(call -> {
            record(call.getArgument(0), null, call.getArgument(1));
            return null;
        }).when(conn).publish(anyString(), any(byte[].class));
        doAnswer(call -> {
            Headers headers = call.getArgument(1);
            record(call.getArgument(0), headers.getFirst("Nats-Msg-Id"), call.getArgument(2));
            return null;
        }).when(conn).publish(anyString(), any(Headers.class), any(byte[].class));
        return conn;
    }

    private void record(String subject, String streamId, byte[] payload) {
        try {
            var message = new Published(subject, streamId, MAPPER.readTree(payload));
            published.add(message);
            timeline.add("bus:" + message.type());
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }

    /** The published messages of {@code type}, in order. */
    protected List<Published> publishedOfType(String type) {
        synchronized (published) {
            return published.stream().filter(p -> p.type().equals(type)).toList();
        }
    }

    // --- steps ---

    protected static final String EVALUATION_TO_REVIEW = "evaluation";
    protected static final String REVIEW_TO_SYNTHESIS = "review";

    protected void send(String threadId, String agent, String type, String content, String metadataJson)
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
    protected void advanceThroughAdvisory(String t) throws InterruptedException {
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now());
        awaitTypes(t, 1, "evaluation_started");
    }

    /** Runs the phase's settle as if enough time had passed, so the floor and the quiet period are over. */
    protected void settle(String t, String phase) throws InterruptedException {
        int before = forThread(t).size();
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now().plusSeconds(60));
        await(() -> forThread(t).size() > before, phase + " settle wrote nothing");
    }

    protected void agreesNow(String t, String what) {
        assertEquals(reduced(t), ThreadView.of(orchestrator.threadForTest(t)), what);
    }

    protected ThreadView reduced(String t) {
        return CoordinatorStateReducer.reduce(forThread(t)).view();
    }

    protected List<CoordinatorEvent> forThread(String t) {
        return eventLog.events().stream().filter(e -> e.threadId().equals(t)).toList();
    }

    protected static List<CoordinatorEvent> concat(List<CoordinatorEvent> a, List<CoordinatorEvent> b) {
        var all = new java.util.ArrayList<>(a);
        all.addAll(b);
        return all;
    }

    protected void awaitTypes(String t, int count, String type) throws InterruptedException {
        await(() -> forThread(t).stream().filter(e -> e.type().equals(type)).count() >= count,
                "no " + type + " #" + count + " for " + t + ", got " + eventLog.types());
    }

    protected static void await(BooleanSupplier condition, String failure) throws InterruptedException {
        Instant deadline = Instant.now().plus(Duration.ofSeconds(20));
        while (!condition.getAsBoolean()) {
            if (Instant.now().isAfter(deadline)) {
                throw new AssertionError(failure);
            }
            Thread.sleep(5);
        }
    }
}
