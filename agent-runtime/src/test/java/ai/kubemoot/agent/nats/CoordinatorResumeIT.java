package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.node.ObjectNode;
import io.nats.client.Connection;
import io.nats.client.Nats;
import io.nats.client.api.StorageType;
import io.nats.client.api.StreamConfiguration;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.net.ServerSocket;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;
import static org.junit.jupiter.api.Assumptions.assumeTrue;
import static org.mockito.Mockito.when;

/**
 * Against a real nats-server (runs only when NATS_SERVER_BIN points at a binary): the
 * coordinator stops in the middle of a thread, agents keep talking to the stream, and a new
 * coordinator reads the log and the stream with ordered consumers, takes the thread over and
 * finishes it. The streams are the ones the chart creates, with the same subjects.
 */
class CoordinatorResumeIT extends CoordinatorThreadTestBase {

    private static final String THREAD = "t-it";
    private static final String START_META = "{\"crew\":\"crew\",\"namespace\":\"ns\"}";

    private static Process server;
    private static Path storeDir;
    private static String url;
    private Connection conn;

    @BeforeAll
    static void start() throws Exception {
        String bin = System.getenv("NATS_SERVER_BIN");
        assumeTrue(bin != null && new File(bin).canExecute(), "NATS_SERVER_BIN not set");
        int port;
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        storeDir = Files.createTempDirectory("nats-resume");
        server = new ProcessBuilder(bin, "-js", "-p", String.valueOf(port), "-sd", storeDir.toString())
                .redirectErrorStream(true).redirectOutput(storeDir.resolve("server.log").toFile()).start();
        url = "nats://127.0.0.1:" + port;
        for (int i = 0; i < 50; i++) {
            try (Connection c = Nats.connect(url)) {
                createStreams(c);
                return;
            } catch (Exception e) {
                if (server.waitFor(100, TimeUnit.MILLISECONDS)) {
                    fail("nats-server exited with " + server.exitValue() + ", see " + storeDir.resolve("server.log"));
                }
            }
        }
        fail("nats-server did not start");
    }

    private static void createStreams(Connection c) throws Exception {
        var jsm = c.jetStreamManagement();
        jsm.addStream(StreamConfiguration.builder().name("KUBEMOOT_DISCUSS").subjects("kubemoot.discuss.>")
                .storageType(StorageType.File).build());
        jsm.addStream(StreamConfiguration.builder().name(CrewScope.COORDINATOR_STREAM).subjects("kubemoot.coordinator.>")
                .storageType(StorageType.File).build());
    }

    @AfterAll
    static void stop() throws Exception {
        if (server != null) {
            server.destroy();
            server.waitFor(5, TimeUnit.SECONDS);
        }
    }

    /** A coordinator whose bus and log are the real server: a fresh orchestrator over the one connection. */
    private void startCoordinator(boolean first) throws Exception {
        build(false);
        selection = SELECT_TWO;
        if (conn != null && !first) {
            // the previous coordinator's connection stays open: it is killed by being forgotten
            conn.flush(Duration.ofSeconds(2));
        }
        if (conn == null) {
            conn = Nats.connect(url);
        }
        when(natsProvider.getConnection()).thenReturn(conn);
        when(natsProvider.isAvailable()).thenReturn(true);
        var recorder = new CoordinatorEventRecorder(true, new NatsCoordinatorEventLog(natsProvider, MAPPER),
                (crew, id) -> CrewScope.of("ns", crew).coordinatorSubject(id), "coordinator", MAPPER, Instant::now,
                eventMetrics());
        orchestrator.useEventRecorder(recorder);
    }

    private DiscussionMetrics eventMetrics() {
        return new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
    }

