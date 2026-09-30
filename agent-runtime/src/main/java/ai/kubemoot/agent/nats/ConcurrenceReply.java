package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ToolCallFailure;
import ai.kubemoot.agent.util.ReplySentinels;

import java.util.Locale;

/**
 * The verdict of a concurrence reply. A concurrence check asks one analyst for a
 * second opinion on results already gathered, so the reply is a verdict, not a
 * new answer: it opens with {@link ReplySentinels#CONCUR} (the results answer the
 * question as asked, with at most a short caveat after it) or with
 * {@link ReplySentinels#CONCERN} (what is missing or wrong; {@link ReplySentinels#TOOL_GAP}
 * is a concern too, as on the contribution path).
 *
 * <p>A reply that opens with neither gives no verdict. It is not agreement: a free-form
 * answer in its place is the analyst redoing the work over data it may not have seen
 * whole, and taking it as agreement would pass its claims to the synthesis unchecked.
 * The coordinator treats no verdict, like an empty reply, as a failure and runs the full
 * review.</p>
 */
final class ConcurrenceReply {

    /** The failure content a reply without a verdict publishes. */
    static final String NO_VERDICT = "The concurrence reply gave no verdict: it opened with neither "
            + ReplySentinels.CONCUR + " nor " + ReplySentinels.CONCERN;

    /** The failure content an empty reply publishes. */
    static final String EMPTY_REPLY = "The concurrence reply was empty";

    /** The failure signal's {@code failureType} for a reply without a verdict. */
    static final String NO_VERDICT_FAILURE_TYPE = "no_verdict";

    /** The failure signal's {@code failureType} for an empty reply, the same name the tool loop's EMPTY_REPLY uses. */
    static final String EMPTY_FAILURE_TYPE = ToolCallFailure.FailureType.EMPTY_REPLY.name().toLowerCase(Locale.ROOT);

    /** What a concurring reply carries when nothing follows the sentinel. */
    static final String CONCURS = "Concurs with the gathered results.";

    enum Verdict { CONCUR, CONCERN, NONE, EMPTY }

    /** A classified reply: its verdict and the text after the sentinel (the whole reply for NONE). */
    record Classified(Verdict verdict, String text) {}

    private ConcurrenceReply() {}

    /** Classify a concurrence reply by its opening sentinel. */
    static Classified classify(String reply) {
        String text = reply == null ? "" : reply.strip();
        if (text.isEmpty()) {
            return new Classified(Verdict.EMPTY, "");
        }
        String concern = DiscussionSubscriber.concernIn(text);
        if (concern != null) {
            return new Classified(Verdict.CONCERN, concern);
        }
        String concur = ReplySentinels.after(text, ReplySentinels.CONCUR);
        if (concur != null) {
            return new Classified(Verdict.CONCUR, concur.isEmpty() ? CONCURS : concur);
        }
        return new Classified(Verdict.NONE, text);
    }
}
