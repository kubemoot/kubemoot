package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.lease.KvLeaseStore;
import ai.kubemoot.agent.lease.LeaseStore;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.Nats;
import io.nats.client.api.KeyValueConfiguration;
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
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;
import static org.junit.jupiter.api.Assumptions.assumeTrue;
import static org.mockito.Mockito.when;

/**
 * Against a real nats-server (runs only when NATS_SERVER_BIN points at a binary): the lease
 * bucket with its TTL, the ordered live feed that carries stream sequences, the log read from a
 * point in time, and two coordinator replicas over the real streams, one of which stops and the
 * other takes its thread over. The streams and the bucket are the ones the chart creates.
 */
class CoordinatorLeaseIT extends CoordinatorThreadTestBase {

    private static final String BUCKET = "kubemoot_coordinator_leases";
    private static final String START_META = "{\"crew\":\"crew\",\"namespace\":\"ns\"}";
    private static final String KEY_PREFIX = "ns.crew.";

    private static Process server;
    private static Path storeDir;
    private static String url;

    @BeforeAll
    static void start() throws Exception {
        String bin = System.getenv("NATS_SERVER_BIN");
        assumeTrue(bin != null && new File(bin).canExecute(), "NATS_SERVER_BIN not set");
        int port;
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        storeDir = Files.createTempDirectory("nats-lease");
        server = new ProcessBuilder(bin, "-js", "-p", String.valueOf(port), "-sd", storeDir.toString())
                .redirectErrorStream(true).redirectOutput(storeDir.resolve("server.log").toFile()).start();
        url = "nats://127.0.0.1:" + port;
        for (int i = 0; i < 50; i++) {
            try (Connection c = Nats.connect(url)) {
                create(c);
                return;
            } catch (Exception e) {
                if (server.waitFor(100, TimeUnit.MILLISECONDS)) {
                    fail("nats-server exited with " + server.exitValue() + ", see " + storeDir.resolve("server.log"));
                }
            }
        }
        fail("nats-server did not start");
    }

    /** The streams as the chart creates them, and the lease bucket with a short TTL so a test need not wait 15s. */
    private static void create(Connection c) throws Exception {
        var jsm = c.jetStreamManagement();
        jsm.addStream(StreamConfiguration.builder().name("KUBEMOOT_DISCUSS").subjects("kubemoot.discuss.>")
                .storageType(StorageType.File).build());
        jsm.addStream(StreamConfiguration.builder().name(CrewScope.COORDINATOR_STREAM).subjects("kubemoot.coordinator.>")
                .storageType(StorageType.File).build());
        c.keyValueManagement().create(KeyValueConfiguration.builder().name(BUCKET).ttl(Duration.ofSeconds(2))
                .maxHistoryPerKey(1).storageType(StorageType.File).build());
    }

    @AfterAll
    static void stop() throws Exception {
        if (server != null) {
            server.destroy();
            server.waitFor(5, TimeUnit.SECONDS);
        }
    }

    // --- the bucket ---

    @Test
    void theBucketGivesOneHolderPerKeyAndDropsAKeyThatIsNotRenewedWithinItsTtl() throws Exception {
        try (Connection c = Nats.connect(url)) {
            LeaseStore a = new KvLeaseStore(c, BUCKET);
            LeaseStore b = new KvLeaseStore(c, BUCKET);

            long first = a.acquire("ttl-1", "pod-a");
            assertTrue(first > 0);
            assertEquals(LeaseStore.NOT_HELD, b.acquire("ttl-1", "pod-b"), "the key is taken");
            long renewed = a.renew("ttl-1", "pod-a", first);
            assertTrue(renewed > first);
            assertEquals(LeaseStore.NOT_HELD, a.renew("ttl-1", "pod-a", first), "a stale revision loses");

            long taken = pollAcquire(b, "ttl-1", "pod-b");
            assertTrue(taken > 0, "the bucket dropped the key that was not renewed");
            assertEquals(LeaseStore.NOT_HELD, a.renew("ttl-1", "pod-a", renewed), "the deposed holder cannot renew");
            b.release("ttl-1", taken);
            assertTrue(a.acquire("ttl-1", "pod-a") > 0, "a released key is free at once");
        }
    }

    /** A condition that may throw, waited for until it holds or the wait runs out. */
    private interface Check {
        boolean holds() throws Exception;
    }

    private static void until(Check check, String failure) throws Exception {
        Instant deadline = Instant.now().plusSeconds(30);
        while (!check.holds()) {
            if (Instant.now().isAfter(deadline)) {
                throw new AssertionError(failure);
            }
            Thread.sleep(100);
        }
    }

