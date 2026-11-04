package ai.kubemoot.agent.chat;

import java.util.Map;

/**
 * Where a raw-output tooler writes each tool call's result as its own workspace object. The
 * object key layout belongs to the implementation, which owns the thread-scoped key schema and
 * the clean-up that deletes a thread's objects by prefix.
 */
public interface CallArtifactStore {

    /**
     * The prefix of every key stored for the thread, {@code <namespace>/<crew>/<thread>/}. A key
     * without it is the key relative to the thread, {@code <agent>/<call number>-<tool>}, which is
     * how agents see and write keys.
     */
    String threadPrefix(String threadId);

    /**
     * The key the next call of {@code tool} by {@code agent} in the thread is stored under:
     * {@code <thread prefix><agent>/<call number>-<tool>}. Each call to this method takes the
     * next call number, so no two calls of one agent in one thread share a key.
     */
    String reserveKey(String threadId, String agent, String tool);

    /**
     * Write {@code body} under {@code key} with {@code headers} as the object's NATS headers, and
     * announce the object to the materializer like any other workspace object. Returns false when
     * the write failed; nothing is thrown.
     */
    boolean put(String threadId, String agent, String tool, String key, byte[] body, Map<String, String> headers);
}
