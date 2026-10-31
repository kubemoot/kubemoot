package ai.kubemoot.agent.nats;

/**
 * Where the coordinator writes the decisions it makes. An append waits for the log to
 * acknowledge it, so the coordinator can act on a decision only once the decision is
 * written (write-ahead), and it states the last sequence the writer saw on the thread's
 * subject, so a second writer on the same thread is refused (fencing). Implementations
 * must not throw: every outcome is an {@link AppendResult}.
 */
interface CoordinatorEventLog {

    /** A log that writes nothing: the setting when the event log is off. */
    CoordinatorEventLog OFF = (subject, event, expectedLastSequence) -> AppendResult.appended(0);

    /**
     * Appends {@code event} to the log at {@code subject}, the thread's coordinator subject,
     * when the last message on the subject has sequence {@code expectedLastSequence} (0: the
     * subject has no message yet).
     */
    AppendResult append(String subject, CoordinatorEvent event, long expectedLastSequence);

    /**
     * The last message on {@code subject}, or empty when the subject has none or the log
     * cannot be read. Used after an append whose outcome is unknown (an acknowledgement
     * that never came), to learn whether the event was stored.
     */
    default java.util.Optional<LastMessage> last(String subject) {
        return java.util.Optional.empty();
    }

    /** The sequence and event id of a stored message. */
    record LastMessage(long sequence, String eventId) {}

    /** What happened to an append, and for an appended event the sequence it was stored at. */
    record AppendResult(Outcome outcome, long sequence) {

        /** How an append ended. */
        enum Outcome {
            /** The event is in the log (or was already: a repeat of the same event id). */
            APPENDED,
            /** Another writer appended to the subject since this writer last did: it no longer owns the thread. */
            FENCED,
            /** The log could not be reached or refused the event; nothing was written. */
            FAILED
        }

        static AppendResult appended(long sequence) {
            return new AppendResult(Outcome.APPENDED, sequence);
        }

        static AppendResult fenced() {
            return new AppendResult(Outcome.FENCED, 0);
        }

        static AppendResult failed() {
            return new AppendResult(Outcome.FAILED, 0);
        }

        boolean isAppended() {
            return outcome == Outcome.APPENDED;
        }

        boolean isFenced() {
            return outcome == Outcome.FENCED;
        }
    }
}
