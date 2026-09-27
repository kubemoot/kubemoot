package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.config.AgentProperties;
import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicReference;

/**
 * The shared view of what every crew's agents are using, requesting, or about
 * to request, in the NATS KV bucket {@value #BUCKET}. Keys are namespace-neutral
 * per model (models are shared across crews); agent ids carry the namespace.
 *
 * <ul>
 *   <li>{@code wait.<model>.<agent>}: the agent is waiting for a GPU for the model.
 *       Written when a wait starts, deleted when it ends.</li>
 *   <li>{@code intent.<model>.<agent>}: the agent was selected for a live thread and
 *       names the model as a candidate. Written at selection, deleted on the agent's
 *       first call or when the thread ends; {@link #INTENT_TTL} is the safety net.</li>
 *   <li>{@code use.<model>}: a decaying count of call starts and the last use time.
 *       Written on every scheduled call start, last writer wins.</li>
 *   <li>{@code sel.<crew>.<agent>}: how often the crew selects the agent (decaying)
 *       and the agent's candidate models. Feeds prediction only; it never causes a load.</li>
 * </ul>
 *
 * <p>Every entry carries {@code expiresAt}; readers ignore expired entries and the
 * bucket's TTL removes them. One key per model and agent keeps writes CAS-free
 * and self-correcting.</p>
 */
@ApplicationScoped
public class DemandBoard {

    private static final Logger log = LoggerFactory.getLogger(DemandBoard.class);

    /** NATS KV bucket name; created by the operator's nats-streams-job. */
    public static final String BUCKET = "kubemoot_model_demand";

    static final Duration INTENT_TTL = Duration.ofMinutes(3);
    static final Duration WAIT_TTL = Duration.ofMinutes(10);
    static final Duration USE_TTL = Duration.ofHours(6);
    /** Half-life of the recent-use rate and the selection frequency. */
    public static final Duration USE_HALF_LIFE = Duration.ofMinutes(10);
    private static final Duration CACHE_TTL = Duration.ofMillis(200);

    static final String KIND_WAIT = "wait";
    static final String KIND_INTENT = "intent";
    static final String KIND_USE = "use";
    static final String KIND_SEL = "sel";

    private static final String F_KIND = "kind";
    private static final String F_MODEL = "model";
    private static final String F_MODELS = "models";
    private static final String F_RATE = "rate";
    private static final String F_UPDATED = "updatedAt";
    private static final String F_LAST_USED = "lastUsedAt";
    private static final String F_EXPIRES = "expiresAt";

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper mapper;
    private final String agentId;
    private final String crew;
    /** Intent keys this agent holds, per thread, so clearing needs no scan. */
    private final Map<String, List<String>> intentKeysByThread = new ConcurrentHashMap<>();
    private final AtomicReference<Cached> cache = new AtomicReference<>();

    @Inject
    public DemandBoard(NatsConnectionProvider natsProvider, ObjectMapper mapper, AgentProperties properties) {
        this.natsProvider = natsProvider;
        this.mapper = mapper;
        String ns = properties.namespace().orElse("");
        this.agentId = ns + "/" + properties.agentName();
        this.crew = ns + "/" + properties.crew().orElse("");
    }

    // ---- writes ----

    /** Records that this agent, selected for {@code threadId}, is about to request one of {@code models}. */
    public void intend(String threadId, List<String> models) {
        long expires = System.currentTimeMillis() + INTENT_TTL.toMillis();
        List<String> keys = new ArrayList<>();
        for (String model : models) {
            String key = KIND_INTENT + "." + ModelKeys.token(model) + "." + ModelKeys.token(agentId);
            ObjectNode n = entry(KIND_INTENT, model, expires);
            n.put("thread", threadId);
            if (put(key, n)) {
                keys.add(key);
            }
        }
        intentKeysByThread.put(threadId, keys);
    }

