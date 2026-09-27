package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.LatencyTracker.AgentLatency;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import io.nats.client.api.KeyValueEntry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

/**
 * Plain JUnit tests for LatencyTracker.
 *
 * Tests cover:
 * - P90 calculation correctness and index boundary behaviour
 * - Cold start default when fewer than MIN_SAMPLES observations exist
 * - Sliding window eviction (oldest observation removed when window is full)
 * - Provider-keyed separation (different keys per agent+provider pair)
 * - NATS KV unavailability — graceful no-op, fallback to default
 */
class LatencyTrackerTest {

    private NatsConnectionProvider natsProvider;
    private Connection conn;
    private KeyValue kv;
    private LatencyTracker tracker;

    private static final int DEFAULT_SECONDS = 90;
    private static final int WINDOW_SIZE = 5; // small window for test clarity
    private static final String BUCKET = "kubemoot_agent_state";

    @BeforeEach
    void setUp() throws Exception {
        natsProvider = mock(NatsConnectionProvider.class);
        conn = mock(Connection.class);
        kv = mock(KeyValue.class);
        when(natsProvider.getConnection()).thenReturn(conn);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-a", "homelab-pilot"));
        when(conn.keyValue(BUCKET)).thenReturn(kv);

        tracker = new LatencyTracker(natsProvider, DEFAULT_SECONDS, WINDOW_SIZE, BUCKET);
    }

    // --- AgentLatency unit tests (pure math, no NATS) ---

    @Test
    void agentLatency_p90_singleSample() {
        var latency = new AgentLatency();
        latency.add(5000L, 20);
        assertEquals(5000.0, latency.p90(), 0.001);
    }

    @Test
    void agentLatency_p90_tenSamples() {
        var latency = new AgentLatency();
        // Add 10 values: 1000, 2000, ..., 10000 ms
        for (int i = 1; i <= 10; i++) {
            latency.add(i * 1000L, 20);
        }
        // P90 of {1000..10000}: ceil(0.9 * 10) - 1 = 9 - 1 = 8 → sorted[8] = 9000
        assertEquals(9000.0, latency.p90(), 0.001);
    }

    @Test
    void agentLatency_p90_emptyReturnsZero() {
        var latency = new AgentLatency();
        assertEquals(0.0, latency.p90(), 0.001);
    }

    @Test
    void agentLatency_p90_twoSamples() {
        var latency = new AgentLatency();
        latency.add(1000L, 20);
        latency.add(9000L, 20);
        // ceil(0.9 * 2) - 1 = 2 - 1 = 1 → sorted[1] = 9000
        assertEquals(9000.0, latency.p90(), 0.001);
    }

    @Test
    void agentLatency_slidingWindow_evictsOldest() {
        var latency = new AgentLatency();
        int window = 3;
        latency.add(1000L, window);
        latency.add(2000L, window);
        latency.add(3000L, window);
        // Window full — adding 100 evicts 1000
        latency.add(100L, window);
        assertEquals(3, latency.sampleCount());
        // Remaining samples: 2000, 3000, 100 — sorted: 100, 2000, 3000
        // P90: ceil(0.9*3) - 1 = 3-1 = 2 → sorted[2] = 3000
        assertEquals(3000.0, latency.p90(), 0.001);
    }

    @Test
    void agentLatency_sampleCount_tracksCorrectly() {
        var latency = new AgentLatency();
        assertEquals(0, latency.sampleCount());
        latency.add(1000L, 5);
        assertEquals(1, latency.sampleCount());
        latency.add(2000L, 5);
        latency.add(3000L, 5);
        latency.add(4000L, 5);
        latency.add(5000L, 5);
        assertEquals(5, latency.sampleCount());
        // 6th add evicts — still 5
        latency.add(6000L, 5);
        assertEquals(5, latency.sampleCount());
    }

    // --- LatencyTracker cold start ---

    @Test
    void getExpectedSeconds_coldStart_returnsDefault() throws Exception {
        when(kv.get(anyString())).thenReturn(null);
        int result = tracker.getExpectedSeconds("k8sgpt", "RTX 5090");
        assertEquals(DEFAULT_SECONDS, result);
    }

    @Test
    void getExpectedSeconds_fewerThanMinSamples_returnsDefault() throws Exception {
        // Only 2 observations — below MIN_SAMPLES_FOR_P90 (3)
        var latency = new AgentLatency();
        latency.add(10000L, WINDOW_SIZE);
        latency.add(20000L, WINDOW_SIZE);

        var entry = mockKvEntry(latency);
        when(kv.get("latency.ns-a.k8sgpt.RTX_5090")).thenReturn(entry);

        int result = tracker.getExpectedSeconds("k8sgpt", "RTX 5090");
        assertEquals(DEFAULT_SECONDS, result);
    }

    @Test
    void getExpectedSeconds_sufficientHistory_returnsP90Seconds() throws Exception {
        // 5 samples: 5s, 10s, 15s, 20s, 25s (in ms)
        var latency = new AgentLatency();
        latency.add(5_000L, WINDOW_SIZE);
        latency.add(10_000L, WINDOW_SIZE);
        latency.add(15_000L, WINDOW_SIZE);
        latency.add(20_000L, WINDOW_SIZE);
        latency.add(25_000L, WINDOW_SIZE);

        var entry = mockKvEntry(latency);
        when(kv.get("latency.ns-a.k8sgpt.RTX_5090")).thenReturn(entry);

        int result = tracker.getExpectedSeconds("k8sgpt", "RTX 5090");
        // P90 of {5000,10000,15000,20000,25000}: ceil(0.9*5)-1 = 5-1=4 → sorted[4]=25000ms → 25s
        assertEquals(25, result);
    }

