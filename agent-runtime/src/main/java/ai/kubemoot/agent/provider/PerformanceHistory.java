package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.nats.KvKeys;
import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.Executor;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

/**
 * The scheduler's record of what it observed, in the NATS KV bucket {@value #BUCKET}:
 * for each provider and model, the recent model requests (tokens, times, outcome,
 * cold start, load on the provider) and the waits for capacity that ended there.
 *
 * <p>The accelerator-mcp server writes calibration samples to the same bucket in
 * the same format (bus schema SchedulerPerformance), as kind {@code calibration}.</p>
 *
 * <p>Each agent writes only its own key, {@code <provider>.<model>.<agent>}, so
 * writes need no compare-and-set; a reader folds every agent's key for a provider
 * and model. Each key keeps the last {@value #MAX_SAMPLES} requests and
 * {@value #MAX_WAITS} waits, so the history is bounded, and the bucket's TTL
 * removes keys for providers, models, or agents that are gone.</p>
 *
 * <p>Recording never blocks or fails a call: writes run on one background thread
 * with a bounded queue, and are dropped (and counted) when the queue is full or NATS
 * is down. That one thread is the only writer, so the in-memory entries need no
 * locking; they hold one entry per provider and model this agent has used.</p>
 */
@ApplicationScoped
public class PerformanceHistory {

    private static final Logger log = LoggerFactory.getLogger(PerformanceHistory.class);

    /** NATS KV bucket name; created by the operator's nats-streams-job. */
    public static final String BUCKET = "kubemoot_scheduler_performance";

    static final int MAX_SAMPLES = 32;
    static final int MAX_WAITS = 16;
    private static final int QUEUE_CAPACITY = 256;

    static final String F_SAMPLES = "samples";
    static final String F_WAITS = "waits";
    static final String F_REQUESTS = "requests";
    static final String F_FAILURES = "failures";

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper mapper;
    private final String agentId;
    private final Executor writer;
    /** This agent's entries, by key; loaded from the bucket on first use after a restart. Writer thread only. */
    private final Map<String, ObjectNode> entries = new HashMap<>();
    private final AtomicLong dropped = new AtomicLong();
    /** Exports each recorded call and wait as metrics. */
    private final SchedulerMetrics metrics;

    @Inject
    public PerformanceHistory(NatsConnectionProvider natsProvider, ObjectMapper mapper, AgentProperties properties,
                              SchedulerMetrics metrics) {
        this(natsProvider, mapper, properties, null, metrics);
    }

    /** {@code writer} runs each write on one thread at a time; null uses the background writer. */
    PerformanceHistory(NatsConnectionProvider natsProvider, ObjectMapper mapper, AgentProperties properties,
                       Executor writer, SchedulerMetrics metrics) {
        this.metrics = metrics;
        this.natsProvider = natsProvider;
        this.mapper = mapper;
        this.agentId = properties.namespace().orElse("") + "/" + properties.agentName();
        this.writer = writer != null ? writer : backgroundWriter();
    }

    private Executor backgroundWriter() {
        return new ThreadPoolExecutor(1, 1, 0L, TimeUnit.MILLISECONDS,
                new ArrayBlockingQueue<>(QUEUE_CAPACITY), r -> {
                    Thread t = new Thread(r, "performance-history");
                    t.setDaemon(true);
                    return t;
                }, (r, pool) -> drop("the write queue is full"));
    }

    /** Writes dropped because the queue was full or NATS was down. */
    public long droppedWrites() {
        return dropped.get();
    }

    private void drop(String why) {
        long n = dropped.incrementAndGet();
        log.debug("Dropped a performance history write ({}); {} dropped so far", why, n);
    }

    // ---- writes ----

    /** Records one model request; ignored when it names no provider or model. */
    public void record(CallObservation o) {
        if (o == null || !o.attributable()) {
            return;
        }
        metrics.call(o);
        writer.execute(() -> append(o.provider(), o.model(), F_SAMPLES, sample(o), MAX_SAMPLES, !o.ok()));
    }

    /**
     * Records that a call waited {@code waitMs} for capacity for {@code model} and was
     * not placed: the thread ended, or nothing could take it. Only a metric; the
     * history keeps the waits of placed calls.
     */
    public void recordUnplacedWait(String model, long waitMs) {
        metrics.waitedUnplaced(model, waitMs);
    }

    /** Records that a call waited {@code waitMs} for capacity before it was placed on {@code provider}. */
    public void recordWait(String provider, String model, long waitMs) {
        if (waitMs < 0 || !CallObservation.names(provider, model)) {
            return;
        }
        metrics.waited(provider, model, waitMs);
        ObjectNode w = mapper.createObjectNode();
        w.put("at", System.currentTimeMillis() - waitMs);
        w.put("ms", waitMs);
        writer.execute(() -> append(provider, model, F_WAITS, w, MAX_WAITS, false));
    }

