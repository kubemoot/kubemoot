package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.lease.FakeLeaseBucket;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** With leases off the coordinator behaves as it did: one replica owns every thread, nothing carries a sequence. */
class CoordinatorLeaseOffTest extends CoordinatorThreadTestBase {

    private static final String START_META = "{\"crew\":\"crew\",\"namespace\":\"ns\"}";

    @Override
    protected boolean withBus() {
        return true;
    }

    @Test
    void theOrchestratorHasNoLeasesAndAMessageForAnUnknownThreadStillMakesAThread() throws Exception {
        assertFalse(orchestrator.leases().enabled());

        send("t1", "human", "thread_start", "How many nodes?", START_META);

        assertNotNull(orchestrator.threadForTest("t1"), "any replica takes up any thread when there is only one");
        assertTrue(orchestrator.holdsThread("t1"));
    }

    @Test
    void eventsCarryNoStreamSequenceWhenTheFeedGivesNone() throws Exception {
        send("t1", "human", "thread_start", "How many nodes?", START_META);
        advanceThroughAdvisory("t1");
        send("t1", "k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}");

        assertFalse(eventLog.events().isEmpty());
        for (CoordinatorEvent event : eventLog.events()) {
            assertFalse(event.data().has("discussSeq"), event.type() + " must not carry a sequence");
        }
        assertEquals(0, CoordinatorStateReducer.reduce(eventLog.events()).discussSeq);
    }

    @Test
    void theRecorderStampsTheSequenceItHadAppliedOnEveryEventWhenThereIsOne() throws Exception {
        orchestrator.handleMessage(startMessage(), 7);
        advanceThroughAdvisory("t1");

        assertFalse(eventLog.events().isEmpty());
        for (CoordinatorEvent event : eventLog.events()) {
            assertEquals(7, event.data().path("discussSeq").asLong(), event.type());
        }
        assertEquals(7, CoordinatorStateReducer.reduce(eventLog.events()).discussSeq);
    }

    @Test
    void theReducerKeepsTheHighestSequenceAnyEventStates() {
        var low = event("a", 5);
        var high = event("b", 9);
        var later = event("c", 6);
        assertEquals("thread_opened", low.type());

        assertEquals(9, CoordinatorStateReducer.reduce(java.util.List.of(low, high, later)).discussSeq);
    }

    @Test
    void aRebuiltThreadStartsFromTheLoggedSequence() throws Exception {
        orchestrator.handleMessage(startMessage(), 7);
        advanceThroughAdvisory("t1");

        var rebuilt = ThreadRebuilder.build(CoordinatorStateReducer.reduce(eventLog.events()));

        assertEquals(7, rebuilt.discussSeq);
    }

    @Test
    void withoutLeasesTheReplicaIsAlwaysThePump() {
        var consumer = pumpConsumer(orchestrator);

        assertTrue(consumer.holdsPump());
    }

    @Test
    void onlyTheReplicaHoldingThePumpLeasePullsRequests() throws Exception {
        var nanos = new AtomicLong(1);
        var bucket = new FakeLeaseBucket(nanos::get, CoordinatorLeases.LEASE.toNanos());
        var first = pumpConsumer(withLeases(bucket, nanos, "pod-a"));
        var second = pumpConsumer(withLeases(bucket, nanos, "pod-b"));

        assertTrue(first.holdsPump());
        assertFalse(second.holdsPump(), "the other replica waits and watches");
        assertTrue(first.holdsPump(), "and the holder keeps it");

        nanos.addAndGet(CoordinatorLeases.LEASE.toNanos());
        assertTrue(second.holdsPump(), "when the holder stops renewing, the other replica becomes the pump");
        assertFalse(first.holdsPump());
    }

    @Test
    void aReplicaWithoutThePumpLeaseDropsItsSubscriptionAndPullsNothing() throws Exception {
        var nanos = new AtomicLong(1);
        var bucket = new FakeLeaseBucket(nanos::get, CoordinatorLeases.LEASE.toNanos());
        var holder = pumpConsumer(withLeases(bucket, nanos, "pod-a"));
        var waiting = pumpConsumer(withLeases(bucket, nanos, "pod-b"));
        waiting.pumpRetryMillis = 1;
        var subscription = mock(io.nats.client.JetStreamSubscription.class);

        assertTrue(holder.pumping(subscription), "the holder keeps pulling");
        assertFalse(waiting.pumping(subscription), "the other replica does not");

        org.mockito.Mockito.verify(subscription, org.mockito.Mockito.times(1)).unsubscribe();
    }

    // --- helpers ---

    private DiscussionOrchestrator withLeases(FakeLeaseBucket bucket, AtomicLong nanos, String pod) throws Exception {
        build(false);
        var orch = orchestrator;
        var leases = new CoordinatorLeases(bucket::storeFor, () -> "ns.crew.", pod, nanos::get,
                orch::threadIsOpen, orch::threadLost);
        orch.useResume(new CoordinatorResume(true, orch, new FakeBusReader(), () -> CrewScope.of("ns", "crew"), MAPPER,
                new DiscussionMetrics(new io.micrometer.core.instrument.simple.SimpleMeterRegistry()),
                System::currentTimeMillis, java.time.Duration.ZERO, leases));
        return orch;
    }

    private RequestQueueConsumer pumpConsumer(DiscussionOrchestrator orch) {
        var properties = mock(AgentProperties.class);
        var discuss = mock(AgentProperties.Discuss.class);
        when(properties.discuss()).thenReturn(discuss);
        when(discuss.coordinator()).thenReturn(true);
        when(properties.crew()).thenReturn(Optional.of("crew"));
        return new RequestQueueConsumer(natsProvider, orch, properties);
    }

    private String startMessage() throws Exception {
        var msg = MAPPER.createObjectNode();
        msg.put("messageId", "m-start");
        msg.put("threadId", "t1");
        msg.put("agentName", "human");
        msg.put("messageType", "thread_start");
        msg.put("content", "How many nodes?");
        msg.put("channel", "broadcast");
        msg.put("timestamp", Instant.now().toString());
        msg.set("metadata", MAPPER.readTree(START_META));
        return MAPPER.writeValueAsString(msg);
    }

    private static CoordinatorEvent event(String id, long seq) {
        var data = MAPPER.createObjectNode();
        data.put("discussSeq", seq);
        return new CoordinatorEvent("t/" + id, "thread_opened", "t", 0, Instant.now(), "coordinator", data);
    }
}
