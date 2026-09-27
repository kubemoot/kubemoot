package ai.kubemoot.agent.provider;

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
import java.util.Optional;
import java.util.UUID;

/**
 * Atomic claim/release of VRAM footprint tickets against ModelProviders.
 *
 * <p>Each inference call claims a {@link Ticket} carrying the model's
 * VRAM footprint (MiB) before issuing the call, and releases it in a
 * {@code finally} block on completion. Concurrent callers see active
 * tickets when computing live headroom, so bin-packing across providers
 * emerges without static slot configuration.</p>
 *
 * <p>Design: {@code kubemoot/docs/scheduler.md} — "JIT GPU Scheduling →
 * Update (2026-05-28): VRAM-headroom tickets". This class is the
 * "single ticket claim and release algorithm" — each call holds exactly
 * one ticket (no double-claim), multiple tickets coexist on the same
 * provider as long as their footprints fit within available headroom.</p>
 *
 * <h3>Two-layer release guarantee</h3>
 * <ol>
 *   <li><b>Happy path:</b> caller's {@code try {} finally { release() }}
 *       drops the ticket immediately on completion (success, failure, or
 *       timeout). Sub-second reclaim.</li>
 *   <li><b>Safety net:</b> the bucket carries a TTL (set at provisioning
 *       time in {@code nats-streams-job.yaml}). If the holder crashes or
 *       disappears between claim and release, the ticket evaporates
 *       automatically — no reaper, no leader, no permanent VRAM leak.</li>
 * </ol>
 * Both layers are required. Either alone has a failure mode.
 *
 * <h3>Race handling</h3>
 * The ticket key uses a UUID, so {@code create()} on the KV bucket never
 * races on the key itself. The race is on the <i>budget</i>: between
 * computing headroom and creating the ticket, another caller may claim
 * and shrink it. {@link #claim} verifies after Create that the post-claim
 * total still fits; if not, it deletes the ticket and returns empty so
 * the caller can retry (typically by recomputing headroom from fresh
 * state). Bounded retry lives in the selector, not here — this class
 * does one attempt and reports honestly.
 *
 * <h3>Fallback</h3>
 * NATS unreachable, bucket missing, or footprint = 0 → {@link #claim}
 * returns empty. The caller falls back to its static endpoint with no
 * ticket — degraded mode, no guarantee against over-commit, but inference
 * still happens. Never block a call on ticket infrastructure.
 */
@ApplicationScoped
public class TicketManager {

    private static final Logger log = LoggerFactory.getLogger(TicketManager.class);

    /** NATS KV bucket name — must match operator provisioning. */
    static final String TICKETS_BUCKET = "kubemoot_provider_tickets";

    /** JSON field names on the stored ticket/residency document. */
    private static final String FIELD_MODEL_NAME = "modelName";
    private static final String FIELD_MODEL_FOOTPRINT = "modelFootprintMiB";
    /** Planned evictions on a cold-load ticket: {model: footprintMiB}. */
    private static final String FIELD_EVICTS = "evicts";

    /** Hex chars of a random UUID used as the holder-id suffix when HOSTNAME is unset. */
    private static final int HOLDER_ID_SUFFIX_CHARS = 8;

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper objectMapper;
    private final String holderId;

    @Inject
    public TicketManager(NatsConnectionProvider natsProvider, ObjectMapper objectMapper) {
        this.natsProvider = natsProvider;
        this.objectMapper = objectMapper;
        // Holder id identifies this agent process for debugging and (future)
        // owner-scoped reclaim. Hostname-based — readable in dashboards but
        // unique per pod. Falls back to a random UUID prefix when env is bare.
        String host = System.getenv("HOSTNAME");
        this.holderId = (host == null || host.isEmpty())
                ? "agent-" + UUID.randomUUID().toString().substring(0, HOLDER_ID_SUFFIX_CHARS)
                : host;
    }