    private ObjectNode sample(CallObservation o) {
        ObjectNode s = mapper.createObjectNode();
        s.put("at", o.atMs());
        s.put("kind", o.kind());
        s.put("ok", o.ok());
        s.put("coldStart", o.coldStart());
        s.put("promptTokens", o.promptTokens());
        s.put("outputTokens", o.outputTokens());
        s.put("durationMs", o.durationMs());
        s.put("loadMs", o.loadMs());
        s.put("promptEvalMs", o.promptEvalMs());
        s.put("evalMs", o.evalMs());
        s.put("activeCalls", o.activeCalls());
        return s;
    }

    private void append(String provider, String model, String field, ObjectNode item, int max, boolean failed) {
        try {
            String key = key(provider, model, agentId);
            KeyValue kv = bucket();
            ObjectNode entry = kv == null ? null : entry(kv, key, provider, model);
            if (entry == null) {
                drop("NATS is unavailable");
                return;
            }
            ArrayNode list = (ArrayNode) entry.get(field);
            list.add(item);
            while (list.size() > max) {
                list.remove(0);
            }
            if (F_SAMPLES.equals(field)) {
                entry.put(F_REQUESTS, entry.path(F_REQUESTS).asLong(0) + 1);
                entry.put(F_FAILURES, entry.path(F_FAILURES).asLong(0) + (failed ? 1 : 0));
            }
            entry.put("updatedAt", System.currentTimeMillis());
            put(kv, key, entry);
        } catch (RuntimeException e) {
            log.warn("Recording performance for {} on {} failed: {}", model, provider, e.getMessage());
        }
    }

    /**
     * This agent's entry for {@code key}: the cached one, else the stored one, else a
     * new one. Null when the bucket could not be read, so a stored history is never
     * replaced by an empty one.
     */
    private ObjectNode entry(KeyValue kv, String key, String provider, String model) {
        ObjectNode cached = entries.get(key);
        if (cached != null) {
            return cached;
        }
        ObjectNode loaded;
        try {
            var stored = kv.get(key);
            loaded = stored == null || stored.getValue() == null ? null : usable(mapper.readTree(stored.getValue()));
        } catch (Exception e) {
            log.debug("Reading {} from {} failed: {}", key, BUCKET, e.getMessage());
            return null;
        }
        ObjectNode entry = loaded != null ? loaded : fresh(provider, model);
        entries.put(key, entry);
        return entry;
    }

    /** {@code stored} when it is an entry this class can extend, else null. */
    private static ObjectNode usable(JsonNode stored) {
        if (stored instanceof ObjectNode o && o.path(F_SAMPLES).isArray() && o.path(F_WAITS).isArray()) {
            return o;
        }
        return null;
    }

    private ObjectNode fresh(String provider, String model) {
        ObjectNode n = mapper.createObjectNode();
        n.put("provider", provider);
        n.put("model", model);
        n.put("agent", agentId);
        n.putArray(F_SAMPLES);
        n.putArray(F_WAITS);
        return n;
    }

    // ---- reads ----

    /** Every agent's entry for {@code provider} and {@code model}; empty when none or NATS is down. */
    public List<JsonNode> entriesFor(String provider, String model) {
        KeyValue kv = bucket();
        List<JsonNode> out = new ArrayList<>();
        if (kv == null) {
            return out;
        }
        String prefix = ModelKeys.token(provider) + "." + ModelKeys.token(model) + ".";
        try {
            for (String key : kv.keys()) {
                if (key.startsWith(prefix)) {
                    JsonNode n = get(kv, key);
                    if (n != null) {
                        out.add(n);
                    }
                }
            }
        } catch (Exception e) {
            log.debug("Reading {} failed: {}", BUCKET, e.getMessage());
        }
        return out;
    }

    // ---- KV ----

    /** The key for one agent's history of one model on one provider. */
    static String key(String provider, String model, String agentId) {
        return ModelKeys.token(provider) + "." + ModelKeys.token(model) + "." + ModelKeys.token(agentId);
    }

    private KeyValue bucket() {
        Connection conn = natsProvider == null ? null : natsProvider.getConnection();
        if (conn == null) {
            return null;
        }
        try {
            return conn.keyValue(BUCKET);
        } catch (Exception e) {
            log.debug("Performance bucket {} unavailable: {}", BUCKET, e.getMessage());
            return null;
        }
    }

    private void put(KeyValue kv, String key, ObjectNode value) {
        if (kv == null) {
            return;
        }
        try {
            kv.put(key, mapper.writeValueAsBytes(value));
        } catch (Exception e) {
            drop("the write failed");
            KvKeys.warnWriteFailure(log, BUCKET, key, e);
        }
    }

    private JsonNode get(KeyValue kv, String key) {
        if (kv == null) {
            return null;
        }
        try {
            var entry = kv.get(key);
            return entry == null || entry.getValue() == null ? null : mapper.readTree(entry.getValue());
        } catch (Exception e) {
            return null;
        }
    }
}