    private static long pollAcquire(LeaseStore store, String key, String holder) throws Exception {
        Instant deadline = Instant.now().plusSeconds(15);
        while (Instant.now().isBefore(deadline)) {
            long revision = store.acquire(key, holder);
            if (revision != LeaseStore.NOT_HELD) {
                return revision;
            }
            Thread.sleep(200);
        }
        return LeaseStore.NOT_HELD;
    }

    // --- the feed and the reader ---

    @Test
    void theLiveFeedDeliversOnlyNewMessagesEachWithItsStreamSequence() throws Exception {
        try (Connection c = Nats.connect(url)) {
            c.publish("kubemoot.discuss.ns.feed.broadcast.old", "{\"n\":0}".getBytes());
            c.flush(Duration.ofSeconds(2));
            var received = new CopyOnWriteArrayList<long[]>();
            var bodies = new CopyOnWriteArrayList<String>();
            DiscussionFeed.subscribe(c, c.createDispatcher(), "kubemoot.discuss.ns.feed.>", (data, sequence) -> {
                bodies.add(data);
                received.add(new long[] {sequence});
            });

            c.publish("kubemoot.discuss.ns.feed.broadcast.t1", "{\"n\":1}".getBytes());
            c.publish("kubemoot.discuss.ns.feed.broadcast.t1", "{\"n\":2}".getBytes());
            until(() -> received.size() >= 2, "the feed delivered " + bodies);

            assertEquals(List.of("{\"n\":1}", "{\"n\":2}"), bodies, "the message that was there before is not replayed");
            assertTrue(received.get(0)[0] > 0);
            assertEquals(received.get(0)[0] + 1, received.get(1)[0], "consecutive messages of the stream, in order");
        }
    }

    @Test
    void theLogCanBeReadFromAPointInTime() throws Exception {
        try (Connection c = Nats.connect(url)) {
            when(natsProvider.getConnection()).thenReturn(c);
            c.publish("kubemoot.discuss.ns.since.broadcast.t1", "before".getBytes());
            c.flush(Duration.ofSeconds(2));
            Thread.sleep(1200);
            Instant cutoff = Instant.now();
            Thread.sleep(200);
            c.publish("kubemoot.discuss.ns.since.broadcast.t1", "after".getBytes());
            c.flush(Duration.ofSeconds(2));

            var read = new NatsBusReader(natsProvider).readSince("KUBEMOOT_DISCUSS", "kubemoot.discuss.ns.since.>", cutoff);

            assertEquals(List.of("after"), read.stream().map(m -> new String(m.data())).toList());
        }
    }

    // --- two replicas ---

    /** One replica: an orchestrator on the real streams with leases in the real bucket, hearing the real feed. */
    private record Replica(String pod, DiscussionOrchestrator orch, CoordinatorResume resume, Dispatcher feed,
                           NatsConnectionProvider provider) {
    }

    private Replica startReplica(Connection conn, String pod) throws Exception {
        build(false);
        selection = SELECT_TWO;
        when(natsProvider.getConnection()).thenReturn(conn);
        when(natsProvider.isAvailable()).thenReturn(true);
        var orch = orchestrator;
        var metrics = new DiscussionMetrics(new SimpleMeterRegistry());
        orch.useEventRecorder(new CoordinatorEventRecorder(true, new NatsCoordinatorEventLog(natsProvider, MAPPER),
                (crew, id) -> CrewScope.of("ns", crew).coordinatorSubject(id), "coordinator", MAPPER, Instant::now,
                metrics));
        var leases = new CoordinatorLeases(() -> new KvLeaseStore(conn, BUCKET), () -> KEY_PREFIX, pod, System::nanoTime,
                orch::threadIsOpen, orch::threadLost);
        var resume = new CoordinatorResume(true, orch, new NatsBusReader(natsProvider),
                () -> CrewScope.of("ns", "crew"), MAPPER, metrics, System::currentTimeMillis, Duration.ZERO, leases);
        orch.useResume(resume);
        resume.discover();
        Dispatcher feed = conn.createDispatcher();
        DiscussionFeed.subscribe(conn, feed, "kubemoot.discuss.ns.crew.>", orch::handleMessage);
        return new Replica(pod, orch, resume, feed, natsProvider);
    }

