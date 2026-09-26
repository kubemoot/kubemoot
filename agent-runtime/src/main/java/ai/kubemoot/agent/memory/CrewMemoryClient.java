package ai.kubemoot.agent.memory;

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

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Crew working memory — per-cluster facts the crew learns at runtime and
 * recalls later, so it stops re-discovering what it already figured out.
 *
 * <p>Backed by the NATS KV bucket {@code kubemoot_crew_memory} (provisioned by
 * the operator's nats-streams-job). Keys are crew-scoped:
 * {@code <crew>.<topic>.<key>}. Values are JSON {@code {value, learnedBy,
 * learnedAt}}. NATS KV is the existing persistent-state backbone (provider
 * state, agent state) — it survives pod restarts and needs no PVC, unlike a
 * file-backed memory server. See tasks/notes/Crew Working Memory.md.
 *
 * <h3>Two access paths</h3>
 * <ul>
 *   <li><b>Read = auto-injection</b>: {@link #recallForContext()} returns the
 *       crew's known facts formatted for the system prompt. ChatService injects
 *       this every discussion query (RAG-style) so agents START already knowing
 *       what the crew learned — they don't have to choose to look it up.</li>
 *   <li><b>Write = structured directive</b>: agents emit a {@code REMEMBER:}
 *       line when they learn a durable fact (guided by ADL, the same convention
 *       as {@code TOOL_GAP:}). {@link #persistFromResponse} parses and stores
 *       them. LLM-driven, no separate component.</li>
 * </ul>
 *
 * <p>Native-safe: reads via {@code mapper.readTree()} (never {@code readValue}
 * of a record — fails silently in GraalVM native). Degrades to no-op when NATS
 * is unavailable, exactly like {@link ai.kubemoot.agent.provider.ProviderSelector}.
 */
@ApplicationScoped
public class CrewMemoryClient {

    private static final Logger log = LoggerFactory.getLogger(CrewMemoryClient.class);

    /** Must match the bucket the operator's nats-streams-job creates. */
    static final String MEMORY_BUCKET = "kubemoot_crew_memory";

    // JSON field names for the stored fact value {value, learnedBy, learnedAt, usedAt}.
    private static final String F_VALUE = "value";
    private static final String F_LEARNED_BY = "learnedBy";
    private static final String F_LEARNED_AT = "learnedAt";
    private static final String F_USED_AT = "usedAt";

    /**
     * On recall, a fact older than this gets its {@code usedAt} refreshed
     * (touch-on-read) so LRU eviction keeps frequently-used facts and drops
     * abandoned ones. Throttled so recall doesn't rewrite on every query.
     */
    private static final java.time.Duration TOUCH_AFTER = java.time.Duration.ofHours(6);

    // Max chars of a fact value echoed into conflict logs (longer values are ellipsized).
    private static final int LOG_VALUE_CHARS = 80;

    /**
     * REMEMBER: &lt;topic&gt; | &lt;key&gt; | &lt;value&gt;
     * Structured directive an agent emits to persist a learned fact. Topic and
     * key are short identifiers; value is free text. Same structured-output
     * convention as TOOL_GAP: / NOTHING_TO_ADD.
     */
    static final Pattern REMEMBER_LINE =
            Pattern.compile("(?m)^\\s*REMEMBER:\\s*([^|]+?)\\s*\\|\\s*([^|]+?)\\s*\\|\\s*(.+?)\\s*$");

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper objectMapper;
    private final String crew;
    // Declarative GC/injection policy from Crew.spec.memory (operator → env).
    private final boolean enabled;
    private final int maxFacts;
    private final int injectLimit;
    private final long ttlMillis;
    private final boolean verifyOnAdd;

    @Inject
    public CrewMemoryClient(NatsConnectionProvider natsProvider, ObjectMapper objectMapper,
                            AgentProperties properties) {
        this.natsProvider = natsProvider;
        this.objectMapper = objectMapper;
        this.crew = properties.crew().filter(s -> !s.isEmpty()).orElse("default");
        var mem = properties.memory();
        this.enabled = mem.enabled();
        this.maxFacts = mem.maxFacts();
        this.injectLimit = mem.injectLimit();
        this.ttlMillis = mem.ttlDays() * 24L * 60L * 60L * 1000L;
        this.verifyOnAdd = mem.verifyOnAdd();
    }

    /**
     * Recall the crew's facts RELEVANT to this query, formatted for system-prompt
     * injection. Relevance keeps injection small even when storage is large:
     * facts whose topic/key/value share a token with the query rank first; the
     * rest fill by recency. Capped at the crew's {@code injectLimit}. Empty when
     * memory is disabled/empty or NATS is down.
     *
     * @param query the user message (relevance signal); may be null/blank, then
     *              facts are selected purely by recency.
     */
    public String recallForContext(String query) {
        if (!enabled || injectLimit <= 0) return "";
        KeyValue kv = bucket();
        if (kv == null) return "";
        List<Fact> facts = readFacts(kv);
        if (facts.isEmpty()) return "";
        // Rank by query relevance first (facts sharing a token with the query),
        // then by recency. Keeps injection small + targeted even with large
        // storage — a GPU query surfaces GPU facts, not the whole memory.
        rankByRelevanceThenRecency(facts, tokens(query));
        var sb = new StringBuilder();
        sb.append("## Crew Working Memory (learned on this cluster — prefer over re-discovery)\n");
        sb.append("These facts were discovered by the crew on THIS cluster. Use them directly; ")
          .append("re-verify only if a fact looks stale or a query using it returns nothing.\n");
        appendInjectedFacts(sb, facts, kv);
        sb.append("\n");
        return sb.toString();
    }

    /**
     * Sort facts by query relevance (higher first), then by recency (most-recent
     * first). In-place; same ordering as the prior inline comparator.
     */
    private static void rankByRelevanceThenRecency(List<Fact> facts, java.util.Set<String> qTokens) {
        facts.sort((a, b) -> {
            int ra = relevance(a, qTokens);
            int rb = relevance(b, qTokens);
            if (ra != rb) return Integer.compare(rb, ra);          // higher relevance first
            return Long.compare(b.usedAtMillis, a.usedAtMillis);   // then most-recent
        });
    }

    /**
     * Append up to {@code injectLimit} facts to the system-prompt buffer, touching
     * each (throttled) so LRU keeps facts that are actually used.
     */
    private void appendInjectedFacts(StringBuilder sb, List<Fact> facts, KeyValue kv) {
        long now = System.currentTimeMillis();
        int limit = Math.min(injectLimit, facts.size());
        for (int i = 0; i < limit; i++) {
            Fact f = facts.get(i);
            sb.append("- [").append(f.topic).append("] ").append(f.key)
              .append(" = ").append(f.value).append("\n");
            // Touch-on-read (throttled): refresh usedAt so LRU keeps facts that
            // are actually used and evicts abandoned ones.
            if (now - f.usedAtMillis > TOUCH_AFTER.toMillis()) {
                touch(kv, f);
            }
        }
    }

    /** Lowercased word tokens (len ≥ 3) of a string, for cheap relevance overlap. */
    static java.util.Set<String> tokens(String s) {
        var set = new java.util.HashSet<String>();
        if (s == null) return set;
        for (String t : s.toLowerCase().split("[^a-z0-9]+")) {
            if (t.length() >= 3) set.add(t);
        }
        return set;
    }

    /** Count of query tokens appearing in a fact's topic/key/value. 0 = not relevant. */
    private static int relevance(Fact f, java.util.Set<String> qTokens) {
        if (qTokens.isEmpty()) return 0;
        String hay = (f.topic + " " + f.key + " " + f.value).toLowerCase();
        int score = 0;
        for (String t : qTokens) {
            if (hay.contains(t)) score++;
        }
        return score;
    }

    /**
     * Parse and persist any {@code REMEMBER:} directives in an agent response.
     * Returns the response with those lines stripped (they are control
     * directives, not user-facing content). No-op when NATS is unavailable.
     */
    public String persistFromResponse(String response, String learnedBy) {
        if (response == null || response.isEmpty() || !response.contains("REMEMBER:")) {
            return response;
        }
        if (enabled) {
            Matcher m = REMEMBER_LINE.matcher(response);
            int count = 0;
            while (m.find()) {
                remember(m.group(1), m.group(2), m.group(3), learnedBy);
                count++;
            }
            if (count > 0) {
                log.info("Persisted {} crew-memory fact(s) from {}", count, learnedBy);
            }
        }
        // Always strip the directives from user-facing text, even if disabled.
        return REMEMBER_LINE.matcher(response).replaceAll("").strip();
    }

    /**
     * Write one fact. Crew-scoped key; value carries provenance. No-op if NATS
     * down or memory disabled. When {@code verifyOnAdd} is set, vets against the
     * existing same-key fact: identical value → touch only (dedup, no rewrite);
     * different value → supersede and log (conflict resolved by fresh discovery,
     * prior kept in KV history). Cross-key dedup/conflict is handled upstream by
     * the LLM, which sees relevant memory via injection.
     */
    public void remember(String topic, String key, String value, String learnedBy) {
        if (!enabled) return;
        KeyValue kv = bucket();
        if (kv == null) return;
        String nk = natsKey(topic, key);
        String newVal = value.strip();
        try {
            if (verifyOnAdd) {
                var existing = kv.get(nk);
                if (existing != null && existing.getValue() != null) {
                    JsonNode prev = objectMapper.readTree(existing.getValue());
                    String prevVal = prev.path(F_VALUE).asText("");
                    if (prevVal.equals(newVal)) {
                        // Duplicate — just refresh usage, don't rewrite.
                        touch(kv, new Fact(nk, topic, key, newVal,
                                prev.path(F_LEARNED_BY).asText(""), prev.path(F_LEARNED_AT).asText(""), 0L));
                        return;
                    }
                    log.info("Crew-memory conflict on {}: superseding {} -> {} (fresh discovery wins)",
                            nk, truncate(prevVal), truncate(newVal));
                }
            }
            String now = Instant.now().toString();
            ObjectNode v = objectMapper.createObjectNode();
            v.put(F_VALUE, newVal);
            v.put(F_LEARNED_BY, learnedBy == null ? "" : learnedBy);
            v.put(F_LEARNED_AT, now);
            v.put(F_USED_AT, now);
            kv.put(nk, objectMapper.writeValueAsBytes(v));
            enforceCap(kv);
        } catch (Exception e) {
            log.debug("Failed to remember {}/{}: {}", topic, key, e.getMessage());
        }
    }

    private static String truncate(String s) {
        if (s == null) return "";
        if (s.length() <= LOG_VALUE_CHARS) return s;
        return s.substring(0, LOG_VALUE_CHARS - 3) + "...";
    }

    // --- internals ---

    /** Refresh a fact's usedAt (LRU touch) without changing its value/provenance. */
    private void touch(KeyValue kv, Fact f) {
        try {
            ObjectNode v = objectMapper.createObjectNode();
            v.put(F_VALUE, f.value);
            v.put(F_LEARNED_BY, f.learnedBy);
            v.put(F_LEARNED_AT, f.learnedAt);
            v.put(F_USED_AT, Instant.now().toString());
            kv.put(f.natsKey, objectMapper.writeValueAsBytes(v));
        } catch (Exception e) {
            log.debug("Failed to touch crew-memory key {}: {}", f.natsKey, e.getMessage());
        }
    }

    /**
     * Evict least-recently-used facts when this crew exceeds {@code maxFacts}.
     * Bounds growth from runaway/varying keys so the bucket never leaks. Called
     * after each write (cheap for small N).
     */
    private void enforceCap(KeyValue kv) {
        List<Fact> facts = readFacts(kv);
        int over = facts.size() - maxFacts;
        if (over <= 0) return;
        facts.sort((a, b) -> Long.compare(a.usedAtMillis, b.usedAtMillis)); // oldest first
        for (int i = 0; i < over; i++) {
            try {
                kv.delete(facts.get(i).natsKey);
                log.info("Evicted LRU crew-memory fact {} (over cap by {})", facts.get(i).natsKey, over);
            } catch (Exception e) {
                log.debug("Failed to evict {}: {}", facts.get(i).natsKey, e.getMessage());
            }
        }
    }

    private List<Fact> readFacts(KeyValue kv) {
        List<Fact> facts = new ArrayList<>();
        if (kv == null) return facts;
        String prefix = sanitize(crew) + ".";
        long now = System.currentTimeMillis();
        try {
            for (String k : kv.keys()) {
                if (k.startsWith(prefix)) {
                    Fact f = readFact(kv, prefix, k, now);
                    if (f != null) facts.add(f);
                }
            }
        } catch (Exception e) {
            log.debug("Failed to read crew-memory bucket: {}", e.getMessage());
        }
        return facts;
    }

    /**
     * Read and parse a single crew-scoped key into a {@link Fact}. Returns null
     * when the entry is missing/unreadable or has expired (TTL); an expired
     * entry is pruned best-effort before returning null.
     */
    private Fact readFact(KeyValue kv, String prefix, String k, long now) {
        try {
            var entry = kv.get(k);
            if (entry == null || entry.getValue() == null) return null;
            // readTree, NOT readValue(Record.class): native-safe.
            JsonNode n = objectMapper.readTree(entry.getValue());
            String[] parts = k.substring(prefix.length()).split("\\.", 2);
            String topic = parts.length > 0 ? parts[0] : "";
            String key = parts.length > 1 ? parts[1] : "";
            String learnedAt = n.path(F_LEARNED_AT).asText("");
            // usedAt defaults to learnedAt for back-compat with pre-LRU entries.
            String usedAt = n.path(F_USED_AT).asText(learnedAt);
            long usedMillis = parseMillis(usedAt);
            // App-level TTL: a fact not used/refreshed within ttlDays is
            // expired, so prune it and skip. Per-crew (the shared bucket's
            // own TTL can't be per-crew). usedMillis=0 (unparseable) is
            // treated as fresh to avoid deleting on a parse glitch.
            if (ttlMillis > 0 && usedMillis > 0 && now - usedMillis > ttlMillis) {
                tryDelete(kv, k);
                return null;
            }
            return new Fact(k, topic, key, n.path(F_VALUE).asText(""),
                    n.path(F_LEARNED_BY).asText(""), learnedAt, usedMillis);
        } catch (Exception e) {
            log.debug("Skipping unreadable crew-memory key {}: {}", k, e.getMessage());
            return null;
        }
    }

    /** Best-effort delete of a KV key; swallows failures (eviction is non-critical). */
    private void tryDelete(KeyValue kv, String k) {
        try {
            kv.delete(k);
        } catch (Exception ignore) {
            // best effort; a failed prune just leaves the expired entry for next pass
        }
    }

    private static long parseMillis(String iso) {
        try {
            return iso == null || iso.isEmpty() ? 0L : Instant.parse(iso).toEpochMilli();
        } catch (Exception e) {
            return 0L;
        }
    }

    private KeyValue bucket() {
        if (!natsProvider.isAvailable()) return null;
        Connection conn = natsProvider.getConnection();
        if (conn == null) return null;
        try {
            return conn.keyValue(MEMORY_BUCKET);
        } catch (Exception e) {
            log.debug("Crew-memory bucket {} unavailable: {}", MEMORY_BUCKET, e.getMessage());
            return null;
        }
    }

    /** NATS KV keys allow [A-Za-z0-9-_/=.] — build a safe crew-scoped key. */
    String natsKey(String topic, String key) {
        return sanitize(crew) + "." + sanitize(topic) + "." + sanitize(key);
    }

    static String sanitize(String s) {
        if (s == null || s.isEmpty()) return "_";
        return s.strip().replaceAll("[^A-Za-z0-9_=-]", "_");
    }

    private record Fact(String natsKey, String topic, String key, String value,
                        String learnedBy, String learnedAt, long usedAtMillis) {}
}