    /** An agent speaks: the message goes to the stream, and to the coordinator when one is listening. */
    private void say(boolean coordinatorListens, String agent, String type, String content, String metadataJson)
            throws Exception {
        ObjectNode msg = MAPPER.createObjectNode();
        msg.put("messageId", UUID.randomUUID().toString());
        msg.put("threadId", THREAD);
        msg.put("agentName", agent);
        msg.put("messageType", type);
        msg.put("content", content);
        msg.put("channel", "broadcast");
        msg.put("timestamp", Instant.now().toString());
        if (metadataJson != null) {
            msg.set("metadata", MAPPER.readTree(metadataJson));
        }
        String json = MAPPER.writeValueAsString(msg);
        conn.publish("kubemoot.discuss.ns.crew.broadcast." + THREAD, json.getBytes());
        conn.flush(Duration.ofSeconds(2));
        if (coordinatorListens) {
            orchestrator.handleMessage(json);
        }
    }

    private List<CoordinatorEvent> logged() {
        try {
            return new NatsBusReader(natsProvider).readAll(CrewScope.COORDINATOR_STREAM, "kubemoot.coordinator.ns.crew.>")
                    .stream().map(m -> CoordinatorEvent.parse(MAPPER, m.data())).toList();
        } catch (java.io.IOException e) {
            throw new IllegalStateException(e);
        }
    }

    @Test
    void aCoordinatorKilledMidThreadIsReplacedAndTheThreadFinishesOnce() throws Exception {
        // The first coordinator takes the question, convenes two agents, and hears one say it is evaluating.
        startCoordinator(true);
        say(true, "human", "thread_start", "How many nodes?", START_META);
        orchestrator.checkThreadTransition(orchestrator.threadForTest(THREAD), Instant.now());
        await(() -> logged().stream().anyMatch(e -> e.type().equals("evaluation_started")), "the evaluation did not start");
        say(true, "k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}");
        int eventsBeforeKill = logged().size();

        // It is killed. The agents finish and say so; nobody is listening.
        orchestrator = null;
        say(false, "k8s-metrics", "agree", "3 nodes", null);
        say(false, "k8s-workloads", "stand_aside", "nothing to add", null);

        // The replacement starts, reads the log and the stream, and takes the thread over.
        startCoordinator(false);
        var resume = new CoordinatorResume(true, orchestrator, new NatsBusReader(natsProvider),
                () -> CrewScope.of("ns", "crew"), MAPPER, eventMetrics(), System::currentTimeMillis);
        orchestrator.useResume(resume);
        resume.discover();
        resume.launch(Runnable::run);

        var thread = orchestrator.threadForTest(THREAD);
        assertTrue(thread != null, "the thread is back in the new coordinator");
        orchestrator.checkThreadTransition(thread, Instant.now().plusSeconds(60));
        await(() -> logged().stream().anyMatch(e -> e.type().equals("thread_closed")), "the thread did not close");

        var events = logged();
        assertEquals(1, events.stream().filter(e -> e.type().equals("coordinator_resumed")).count());
        assertTrue(events.size() > eventsBeforeKill);
        long answers = new NatsBusReader(natsProvider).readAll("KUBEMOOT_DISCUSS", "kubemoot.discuss.ns.crew.*." + THREAD)
                .stream().map(m -> parse(m.data())).filter(m -> m.path("messageType").asText().equals("synthesis")
                        && m.path("agentName").asText().equals("coordinator")).map(m -> m.path("messageId").asText())
                .distinct().count();
        assertEquals(1, answers, "one answer");
        var outcome = CoordinatorStateReducer.reduce(events);
        assertEquals("CLOSED", outcome.phase);
        assertEquals("Eleven pods.", outcome.answer);
        assertEquals(java.util.Set.of("k8s-metrics"), outcome.agree.keySet(), "the agent's answer, sent while down, was applied");
        assertEquals(java.util.Set.of("k8s-workloads"), outcome.standAside);
    }

    private static com.fasterxml.jackson.databind.JsonNode parse(byte[] data) {
        try {
            return MAPPER.readTree(data);
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }
}