    /** Clears this agent's intents for {@code threadId}: its first call started or the thread ended. */
    public void clearIntents(String threadId) {
        List<String> keys = intentKeysByThread.remove(threadId);
        if (keys != null) {
            keys.forEach(this::delete);
        }
    }

    /** Records that this agent is waiting for a GPU for {@code model}. */
    public void waitStarted(String model) {
        put(waitKey(model), entry(KIND_WAIT, model, System.currentTimeMillis() + WAIT_TTL.toMillis()));
    }

    /** Clears this agent's wait for {@code model}. */
    public void waitEnded(String model) {
        delete(waitKey(model));
    }

    /** Records a call start for {@code model}: advances its decaying rate and last-use time. */
    public void recordUse(String model) {
        String key = KIND_USE + "." + ModelKeys.token(model);
        long now = System.currentTimeMillis();
        JsonNode prev = get(key);
        double rate = decayed(prev == null ? 0.0 : prev.path(F_RATE).asDouble(0.0),
                prev == null ? now : prev.path(F_UPDATED).asLong(now), now) + 1.0;
        ObjectNode n = entry(KIND_USE, model, now + USE_TTL.toMillis());
        n.put(F_RATE, rate);
        n.put(F_UPDATED, now);
        n.put(F_LAST_USED, now);
        put(key, n);
    }

    /** Records that this agent's crew selected it, naming its candidate models (prediction only). */
    public void recordSelection(List<String> models) {
        String key = KIND_SEL + "." + ModelKeys.token(crew) + "." + ModelKeys.token(agentId);
        long now = System.currentTimeMillis();
        JsonNode prev = get(key);
        double rate = decayed(prev == null ? 0.0 : prev.path(F_RATE).asDouble(0.0),
                prev == null ? now : prev.path(F_UPDATED).asLong(now), now) + 1.0;
        ObjectNode n = mapper.createObjectNode();
        n.put(F_KIND, KIND_SEL);
        n.put(F_RATE, rate);
        n.put(F_UPDATED, now);
        n.put(F_EXPIRES, now + USE_TTL.toMillis());
        var arr = n.putArray(F_MODELS);
        models.forEach(arr::add);
        put(key, n);
    }

    // ---- reads ----

    /** Current demand per model across every crew; empty when the bucket is unavailable. */
    public Map<String, ModelDemand> snapshot() {
        Cached c = cache.get();
        if (c != null && System.nanoTime() - c.atNanos < CACHE_TTL.toNanos()) {
            return c.demand;
        }
        Map<String, ModelDemand> demand = aggregate(readAll(), System.currentTimeMillis());
        cache.set(new Cached(demand, System.nanoTime()));
        return demand;
    }

    /**
     * Folds demand entries into per-model demand at {@code nowMs}. Expired entries
     * are ignored; rates are decayed to now. Pure, for testing.
     */
    static Map<String, ModelDemand> aggregate(List<JsonNode> entries, long nowMs) {
        Map<String, MutableDemand> acc = new HashMap<>();
        for (JsonNode n : entries) {
            if (n.path(F_EXPIRES).asLong(Long.MAX_VALUE) >= nowMs) {
                fold(n, nowMs, acc);
            }
        }
        Map<String, ModelDemand> out = new HashMap<>();
        acc.forEach((model, d) -> out.put(model, d.freeze()));
        return out;
    }

    private static void fold(JsonNode n, long nowMs, Map<String, MutableDemand> acc) {
        String kind = n.path(F_KIND).asText("");
        switch (kind) {
            case KIND_WAIT -> acc.computeIfAbsent(n.path(F_MODEL).asText(""), k -> new MutableDemand()).waiters++;
            case KIND_INTENT -> acc.computeIfAbsent(n.path(F_MODEL).asText(""), k -> new MutableDemand()).intents++;
            case KIND_USE -> foldUse(n, nowMs, acc);
            case KIND_SEL -> foldSelection(n, nowMs, acc);
            default -> { /* unknown kinds are ignored */ }
        }
    }