    /**
     * Attempt to claim a ticket for {@code footprintMiB} on the named
     * provider, subject to the caller-computed {@code availableHeadroomMiB}
     * (= provider total VRAM minus loaded-model footprints).
     *
     * Returns {@link Optional#empty} when:
     * <ul>
     *   <li>NATS is unavailable</li>
     *   <li>footprint or headroom is non-positive</li>
     *   <li>existing tickets already saturate the budget</li>
     *   <li>a concurrent claim shrank headroom inside our race window
     *       (caller should recompute headroom and retry)</li>
     * </ul>
     *
     * @param provider             ModelProvider name (matches state bucket key)
     * @param footprintMiB         VRAM this call needs (MiB)
     * @param availableHeadroomMiB total VRAM minus loaded-model footprints
     */
    public Optional<Ticket> claim(String provider, long footprintMiB, long availableHeadroomMiB) {
        return claim(provider, footprintMiB, 0L, availableHeadroomMiB);
    }

    /**
     * Phase D overload — also records the call's KV-cache footprint
     * (MiB) on the ticket so concurrent callers can sum across active
     * tickets via {@link #activeKvCacheFor}. Passing 0 for
     * {@code kvCacheFootprintMiB} reproduces the v2.2 behaviour
     * (model-weights-only gating).
     */
    public Optional<Ticket> claim(String provider, long footprintMiB,
                                   long kvCacheFootprintMiB, long availableHeadroomMiB) {
        return claim(provider, footprintMiB, kvCacheFootprintMiB, availableHeadroomMiB, null);
    }

    /**
     * Cold-start convergence overload — also stamps {@code modelName} on
     * the ticket so concurrent selectors can see "this provider is
     * loading model M right now" via {@link #activeModelsOn} and converge
     * on the in-flight cold-load instead of redundantly cold-loading on
     * another provider. Passing {@code null} reproduces the prior
     * behaviour (ticket carries no model identity). Added 2026-05-31 for
     * [[Cold-Start Model-Load Wedge]].
     */
    public Optional<Ticket> claim(String provider, long footprintMiB,
                                   long kvCacheFootprintMiB, long availableHeadroomMiB,
                                   String modelName) {
        return claimWithEvictions(provider, footprintMiB, kvCacheFootprintMiB, availableHeadroomMiB,
                modelName, java.util.Map.of(), java.util.Set.of());
    }

    /**
     * Claim a ticket for a cold load that unloads {@code evictions} (model to
     * footprint MiB) first. The planned evictions ride on the ticket, and the budget
     * counts every distinct victim planned by any active ticket on the provider once
     * (only victims still in {@code residentModels}; a victim already gone is in the
     * headroom). Two planners that pick the same victim therefore cannot both spend
     * its memory: the second one's post-claim check sees both loads against one
     * victim's credit and releases. The claim is also released when a victim picked
     * up in-flight work in the meantime.
     *
     * @param availableHeadroomMiB total VRAM minus loaded-model footprints
     * @param residentModels       models the provider currently reports loaded
     */
    public Optional<Ticket> claimWithEvictions(String provider, long footprintMiB, long kvCacheFootprintMiB,
                                               long availableHeadroomMiB, String modelName,
                                               java.util.Map<String, Long> evictions,
                                               java.util.Set<String> residentModels) {
        if (provider == null || provider.isEmpty() || footprintMiB <= 0) return Optional.empty();
        KeyValue kv = openBucket();
        if (kv == null) return Optional.empty();
        Budget budget = new Budget(provider, footprintMiB, availableHeadroomMiB, residentModels);
        // Pre-flight: cheap check before writing; planned victims (including ours) count once.
        if (!budget.fits(kv, evictions, 0L)) {
            return Optional.empty();
        }
        Ticket ticket = new Ticket(UUID.randomUUID().toString(), provider, footprintMiB, holderId,
                Instant.now().toString(), Math.max(0L, kvCacheFootprintMiB), modelName);
        try {
            kv.create(ticket.keyName(), encode(ticket, evictions));
        } catch (Exception e) {
            log.debug("Ticket create failed for {}: {}", ticket.keyName(), e.getMessage());
            return Optional.empty();
        }
        // Post-claim verification: our ticket is now in the scan, so re-check without adding it again.
        if (!budget.fits(kv, java.util.Map.of(), -footprintMiB) || victimsBusy(kv, provider, evictions.keySet())) {
            log.debug("Released ticket {} on {}: over budget or a planned victim is busy", ticket.ticketId(), provider);
            release(ticket);
            return Optional.empty();
        }
        log.debug("Claimed ticket {} on {} for {} MiB (evicting {})",
                ticket.ticketId(), provider, footprintMiB, evictions.keySet());
        return Optional.of(ticket);
    }

