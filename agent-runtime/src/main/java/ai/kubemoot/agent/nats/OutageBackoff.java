package ai.kubemoot.agent.nats;

import java.time.Duration;
import java.time.Instant;

/**
 * How long, and how often, the coordinator tries again while NATS is away: a pause that
 * starts short and doubles to a cap, inside one deadline. The wait is driven by NATS
 * coming back (the caller tries again after each pause); the deadline is a safety net for
 * an outage that never ends, not the normal path.
 *
 * <p>{@link #start()} begins one {@link Wait}. A decision that makes several publishes
 * (the answer's log event, the synthesis, the close) shares one {@code Wait}, so the whole
 * decision waits at most one bound.
 */
final class OutageBackoff {

    /** The bound of one decision's wait. */
    static final Duration DEFAULT_BOUND = Duration.ofMinutes(2);
    private static final Duration FIRST_PAUSE = Duration.ofMillis(250);
    private static final Duration MAX_PAUSE = Duration.ofSeconds(3);

    private final Duration bound;
    private final Duration firstPause;

    OutageBackoff(Duration bound, Duration firstPause) {
        this.bound = bound;
        this.firstPause = firstPause;
    }

    static OutageBackoff standard() {
        return new OutageBackoff(DEFAULT_BOUND, FIRST_PAUSE);
    }

    Duration bound() {
        return bound;
    }

    /** Begins a wait whose deadline is the bound from now. */
    Wait start() {
        return new Wait(Instant.now().plus(bound), firstPause);
    }

    /** One decision's wait: the shared deadline and the current pause. */
    static final class Wait {
        private final Instant deadline;
        private final Duration firstPause;
        private Duration pause;

        private Wait(Instant deadline, Duration firstPause) {
            this.deadline = deadline;
            this.firstPause = firstPause;
            this.pause = firstPause;
        }

        /** Starts the pauses over, for the next publish of the same decision. */
        synchronized void resetPause() {
            pause = firstPause;
        }

        /**
         * Pauses before the next try. Returns false, without pausing, when the deadline would pass
         * first or the thread is interrupted; the caller then gives up.
         */
        synchronized boolean next() {
            if (!Instant.now().plus(pause).isBefore(deadline)) {
                return false;
            }
            try {
                Thread.sleep(pause.toMillis());
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return false;
            }
            pause = pause.multipliedBy(2).compareTo(MAX_PAUSE) > 0 ? MAX_PAUSE : pause.multipliedBy(2);
            return true;
        }
    }
}