    private static void foldUse(JsonNode n, long nowMs, Map<String, MutableDemand> acc) {
        MutableDemand d = acc.computeIfAbsent(n.path(F_MODEL).asText(""), k -> new MutableDemand());
        d.useRate += decayed(n.path(F_RATE).asDouble(0.0), n.path(F_UPDATED).asLong(nowMs), nowMs);
        d.lastUsedMs = Math.max(d.lastUsedMs, n.path(F_LAST_USED).asLong(0L));
    }

    private static void foldSelection(JsonNode n, long nowMs, Map<String, MutableDemand> acc) {
        double rate = decayed(n.path(F_RATE).asDouble(0.0), n.path(F_UPDATED).asLong(nowMs), nowMs);
        n.path(F_MODELS).forEach(m -> acc.computeIfAbsent(m.asText(""), k -> new MutableDemand()).predicted += rate);
    }

    /** {@code rate} decayed from {@code thenMs} to {@code nowMs} with half-life {@link #USE_HALF_LIFE}. */
    static double decayed(double rate, long thenMs, long nowMs) {
        long dt = Math.max(0L, nowMs - thenMs);
        return rate * Math.pow(0.5, dt / (double) USE_HALF_LIFE.toMillis());
    }

    // ---- KV plumbing ----

    private String waitKey(String model) {
        return KIND_WAIT + "." + ModelKeys.token(model) + "." + ModelKeys.token(agentId);
    }

    private ObjectNode entry(String kind, String model, long expiresAt) {
        ObjectNode n = mapper.createObjectNode();
        n.put(F_KIND, kind);
        n.put(F_MODEL, model);
        n.put("agent", agentId);
        n.put(F_EXPIRES, expiresAt);
        return n;
    }

    private KeyValue bucket() {
        Connection conn = natsProvider == null ? null : natsProvider.getConnection();
        if (conn == null) {
            return null;
        }
        try {
            return conn.keyValue(BUCKET);
        } catch (Exception e) {
            log.debug("Demand bucket {} unavailable: {}", BUCKET, e.getMessage());
            return null;
        }
    }

    private boolean put(String key, ObjectNode value) {
        KeyValue kv = bucket();
        if (kv == null) {
            return false;
        }
        try {
            kv.put(key, mapper.writeValueAsBytes(value));
            cache.set(null);
            return true;
        } catch (Exception e) {
            log.debug("Demand write {} failed: {}", key, e.getMessage());
            return false;
        }
    }

    private void delete(String key) {
        KeyValue kv = bucket();
        if (kv == null) {
            return;
        }
        try {
            kv.delete(key);
            cache.set(null);
        } catch (Exception e) {
            log.debug("Demand delete {} failed (TTL cleans up): {}", key, e.getMessage());
        }
    }

    private JsonNode get(String key) {
        KeyValue kv = bucket();
        return kv == null ? null : get(kv, key);
    }

    private JsonNode get(KeyValue kv, String key) {
        try {
            var entry = kv.get(key);
            return entry == null || entry.getValue() == null ? null : mapper.readTree(entry.getValue());
        } catch (Exception e) {
            return null;
        }
    }

    private List<JsonNode> readAll() {
        KeyValue kv = bucket();
        List<JsonNode> out = new ArrayList<>();
        if (kv == null) {
            return out;
        }
        try {
            for (String key : kv.keys()) {
                JsonNode n = get(kv, key);
                if (n != null) {
                    out.add(n);
                }
            }
        } catch (Exception e) {
            log.debug("Reading demand bucket failed: {}", e.getMessage());
        }
        return out;
    }

    private static final class MutableDemand {
        int waiters;
        int intents;
        double useRate;
        double predicted;
        long lastUsedMs;

        ModelDemand freeze() {
            return new ModelDemand(waiters, intents, useRate, predicted, lastUsedMs);
        }
    }

    private record Cached(Map<String, ModelDemand> demand, long atNanos) {}
}
