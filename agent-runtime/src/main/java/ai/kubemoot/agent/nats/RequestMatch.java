package ai.kubemoot.agent.nats;

/**
 * The thread a request already started, found when a restarted coordinator is handed the
 * request again. {@code live} is the thread in memory (in flight or closed); a thread that
 * closed before the restart is known only by its logged answer, so {@code live} is null.
 */
record RequestMatch(DiscussionOrchestrator.ThreadState live, String threadId, String answer, boolean closed) {

    /** A thread in memory that has not closed: the caller waits for its answer. */
    static RequestMatch inFlight(DiscussionOrchestrator.ThreadState thread) {
        return new RequestMatch(thread, thread.threadId, null, false);
    }

    /** A thread that closed with {@code answer} (null when it closed with none). */
    static RequestMatch closedWith(String threadId, String answer) {
        return new RequestMatch(null, threadId, answer, true);
    }
}
