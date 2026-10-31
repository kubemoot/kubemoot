package ai.kubemoot.agent.nats;

/**
 * Where the coordinator writes the decisions it makes. Writing is best effort: a log
 * that cannot be reached never stops or slows a discussion, it only loses events.
 * Implementations must not throw.
 */
interface CoordinatorEventLog {

    /** A log that writes nothing: the setting when the event log is off. */
    CoordinatorEventLog OFF = (subject, event) -> { };

    /** Appends {@code event} to the log at {@code subject}, the thread's coordinator subject. */
    void append(String subject, CoordinatorEvent event);
}
