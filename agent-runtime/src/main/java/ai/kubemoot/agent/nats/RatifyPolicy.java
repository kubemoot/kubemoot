package ai.kubemoot.agent.nats;

import java.util.Locale;
import java.util.Optional;

/**
 * Whether and how the contributors ratify the coordinator's draft answer, as the crew's
 * consensus archetype declares ({@code ratify} on the MootArchetype). Ratification is on
 * when the archetype declares nothing: it is what makes an answer the crew's.
 */
record RatifyPolicy(boolean enabled, int maxRounds, Roster roster) {

    /** Who ratifies. */
    enum Roster { CONTRIBUTORS, CREW }

    static final int DEFAULT_MAX_ROUNDS = 2;
    static final int MAX_ROUNDS_LIMIT = 5;

    /** The policy of an archetype that declares no {@code ratify} block. */
    static final RatifyPolicy DEFAULT = new RatifyPolicy(true, DEFAULT_MAX_ROUNDS, Roster.CONTRIBUTORS);

    /**
     * The policy for the declared values; an absent or unreadable value takes the default,
     * and the rounds are held between 1 and {@value #MAX_ROUNDS_LIMIT}.
     */
    static RatifyPolicy of(Optional<String> enabled, Optional<Integer> maxRounds, Optional<String> roster) {
        boolean on = enabled.map(v -> !"false".equalsIgnoreCase(v.strip())).orElse(true);
        int rounds = Math.max(1, Math.min(MAX_ROUNDS_LIMIT, maxRounds.orElse(DEFAULT_MAX_ROUNDS)));
        return new RatifyPolicy(on, rounds, rosterOf(roster));
    }

    private static Roster rosterOf(Optional<String> value) {
        return value.map(v -> "crew".equals(v.strip().toLowerCase(Locale.ROOT)) ? Roster.CREW : Roster.CONTRIBUTORS)
                .orElse(Roster.CONTRIBUTORS);
    }

    /** True when a draft in {@code round} may be revised once more. */
    boolean allowsRevisionAfter(int round) {
        return round < maxRounds;
    }
}