    /** The tickets bucket, or null when NATS or the bucket is unavailable. */
    private KeyValue openBucket() {
        if (!natsProvider.isAvailable()) return null;
        Connection conn = natsProvider.getConnection();
        if (conn == null) return null;
        try {
            return conn.keyValue(TICKETS_BUCKET);
        } catch (Exception e) {
            log.debug("Tickets bucket {} not available: {}", TICKETS_BUCKET, e.getMessage());
            return null;
        }
    }

    /** True when any model in {@code victims} is held by an active ticket on the provider. */
    private boolean victimsBusy(KeyValue kv, String provider, java.util.Set<String> victims) {
        if (victims.isEmpty()) return false;
        java.util.Set<String> busy = collectModelsFor(kv, provider);
        return victims.stream().anyMatch(busy::contains);
    }

    /** The budget for one claim: active footprints plus this claim within headroom plus eviction credit. */
    private final class Budget {
        private final String provider;
        private final long footprintMiB;
        private final long headroomMiB;
        private final java.util.Set<String> resident;

        Budget(String provider, long footprintMiB, long headroomMiB, java.util.Set<String> resident) {
            this.provider = provider;
            this.footprintMiB = footprintMiB;
            this.headroomMiB = headroomMiB;
            this.resident = resident == null ? java.util.Set.of() : resident;
        }

        /** {@code adjustMiB} corrects for our own ticket already being in the scan (post-claim). */
        boolean fits(KeyValue kv, java.util.Map<String, Long> ownEvictions, long adjustMiB) {
            List<java.util.Map<String, Long>> planned = new ArrayList<>(plannedEvictionMaps(kv, provider));
            planned.add(ownEvictions);
            long credit = evictionCreditMiB(planned, resident);
            long active = sumFootprintsFor(kv, provider) + adjustMiB;
            return withinBudget(active, footprintMiB, headroomMiB, credit);
        }
    }

    /**
     * Pure budget rule: active ticket footprints plus this claim must fit the
     * headroom plus the eviction credit. Non-positive footprint never fits; with
     * no credit, a non-positive headroom never fits.
     */
    static boolean withinBudget(long activeMiB, long footprintMiB, long headroomMiB, long creditMiB) {
        if (footprintMiB <= 0 || headroomMiB + creditMiB <= 0) return false;
        return activeMiB + footprintMiB <= headroomMiB + creditMiB;
    }

    /**
     * Memory planned evictions will free: each distinct victim counted once, and
     * only while it is still resident (after it unloads, its memory is part of the
     * headroom already). Pure, for testing.
     */
    static long evictionCreditMiB(List<java.util.Map<String, Long>> planned, java.util.Set<String> resident) {
        java.util.Map<String, Long> distinct = new java.util.HashMap<>();
        for (java.util.Map<String, Long> m : planned) {
            m.forEach((model, fp) -> {
                if (resident.contains(model)) distinct.merge(model, fp, Math::max);
            });
        }
        return distinct.values().stream().mapToLong(Long::longValue).sum();
    }

    /** Victims planned by the active tickets on {@code provider}, one map per ticket. */
    private List<java.util.Map<String, Long>> plannedEvictionMaps(KeyValue kv, String provider) {
        List<java.util.Map<String, Long>> out = new ArrayList<>();
        forEachTicket(kv, provider, "planned evictions", node -> {
            JsonNode ev = node.path(FIELD_EVICTS);
            if (ev.isObject()) {
                java.util.Map<String, Long> m = new java.util.HashMap<>();
                ev.fields().forEachRemaining(e -> m.put(e.getKey(), e.getValue().asLong(0L)));
                out.add(m);
            }
        });
        return out;
    }