    @Test
    void getExpectedSeconds_nullAgentName_returnsDefault() {
        int result = tracker.getExpectedSeconds(null, "RTX 5090");
        assertEquals(DEFAULT_SECONDS, result);
    }

    @Test
    void getExpectedSeconds_natsUnavailable_returnsDefault() {
        when(natsProvider.getConnection()).thenReturn(null);
        int result = tracker.getExpectedSeconds("k8sgpt", "RTX 5090");
        assertEquals(DEFAULT_SECONDS, result);
    }

    @Test
    void getExpectedSeconds_natsException_returnsDefault() throws Exception {
        when(kv.get(anyString())).thenThrow(new RuntimeException("NATS down"));
        int result = tracker.getExpectedSeconds("k8sgpt", "RTX 5090");
        assertEquals(DEFAULT_SECONDS, result);
    }

    // --- LatencyTracker recordLatency ---

    @Test
    void recordLatency_persists_toKvBucket() throws Exception {
        when(kv.get(anyString())).thenReturn(null); // no prior history

        tracker.recordLatency("k8sgpt", "RTX 5090", 30_000L);

        verify(kv).put(eq("latency.ns-a.k8sgpt.RTX_5090"), any(byte[].class));
    }

    @Test
    void recordLatency_nullAgent_noOp() throws Exception {
        tracker.recordLatency(null, "RTX 5090", 30_000L);
        verify(kv, never()).put(anyString(), any(byte[].class));
    }

    @Test
    void recordLatency_natsUnavailable_noException() {
        when(natsProvider.getConnection()).thenReturn(null);
        // Should not throw
        assertDoesNotThrow(() -> tracker.recordLatency("k8sgpt", "RTX 5090", 30_000L));
    }

    // --- Provider-keyed separation ---

    @Test
    void providerKeyedSeparation_differentProvidersGetDifferentKeys() throws Exception {
        when(kv.get(anyString())).thenReturn(null);

        tracker.recordLatency("k8sgpt", "RTX 5090", 20_000L);
        tracker.recordLatency("k8sgpt", "RTX 4090", 60_000L);

        verify(kv).put(eq("latency.ns-a.k8sgpt.RTX_5090"), any(byte[].class));
        verify(kv).put(eq("latency.ns-a.k8sgpt.RTX_4090"), any(byte[].class));
    }

    @Test
    void latencyToken_replacesSpacesSlashesAndDots() {
        assertEquals("RTX_5090", CrewScope.latencyToken("RTX 5090"));
        assertEquals("RTX_4090", CrewScope.latencyToken("RTX 4090"));
        assertEquals("some_path_key", CrewScope.latencyToken("some/path/key"));
        assertEquals("a_b_c", CrewScope.latencyToken("a.b.c"));
    }

    @Test
    void recordLatency_sameAgentInAnotherNamespace_writesItsOwnKey() throws Exception {
        when(kv.get(anyString())).thenReturn(null);
        when(natsProvider.scope()).thenReturn(CrewScope.of("ns-b", "homelab-pilot"));

        tracker.recordLatency("k8sgpt", "RTX 5090", 20_000L);

        verify(kv).put(eq("latency.ns-b.k8sgpt.RTX_5090"), any(byte[].class));
        verify(kv, never()).put(eq("latency.ns-a.k8sgpt.RTX_5090"), any(byte[].class));
    }

    // --- Round-trip: record then query ---

    @Test
    void roundTrip_recordThenGetExpectedSeconds() throws Exception {
        // Set up tracker with real serialization round-trip using a mock that stores the value
        var storedValue = new byte[1][];
        when(kv.get(anyString())).thenAnswer(inv -> {
            if (storedValue[0] == null) return null;
            return mockKvEntryFromBytes(storedValue[0]);
        });
        doAnswer(inv -> {
            storedValue[0] = (byte[]) inv.getArgument(1);
            return 0L;
        }).when(kv).put(anyString(), any(byte[].class));

        // Need enough samples to clear cold-start threshold
        tracker.recordLatency("nginx", "RTX 4090", 30_000L);
        tracker.recordLatency("nginx", "RTX 4090", 40_000L);
        tracker.recordLatency("nginx", "RTX 4090", 50_000L);

        int seconds = tracker.getExpectedSeconds("nginx", "RTX 4090");
        // P90 of {30000, 40000, 50000}: ceil(0.9*3)-1 = 3-1=2 → sorted[2]=50000ms → 50s
        assertEquals(50, seconds);
    }

    // --- Helpers ---

    private KeyValueEntry mockKvEntry(AgentLatency latency) throws Exception {
        com.fasterxml.jackson.databind.ObjectMapper mapper = new com.fasterxml.jackson.databind.ObjectMapper();
        byte[] bytes = mapper.writeValueAsBytes(latency);
        return mockKvEntryFromBytes(bytes);
    }

    private KeyValueEntry mockKvEntryFromBytes(byte[] bytes) {
        var entry = mock(KeyValueEntry.class);
        when(entry.getValue()).thenReturn(bytes);
        return entry;
    }
}
