package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for the pure algorithm portion of {@link TicketManager}.
 *
 * The NATS-touching paths ({@code claim}, {@code release},
 * {@code activeFootprintFor}) are thin adapters over JNATS KV calls and
 * are covered by integration smoke testing post-deploy — mocking the
 * KeyValue API for unit tests adds noise without confidence, same
 * pattern as {@link ProviderSelectorTest}.
 *
 * What we DO unit-test here is {@link TicketManager#fitsBudget} — the
 * race-safety predicate the {@code claim} method uses both pre-flight
 * and post-create. That decision is the heart of "no two agents
 * over-commit the same VRAM."
 */
class TicketManagerTest {

    private static Ticket ticket(String provider, long footprintMiB) {
        return new Ticket("t-" + Math.random(), provider, footprintMiB, "test-holder", "2026-05-28T00:00:00Z");
    }

    // ---- fitsBudget: the race-safety predicate ----

    @Test
    void emptyBucketAcceptsFirstClaim() {
        // No active tickets, headroom 32GB, claim 22GB qwen3:32b → fits.
        assertTrue(TicketManager.fitsBudget(List.of(), 22_000, 32_000));
    }

    @Test
    void rejectsClaimExceedingHeadroom() {
        // Empty bucket but headroom too small (e.g. 32B model loaded eats most VRAM).
        assertFalse(TicketManager.fitsBudget(List.of(), 22_000, 5_000));
    }

    @Test
    void packsMultipleSmallClaimsOnSameProvider() {
        // Four 5GB qwen3:8b tickets share a 32GB provider — total 20GB ≤ 32GB → fifth fits.
        List<Ticket> active = List.of(
                ticket("rig0", 5_000),
                ticket("rig0", 5_000),
                ticket("rig0", 5_000)
        );
        assertTrue(TicketManager.fitsBudget(active, 5_000, 32_000), "Fourth 5GB claim should fit");
    }

    @Test
    void rejectsClaimThatPushesOverBudget() {
        // Three 8GB tickets active = 24GB. Adding 10GB would push to 34GB on a 32GB provider → reject.
        List<Ticket> active = List.of(
                ticket("rig0", 8_000),
                ticket("rig0", 8_000),
                ticket("rig0", 8_000)
        );
        assertFalse(TicketManager.fitsBudget(active, 10_000, 32_000));
    }

    @Test
    void exactlyFillingBudgetIsAllowed() {
        // Boundary: 22GB active + 10GB claim = 32GB = headroom → allowed (≤ not <).
        List<Ticket> active = List.of(ticket("rig0", 22_000));
        assertTrue(TicketManager.fitsBudget(active, 10_000, 32_000));
    }

    @Test
    void zeroOrNegativeFootprintRejected() {
        assertFalse(TicketManager.fitsBudget(List.of(), 0, 32_000));
        assertFalse(TicketManager.fitsBudget(List.of(), -1, 32_000));
    }

    @Test
    void zeroOrNegativeHeadroomRejected() {
        // Provider has no headroom (all VRAM consumed by loaded models) → no claim possible.
        assertFalse(TicketManager.fitsBudget(List.of(), 5_000, 0));
        assertFalse(TicketManager.fitsBudget(List.of(), 5_000, -1));
    }

    @Test
    void nullActiveTicketsTreatedAsEmpty() {
        // Defensive: null list should not NPE, treated as no in-flight reservations.
        assertTrue(TicketManager.fitsBudget(null, 5_000, 32_000));
    }

    @Test
    void nullTicketEntriesIgnored() {
        // Defensive: a null entry in the list should be skipped, not crash.
        List<Ticket> active = java.util.Arrays.asList(ticket("rig0", 5_000), null, ticket("rig0", 5_000));
        assertTrue(TicketManager.fitsBudget(active, 10_000, 32_000));
    }

    // ---- Ticket key format ----

    @Test
    void keyFormatScopesByProvider() {
        // Key format <provider>__<uuid> lets the selector list per-provider via prefix filter.
        // Critical that the separator is unambiguous — provider names contain dashes (ollama-rig1)
        // and UUIDs contain dashes too; double underscore is the safe split.
        Ticket t = new Ticket("abc-123", "ollama-rig1", 5_000, "h", "2026-05-28T00:00:00Z");
        assertEquals("ollama-rig1__abc-123", t.keyName());
        assertEquals("ollama-rig1__abc-123", Ticket.keyFor("ollama-rig1", "abc-123"));
    }

    @Test
    void keyPrefixDistinguishesProviders() {
        // ollama-gpu vs ollama-gpu-extended must NOT be confused on prefix match.
        // Verify by the separator: scanner uses startsWith(provider + "__"), so a longer
        // provider name's prefix won't accidentally match a shorter one's keys.
        String gpuPrefix = "ollama-gpu" + "__";
        String extendedKey = Ticket.keyFor("ollama-gpu-extended", "x");
        assertFalse(extendedKey.startsWith(gpuPrefix),
                "ollama-gpu-extended ticket must not match ollama-gpu prefix");
    }

    // ---- keyPrefixFor: the scan prefix the bucket scans use ----

    @Test
    void scanPrefixMatchesTicketKeyForSeparator() {
        // The prefix used by the per-provider KV scan must be exactly what
        // Ticket.keyName() produces up to the ticketId, or the scan misses
        // (or over-matches) tickets. Both must agree on the "__" separator.
        String provider = "ollama-rig1";
        String prefix = TicketManager.keyPrefixFor(provider);
        assertEquals("ollama-rig1__", prefix);
        assertTrue(Ticket.keyFor(provider, "abc-123").startsWith(prefix),
                "a ticket key for the provider must match its scan prefix");
    }

    @Test
    void scanPrefixDoesNotMatchLongerProviderName() {
        // The scan prefix must not let ollama-gpu pick up ollama-gpu-extended keys.
        String prefix = TicketManager.keyPrefixFor("ollama-gpu");
        assertFalse(Ticket.keyFor("ollama-gpu-extended", "x").startsWith(prefix),
                "longer provider name must not match the shorter provider's scan prefix");
    }
}