    /** Models that active tickets on {@code provider} plan to unload, with their footprints. */
    public java.util.Map<String, Long> plannedEvictionsOn(String provider) {
        KeyValue kv = provider == null ? null : openBucket();
        java.util.Map<String, Long> out = new java.util.HashMap<>();
        if (kv != null) {
            plannedEvictionMaps(kv, provider).forEach(out::putAll);
        }
        return out;
    }

    /** Start times of the active tickets on {@code provider}; the in-flight calls holding its slots. */
    public List<Instant> inFlightStartsFor(String provider) {
        KeyValue kv = provider == null ? null : openBucket();
        List<Instant> out = new ArrayList<>();
        if (kv != null) {
            forEachTicket(kv, provider, "in-flight starts", node -> parseInstant(node.path("claimedAt").asText(""), out));
        }
        return out;
    }

    private static void parseInstant(String text, List<Instant> out) {
        try {
            out.add(Instant.parse(text));
        } catch (Exception e) {
            // a ticket without a readable start time contributes no wait estimate
        }
    }

    /** Drops the residency overlay entry for a model that was unloaded. Best-effort. */
    public void clearResidency(String provider, String model) {
        KeyValue kv = openBucket();
        if (kv == null || provider == null || model == null) return;
        try {
            kv.delete(residencyKey(provider, model));
        } catch (Exception e) {
            log.debug("clearResidency({},{}) failed (TTL cleans up): {}", provider, model, e.getMessage());
        }
    }

    /**
     * Release a held ticket. Idempotent and best-effort — failures are
     * logged and swallowed because the bucket TTL is the safety net. The
     * caller must not be exposed to release errors; an inference call's
     * result matters more than a clean delete.
     */
    public void release(Ticket ticket) {
        if (ticket == null) return;
        if (!natsProvider.isAvailable()) return;

        Connection conn = natsProvider.getConnection();
        if (conn == null) return;

        try {
            KeyValue kv = conn.keyValue(TICKETS_BUCKET);
            kv.delete(ticket.keyName());
            log.debug("Released ticket {} on {}", ticket.ticketId(), ticket.provider());
        } catch (Exception e) {
            // TTL will clean up — log and move on.
            log.debug("Ticket release failed for {} (TTL safety net applies): {}",
                    ticket.keyName(), e.getMessage());
        }
    }

    /**
     * Sum the footprints of all active tickets for the named provider.
     * Used by callers (notably {@link ProviderSelector}) to compute
     * provider headroom for the cold-load gate (free VRAM ≥ footprint).
     */
    public long activeFootprintFor(String provider) {
        if (provider == null || !natsProvider.isAvailable()) return 0L;
        Connection conn = natsProvider.getConnection();
        if (conn == null) return 0L;
        try {
            return sumFootprintsFor(conn.keyValue(TICKETS_BUCKET), provider);
        } catch (Exception e) {
            log.debug("activeFootprintFor({}) failed: {}", provider, e.getMessage());
            return 0L;
        }
    }

    /**
     * Count active tickets for the named provider — the in-flight call
     * concurrency on this provider RIGHT NOW. Together with
     * {@code ProviderState.maxParallel}, this gives the slot-based gate
     * for warm-model calls: provider has a free slot if
     * {@code activeCountFor(name) < maxParallel}.
     *
     * Separate from {@link #activeFootprintFor} because warm-model calls
     * reserve only scratch VRAM (or zero) but still consume one
     * concurrency slot — counting matters more than footprint summing
     * for that gate. Cold-load gating still uses {@code activeFootprintFor}.
     */
    public int activeCountFor(String provider) {
        if (provider == null || !natsProvider.isAvailable()) return 0;
        Connection conn = natsProvider.getConnection();
        if (conn == null) return 0;
        try {
            return countTicketsFor(conn.keyValue(TICKETS_BUCKET), provider);
        } catch (Exception e) {
            log.debug("activeCountFor({}) failed: {}", provider, e.getMessage());
            return 0;
        }
    }

