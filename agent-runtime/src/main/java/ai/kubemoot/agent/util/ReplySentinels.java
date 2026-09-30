package ai.kubemoot.agent.util;

/**
 * The sentinels an agent reply may open with, and the one way to read them. Every
 * reader of a reply (the tool loop, the contribution classifier, the concurrence
 * verdict) uses these, so the protocol has one owner. Case-sensitive: a sentinel
 * counts only as written, at the start of the stripped reply.
 */
public final class ReplySentinels {

    /** A reply naming a tool the agent lacks: a concern routed to gap detection. */
    public static final String TOOL_GAP = "TOOL_GAP:";
    /** A reply naming what is missing or wrong: a concern. */
    public static final String CONCERN = "CONCERN:";
    /** A concurrence reply that agrees, followed by at most a short caveat. */
    public static final String CONCUR = "CONCUR:";

    private ReplySentinels() {}

    /**
     * The text after {@code sentinel}, trimmed, when the stripped {@code reply} opens
     * with it; null when it does not (including a null reply).
     */
    public static String after(String reply, String sentinel) {
        String text = reply == null ? "" : reply.strip();
        return text.startsWith(sentinel) ? text.substring(sentinel.length()).trim() : null;
    }
}
