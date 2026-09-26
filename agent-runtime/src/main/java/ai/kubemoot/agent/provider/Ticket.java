package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;

/**
 * A single in-flight reservation against a ModelProvider's VRAM budget.
 *
 * Each inference call claims exactly one Ticket via {@link TicketManager}
 * before issuing the call, and releases it in a {@code finally} block on
 * completion. The Ticket records the footprint (MiB) the call expects to
 * consume so concurrent callers see this claim when computing live
 * headroom. See {@code kubemoot/docs/scheduler.md} — "JIT GPU Scheduling
 * → Update (2026-05-28): VRAM-headroom tickets".
 *
 * Stored in NATS KV bucket {@code kubemoot_provider_tickets} as a JSON
 * document; the bucket carries a TTL that is the unconditional safety-net
 * release if the holder disappears (crash, OOMKill, network partition).
 * The fast path is still the {@code finally}-block delete — TTL is the
 * second layer, not the primary release mechanism.
 *
 * Key format in the bucket: {@code <provider>__<ticketId>}. The double
 * underscore separator avoids collisions with NATS KV's dot-delimited
 * subject semantics while still allowing prefix-scoped lookups per
 * provider via {@code keys()} + filter.
 *
 * {@code @JsonIgnoreProperties(ignoreUnknown=true)} so adding fields
 * never breaks deserialisation on older agents.
 */
@JsonIgnoreProperties(ignoreUnknown = true)
public record Ticket(
        String ticketId,
        String provider,
        long modelFootprintMiB,
        String holderId,
        String claimedAt,
        /**
         * KV-cache VRAM (MiB) this call reserves on the provider, on
         * top of the model weights. Computed at claim time from the
         * call's prompt + max-output token counts via
         * {@link KvCacheEstimator}. Concurrent claimers see this in
         * {@link TicketManager#activeKvCacheFor} so the predictor's
         * cold-load gate factors in real in-flight KV pressure.
         * Added in Phase D (2026-05-28). {@code 0L} on older tickets
         * deserialised from before Phase D; behaves as "no KV
         * reservation" which is the legacy v2.2 behavior.
         */
        long kvCacheFootprintMiB,
        /**
         * Model name this call targets on the provider (e.g.
         * {@code qwen3:8b}). Lets concurrent selectors see in-flight
         * cold-loads via {@link TicketManager#activeModelsOn} and
         * converge on the in-flight load instead of triggering
         * redundant cold-loads on other providers. Added 2026-05-31
         * for the cold-start convergence work. {@code null} on older
         * tickets — behaves as "unknown model," which excludes the
         * ticket from {@code activeModelsOn} matching (safe fallback
         * to the v2.2 behavior where load-in-flight wasn't tracked).
         */
        String modelName
) {
    /** Backwards-compat constructor for callers (and tests) that predate the KV field. */
    public Ticket(String ticketId, String provider, long modelFootprintMiB,
                  String holderId, String claimedAt) {
        this(ticketId, provider, modelFootprintMiB, holderId, claimedAt, 0L, null);
    }

    /** Backwards-compat constructor for callers that predate the modelName field. */
    public Ticket(String ticketId, String provider, long modelFootprintMiB,
                  String holderId, String claimedAt, long kvCacheFootprintMiB) {
        this(ticketId, provider, modelFootprintMiB, holderId, claimedAt, kvCacheFootprintMiB, null);
    }

    /** NATS KV key for this ticket. Used by {@link TicketManager} for create/delete. */
    public String keyName() {
        return keyFor(provider, ticketId);
    }

    /** Build the KV key for a (provider, ticketId) pair. */
    public static String keyFor(String provider, String ticketId) {
        return provider + "__" + ticketId;
    }
}