    /**
     * Set of model names currently held by active tickets on the named
     * provider. A model appears here iff at least one in-flight call is
     * targeting it on this provider — i.e., the model is loaded OR
     * actively being cold-loaded right now.
     *
     * Used by {@link ProviderSelector} to converge concurrent selectors
     * on an in-flight cold-load: when agent A starts cold-loading model
     * M on provider P, agent B arriving shortly after sees M in
     * {@code activeModelsOn(P)} and treats P as warm-equivalent for M,
     * picking P (and queueing at Ollama) instead of triggering a
     * redundant cold-load on a different provider. See
     * [[Cold-Start Model-Load Wedge]] for the design.
     *
     * Empty set on errors / no tickets / no NATS — caller treats
     * absence as "model not in-flight here" (safe fallback to the
     * pre-2026-05-31 behaviour).
     */
    public java.util.Set<String> activeModelsOn(String provider) {
        if (provider == null || !natsProvider.isAvailable()) return java.util.Set.of();
        Connection conn = natsProvider.getConnection();
        if (conn == null) return java.util.Set.of();
        try {
            return collectModelsFor(conn.keyValue(TICKETS_BUCKET), provider);
        } catch (Exception e) {
            log.debug("activeModelsOn({}) failed: {}", provider, e.getMessage());
            return java.util.Set.of();
        }
    }

    /**
     * Build the prefix used to scope a {@code keys()} scan to one provider's
     * tickets. The double-underscore separator matches {@link Ticket#keyFor}.
     */
    static String keyPrefixFor(String provider) {
        return provider + "__";
    }

    /**
     * Parse one stored ticket entry to a {@link JsonNode}, or {@code null}
     * if the entry is absent, empty, or unreadable. Native-safe: uses
     * {@code readTree}, never {@code readValue} on the Ticket record (record
     * reflection fails silently in GraalVM native; see
     * {@code [[project_jit_native_deser_bug]]}). Unreadable entries are
     * logged and skipped — better to under-count than crash a headroom calc.
     */
    private JsonNode readTicketNode(KeyValue kv, String key, String context) {
        try {
            var entry = kv.get(key);
            if (entry == null || entry.getValue() == null) return null;
            return objectMapper.readTree(entry.getValue());
        } catch (Exception e) {
            log.debug("Skipping unreadable ticket {} ({}): {}", key, context, e.getMessage());
            return null;
        }
    }

    /**
     * Scan every ticket whose key matches the provider prefix and feed each
     * successfully-parsed document to {@code consumer}. Centralises the
     * list-keys / prefix-filter / parse / per-entry-skip pattern shared by
     * the footprint, KV-cache, and model-collection scans so each caller is
     * a one-line projection with no nested try and no loop short-circuit.
     */
    private void forEachTicket(KeyValue kv, String provider, String context,
                               java.util.function.Consumer<JsonNode> consumer) {
        String prefix = keyPrefixFor(provider);
        try {
            for (String key : kv.keys()) {
                if (key.startsWith(prefix)) {
                    JsonNode node = readTicketNode(kv, key, context);
                    if (node != null) {
                        consumer.accept(node);
                    }
                }
            }
        } catch (Exception e) {
            log.debug("Listing tickets for {} ({}) failed: {}", provider, context, e.getMessage());
        }
    }

    /**
     * Collect distinct ticket.modelName values for the named provider.
     * Native-safe: uses {@code readTree} (NOT {@code readValue} on the
     * Ticket record — that fails silently in GraalVM native; see
     * {@code [[project_jit_native_deser_bug]]}). Skips tickets that
     * predate the modelName field (those will have null/missing
     * modelName and contribute nothing to the set).
     */
    private java.util.Set<String> collectModelsFor(KeyValue kv, String provider) {
        java.util.Set<String> models = new java.util.HashSet<>();
        forEachTicket(kv, provider, "model collect", node -> {
            String modelName = node.path(FIELD_MODEL_NAME).asText(null);
            if (modelName != null && !modelName.isEmpty()) {
                models.add(modelName);
            }
        });
        return models;
    }

