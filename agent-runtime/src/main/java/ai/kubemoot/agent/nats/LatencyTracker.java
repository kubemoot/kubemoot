package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.enterprise.context.ApplicationScoped;
import org.eclipse.microprofile.config.inject.ConfigProperty;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

/**
 * Self-calibrating latency tracker for discussion evaluation deadlines.
 *
 * Maintains a rolling P90 response time per agent+provider pair, stored in NATS KV
 * (kubemoot_latency bucket, no TTL). Used by DiscussionOrchestrator to set realistic
 * deadlines when an evaluating signal arrives — rather than using fixed heuristics.
 *
 * Key format: latency.<namespace>.<agentName>.<provider> (see {@link CrewScope#latencyKey}).
 *
 * Cold start: when fewer than 3 observations exist, returns KUBEMOOT_EVAL_DEFAULT_SECONDS
 * (default 90s) — generous on purpose to avoid cutting off agents early.
 *
 * Provider-keyed: same agent on different hardware (ollama-gpu vs ollama-rig1) gets
 * separate windows so scheduler migrations don't corrupt estimates.
 */
@ApplicationScoped
public class LatencyTracker {

    private static final Logger log = LoggerFactory.getLogger(LatencyTracker.class);
    private static final ObjectMapper mapper = new ObjectMapper();
    static final int MIN_SAMPLES_FOR_P90 = 3;

    private final NatsConnectionProvider natsProvider;
    private final int defaultSeconds;
    private final int windowSize;
    private final String kvBucket;

    public LatencyTracker(
            NatsConnectionProvider natsProvider,
            @ConfigProperty(name = "kubemoot.eval.default-seconds", defaultValue = "90") int defaultSeconds,
            @ConfigProperty(name = "kubemoot.eval.window-size", defaultValue = "20") int windowSize,
            @ConfigProperty(name = "kubemoot.latency.kv-bucket", defaultValue = "kubemoot_latency") String kvBucket
    ) {
        this.natsProvider = natsProvider;
        this.defaultSeconds = defaultSeconds;
        this.windowSize = windowSize;
        this.kvBucket = kvBucket;
    }

    /**
     * Record the wall-clock duration from triage pass to terminal signal for this
     * agent+provider pair. Called by the orchestrator after each terminal signal.
     *
     * @param agentName  the contributing agent
     * @param provider   GPU/provider label (e.g., "RTX 5090", "RTX 4090") from signal metadata
     * @param durationMs wall-clock milliseconds from evaluation start to terminal signal
     */
    public void recordLatency(String agentName, String provider, long durationMs) {
        if (agentName == null || agentName.isEmpty()) return;
        String effectiveProvider = provider != null && !provider.isEmpty() ? provider : "unknown";
        try {
            String key = natsProvider.scope().latencyKey(agentName, effectiveProvider);
            AgentLatency latency = loadOrCreate(key);
            latency.add(durationMs, windowSize);
            persist(key, latency);
            log.debug("Recorded latency for {}/{}: {}ms (window={}, p90={}ms)",
                    agentName, effectiveProvider, durationMs, latency.sampleCount(), (long) latency.p90());
        } catch (Exception e) {
            log.warn("Failed to record latency for {}/{}: {}", agentName, effectiveProvider, e.getMessage());
        }
    }

    /**
     * Return the P90 expected duration in seconds for this agent+provider pair.
     * Falls back to defaultSeconds when fewer than MIN_SAMPLES_FOR_P90 observations exist.
     *
     * @param agentName the contributing agent
     * @param provider  GPU/provider label from signal metadata
     * @return expected seconds (P90), or defaultSeconds for cold start
     */
    public int getExpectedSeconds(String agentName, String provider) {
        if (agentName == null || agentName.isEmpty()) return defaultSeconds;
        String effectiveProvider = provider != null && !provider.isEmpty() ? provider : "unknown";
        try {
            String key = natsProvider.scope().latencyKey(agentName, effectiveProvider);
            AgentLatency latency = load(key);
            if (latency == null || latency.sampleCount() < MIN_SAMPLES_FOR_P90) {
                log.debug("Cold start for {}/{} (samples={}) — using default {}s",
                        agentName, effectiveProvider,
                        latency != null ? latency.sampleCount() : 0,
                        defaultSeconds);
                return defaultSeconds;
            }
            int seconds = (int) Math.ceil(latency.p90() / 1000.0);
            log.debug("P90 for {}/{}: {}s (from {} samples)", agentName, effectiveProvider, seconds, latency.sampleCount());
            return seconds;
        } catch (Exception e) {
            log.warn("Failed to load latency for {}/{}: {} — using default", agentName, effectiveProvider, e.getMessage());
            return defaultSeconds;
        }
    }

    // --- NATS KV I/O ---

    private AgentLatency loadOrCreate(String key) {
        AgentLatency existing = load(key);
        return existing != null ? existing : new AgentLatency();
    }

    AgentLatency load(String key) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return null;

            var kv = conn.keyValue(kvBucket);
            var entry = kv.get(key);
            if (entry == null || entry.getValue() == null) return null;

            return mapper.readValue(entry.getValue(), AgentLatency.class);
        } catch (Exception e) {
            log.debug("No existing latency entry for key {}: {}", key, e.getMessage());
            return null;
        }
    }

    private void persist(String key, AgentLatency latency) {
        try {
            var conn = natsProvider.getConnection();
            if (conn == null) return;

            var kv = conn.keyValue(kvBucket);
            kv.put(key, mapper.writeValueAsBytes(latency));
        } catch (Exception e) {
            log.warn("Failed to persist latency entry {}: {}", key, e.getMessage());
        }
    }

    // --- Data model ---

    /**
     * Sliding window of recent duration observations with P90 calculation.
     * Serialized as JSON into NATS KV — no external dependencies.
     *
     * Package-private for testability.
     */
    static class AgentLatency {

        // Observations in milliseconds, ordered oldest-to-newest.
        private List<Long> samples = new ArrayList<>();

        public List<Long> getSamples() {
            return samples;
        }

        public void setSamples(List<Long> samples) {
            this.samples = samples;
        }

        /** Add a new observation, evicting the oldest if window is full. */
        void add(long durationMs, int windowSize) {
            samples.add(durationMs);
            while (samples.size() > windowSize) {
                samples.removeFirst();
            }
        }

        int sampleCount() {
            return samples.size();
        }

        /**
         * P90 of the current window.
         * Sorts a copy of the samples and takes the 90th-percentile index.
         */
        double p90() {
            if (samples.isEmpty()) return 0;
            long[] sorted = samples.stream().mapToLong(Long::longValue).sorted().toArray();
            // Index formula: ceil(p * n) - 1, clamped to valid range.
            int idx = (int) Math.min(Math.ceil(0.9 * sorted.length) - 1, (double) sorted.length - 1);
            idx = Math.max(0, idx);
            return sorted[idx];
        }
    }
}