    private static void say(Connection conn, String threadId, String agent, String type, String content,
                            String metadataJson, Instant sentAt) throws Exception {
        ObjectNode msg = MAPPER.createObjectNode();
        msg.put("messageId", UUID.randomUUID().toString());
        msg.put("threadId", threadId);
        msg.put("agentName", agent);
        msg.put("messageType", type);
        msg.put("content", content);
        msg.put("channel", "broadcast");
        msg.put("timestamp", sentAt.toString());
        if (metadataJson != null) {
            msg.set("metadata", MAPPER.readTree(metadataJson));
        }
        conn.publish("kubemoot.discuss.ns.crew.broadcast." + threadId, MAPPER.writeValueAsBytes(msg));
        conn.flush(Duration.ofSeconds(2));
    }

    private List<CoordinatorEvent> logged(Connection conn, String threadId) throws Exception {
        when(natsProvider.getConnection()).thenReturn(conn);
        var out = new ArrayList<CoordinatorEvent>();
        for (var message : new NatsBusReader(natsProvider).readAll(CrewScope.COORDINATOR_STREAM,
                CrewScope.of("ns", "crew").coordinatorSubject(threadId))) {
            out.add(CoordinatorEvent.parse(MAPPER, message.data()));
        }
        return out;
    }

    private int answersOn(Connection conn, String threadId) throws Exception {
        when(natsProvider.getConnection()).thenReturn(conn);
        int answers = 0;
        for (var message : new NatsBusReader(natsProvider).readAll("KUBEMOOT_DISCUSS", "kubemoot.discuss.ns.crew.broadcast." + threadId)) {
            JsonNode body = MAPPER.readTree(message.data());
            if ("synthesis".equals(body.path("messageType").asText())) {
                answers++;
            }
        }
        return answers;
    }

    @Test
    void aThreadWhoseOwnerStoppedIsTakenOverThroughTheRealBucketAndFinishedOnce() throws Exception {
        String t = "t-" + UUID.randomUUID();
        try (Connection conn = Nats.connect(url)) {
            Replica a = startReplica(conn, "pod-a");
            Replica b = startReplica(conn, "pod-b");

            say(conn, t, "human", "thread_start", "How many nodes?", START_META, Instant.now());
            until(() -> a.orch().holdsThread(t) ^ b.orch().holdsThread(t), "exactly one replica should own the thread");
            Replica owner = a.orch().holdsThread(t) ? a : b;
            Replica other = owner == a ? b : a;
            assertFalse(other.orch().holdsThread(t));

            owner.orch().checkThreadTransition(owner.orch().threadForTest(t), Instant.now());
            until(() -> logged(conn, t).stream().anyMatch(e -> e.type().equals("evaluation_started")),
                    "the evaluation did not start");
            say(conn, t, "k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}", Instant.now());
            until(() -> logged(conn, t).stream().anyMatch(e -> e.type().equals("signal_applied")), "the signal was not logged");
            long applied = CoordinatorStateReducer.reduce(logged(conn, t)).discussSeq;
            assertTrue(applied > 0, "the owner's events state the stream sequence it applied");

            // The owner stops hearing the stream and stops renewing. The agents and the dashboard go on:
            // the pause carries a time an hour behind the coordinator's clock.
            conn.closeDispatcher(owner.feed());
            say(conn, t, "dashboard", "thread_pause", "", null, Instant.now().minus(Duration.ofHours(1)));
            say(conn, t, "k8s-metrics", "agree", "3 nodes", null, Instant.now());
            say(conn, t, "k8s-workloads", "stand_aside", "nothing to add", null, Instant.now());

            until(() -> {
                other.resume().scan(Runnable::run);
                return other.orch().holdsThread(t);
            }, "the other replica did not take the thread over after the lease lapsed");

            assertEquals(1, logged(conn, t).stream().filter(e -> e.type().equals("coordinator_resumed")).count());
            assertEquals(Phase.PAUSED, other.orch().threadForTest(t).phase,
                    "the pause came after the owner's last decision in the stream, whatever its timestamp says");
            say(conn, t, "dashboard", "thread_resume", "", null, Instant.now());
            until(() -> other.orch().threadForTest(t).phase != Phase.PAUSED, "the thread did not resume");
            other.orch().checkThreadTransition(other.orch().threadForTest(t), Instant.now().plusSeconds(60));
            until(() -> logged(conn, t).stream().anyMatch(e -> e.type().equals("thread_closed")), "the thread did not close");

            assertEquals(1, answersOn(conn, t), "one answer");
            assertEquals(1, logged(conn, t).stream().filter(e -> e.type().equals("thread_opened")).count());
            assertEquals("Eleven pods.", CoordinatorStateReducer.reduce(logged(conn, t)).answer);
            var leaseStore = new KvLeaseStore(conn, BUCKET);
            assertTrue(pollAcquire(leaseStore, KEY_PREFIX + t, "probe") > 0, "the lease was given up at close");
        }
    }
}