    // ============================================================================
    // Residency reservations — the "model stays resident between calls" overlay.
    // ============================================================================
    //
    // A per-call ticket reserves VRAM only for the duration of one inference, but
    // the loaded model STAYS resident on the GPU (Ollama keep_alive) long after
    // the call. The operator's /api/ps probe republishes that residency only every
    // ~30s, so between a model loading and the probe catching up, other agents see
    // the GPU as free and cold-load onto it, evicting the resident model (the
    // 2026-06-11 thrash). A residency reservation closes that gap: the agent that
    // just ran model M on provider P records M's footprint immediately under a
    // STABLE key, refreshed on every use, so all agents see M resident at decision
    // time without waiting for the probe. Stored in the same tickets bucket but
    // under a distinct "residency__<provider>__<model>" prefix so the per-call
    // ticket sums (which scan "<provider>__") never see it. The bucket's 10m TTL
    // reaps a residency entry that stops being refreshed (model idled out/evicted).
    // See [[JIT Scheduler Locality Algorithm]].
    static final String RESIDENCY_PREFIX = "residency__";

    static String residencyKey(String provider, String model) {
        return RESIDENCY_PREFIX + provider + "__" + model;
    }

    /**
     * Record (or refresh) that {@code model} is resident on {@code provider} with
     * the given VRAM footprint. Idempotent upsert under a stable key; each call
     * resets the bucket TTL so the reservation lives as long as the model keeps
     * being used. Best-effort: NATS errors are swallowed (the probe is the
     * eventual backstop). No-op for blank inputs or non-positive footprint.
     */
    public void recordResidency(String provider, String model, long footprintMiB) {
        if (provider == null || provider.isEmpty()) return;
        if (model == null || model.isEmpty()) return;
        if (footprintMiB <= 0) return;
        if (!natsProvider.isAvailable()) return;
        Connection conn = natsProvider.getConnection();
        if (conn == null) return;
        try {
            KeyValue kv = conn.keyValue(TICKETS_BUCKET);
            ObjectNode node = objectMapper.createObjectNode();
            node.put("provider", provider);
            node.put(FIELD_MODEL_NAME, model);
            node.put(FIELD_MODEL_FOOTPRINT, footprintMiB);
            kv.put(residencyKey(provider, model), objectMapper.writeValueAsBytes(node));
        } catch (Exception e) {
            log.debug("recordResidency({},{}) failed (probe is backstop): {}", provider, model, e.getMessage());
        }
    }

    /**
     * Per-model resident footprints reserved on the named provider via
     * {@link #recordResidency}. The selector merges this overlay with the
     * operator-published (probe-stale) resident footprints, taking the per-model
     * MAX so a just-loaded model is visible immediately without double-counting
     * one the probe has already published. Empty on errors / no entries / no NATS.
     */
    public java.util.Map<String, Long> residentFootprintsFor(String provider) {
        if (provider == null || !natsProvider.isAvailable()) return java.util.Map.of();
        Connection conn = natsProvider.getConnection();
        if (conn == null) return java.util.Map.of();
        String prefix = RESIDENCY_PREFIX + provider + "__";
        java.util.Map<String, Long> out = new java.util.HashMap<>();
        try {
            KeyValue kv = conn.keyValue(TICKETS_BUCKET);
            for (String key : kv.keys()) {
                if (key.startsWith(prefix)) {
                    collectResidency(kv, key, out);
                }
            }
        } catch (Exception e) {
            log.debug("residentFootprintsFor({}) failed: {}", provider, e.getMessage());
        }
        return out;
    }

    /**
     * Parse one residency entry and, when valid (non-blank model, positive
     * footprint), record it in {@code out}. Unreadable entries are logged
     * and skipped — extracted from {@link #residentFootprintsFor} to drop
     * the nested try/catch.
     */
    private void collectResidency(KeyValue kv, String key, java.util.Map<String, Long> out) {
        try {
            var entry = kv.get(key);
            if (entry == null || entry.getValue() == null) return;
            JsonNode n = objectMapper.readTree(entry.getValue());
            String model = n.path(FIELD_MODEL_NAME).asText(null);
            long fp = n.path(FIELD_MODEL_FOOTPRINT).asLong(0L);
            if (model != null && !model.isEmpty() && fp > 0) {
                out.put(model, fp);
            }
        } catch (Exception e) {
            log.debug("Skipping unreadable residency entry {}: {}", key, e.getMessage());
        }
    }

    /**
     * Sum the KV-cache footprints of all active tickets for the named
     * provider (Phase D). Used by the FitPredictor cold-load gate so
     * a concurrent claim's KV pressure is visible to the next caller.
     * Same prefix-scan pattern as {@link #activeFootprintFor}.
     */
    public long activeKvCacheFor(String provider) {
        if (provider == null || !natsProvider.isAvailable()) return 0L;
        Connection conn = natsProvider.getConnection();
        if (conn == null) return 0L;
        try {
            return sumKvCacheFor(conn.keyValue(TICKETS_BUCKET), provider);
        } catch (Exception e) {
            log.debug("activeKvCacheFor({}) failed: {}", provider, e.getMessage());
            return 0L;
        }
    }

    /** Sum kvCacheFootprintMiB across tickets for the named provider. Native-safe. */
    private long sumKvCacheFor(KeyValue kv, String provider) {
        long[] total = {0L};
        forEachTicket(kv, provider, "kv sum",
                node -> total[0] += node.path("kvCacheFootprintMiB").asLong(0L));
        return total[0];
    }

    /** Count tickets matching the provider prefix on the bucket. Native-safe (no record reflection). */
    private int countTicketsFor(KeyValue kv, String provider) {
        String prefix = keyPrefixFor(provider);
        int count = 0;
        try {
            for (String key : kv.keys()) {
                if (key.startsWith(prefix)) {
                    var entry = kv.get(key);
                    if (entry != null && entry.getValue() != null) {
                        count++;
                    }
                }
            }
        } catch (Exception e) {
            log.debug("Counting tickets for {} failed: {}", provider, e.getMessage());
        }
        return count;
    }

    /**
     * Sum footprints for the named provider over a given KeyValue handle.
     * Native-safe: uses {@code readTree}, never {@code readValue} on the
     * Ticket record (record reflection fails silently in GraalVM native —
     * see {@code [[project_jit_native_deser_bug]]}).
     */
    private long sumFootprintsFor(KeyValue kv, String provider) {
        long[] total = {0L};
        forEachTicket(kv, provider, "footprint sum",
                node -> total[0] += node.path(FIELD_MODEL_FOOTPRINT).asLong(0L));
        return total[0];
    }

    /** Encode a Ticket and its planned evictions to JSON bytes via manual ObjectNode (native-safe). */
    private byte[] encode(Ticket ticket, java.util.Map<String, Long> evictions) {
        ObjectNode node = objectMapper.createObjectNode();
        node.put("ticketId", ticket.ticketId());
        node.put("provider", ticket.provider());
        node.put(FIELD_MODEL_FOOTPRINT, ticket.modelFootprintMiB());
        node.put("kvCacheFootprintMiB", ticket.kvCacheFootprintMiB());
        node.put("holderId", ticket.holderId());
        node.put("claimedAt", ticket.claimedAt());
        if (ticket.modelName() != null) {
            node.put(FIELD_MODEL_NAME, ticket.modelName());
        }
        if (!evictions.isEmpty()) {
            ObjectNode ev = node.putObject(FIELD_EVICTS);
            evictions.forEach(ev::put);
        }
        try {
            return objectMapper.writeValueAsBytes(node);
        } catch (Exception e) {
            // Should be impossible for a flat ObjectNode; surface as runtime
            // so the bug is loud during development.
            throw new IllegalStateException("Failed to encode ticket " + ticket.ticketId(), e);
        }
    }

    /**
     * Algorithm helper, public for unit testing. Decides whether a
     * proposed claim of {@code footprintMiB} fits given current active
     * tickets and the available headroom. Pure function, no NATS.
     */
    public static boolean fitsBudget(List<Ticket> activeTickets, long footprintMiB, long availableHeadroomMiB) {
        if (footprintMiB <= 0 || availableHeadroomMiB <= 0) return false;
        long active = 0L;
        if (activeTickets != null) {
            for (Ticket t : activeTickets) {
                if (t != null) active += t.modelFootprintMiB();
            }
        }
        return active + footprintMiB <= availableHeadroomMiB;
    }

    /** Test-only accessor for the env-derived holderId. */
    String holderIdForTest() {
        return holderId;
    }
}
