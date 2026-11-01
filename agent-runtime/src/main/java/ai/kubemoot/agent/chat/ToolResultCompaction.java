package ai.kubemoot.agent.chat;

import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.ToolExecutionResultMessage;

import java.util.List;

/**
 * Brings a tool loop's prompt back inside the window by replacing the largest earlier tool
 * results with a short note. The model's own call (name and arguments) stays in the prompt, so
 * the note only has to say that the result was dropped and that the call can be made again with
 * less. Used by an agent that reads results itself; a tooler's full results go to the crew and
 * its model sees stand-ins instead.
 */
final class ToolResultCompaction {

    /** Results at or below this many characters are never replaced: the note is not much shorter. */
    static final int MIN_REPLACEABLE_CHARS = 600;

    private ToolResultCompaction() {}

    /**
     * Replace the largest tool results in {@code messages} until the estimated prompt is at most
     * {@code targetTokens}. Returns how many were replaced (zero when the prompt already fits or
     * no result is large enough to be worth replacing).
     */
    static int compact(List<ChatMessage> messages, List<ToolSpecification> specs, long targetTokens,
                       CharsPerToken ratio) {
        int replaced = 0;
        while (estimate(messages, specs, ratio) > targetTokens) {
            int largest = largestReplaceable(messages);
            if (largest < 0) {
                break;
            }
            var result = (ToolExecutionResultMessage) messages.get(largest);
            messages.set(largest, new ToolExecutionResultMessage(result.id(), result.toolName(),
                    note(result.toolName(), ratio.tokens(sizeOf(result)))));
            replaced++;
        }
        return replaced;
    }

    /** Estimated tokens of the whole prompt, the same way the loop estimates it without an engine report. */
    static long estimate(List<ChatMessage> messages, List<ToolSpecification> specs, CharsPerToken ratio) {
        return PromptSize.fullEstimateTokens(messages, specs, ratio);
    }

    /** True for a note this class wrote. */
    static boolean isNote(String text) {
        return text != null && text.contains(MARKER);
    }

    private static final String MARKER = "was removed from the context window";

    private static int largestReplaceable(List<ChatMessage> messages) {
        int best = -1;
        long bestSize = MIN_REPLACEABLE_CHARS;
        for (int i = 0; i < messages.size(); i++) {
            if (messages.get(i) instanceof ToolExecutionResultMessage result && !isNote(result.text())) {
                long size = sizeOf(result);
                if (size > bestSize) {
                    best = i;
                    bestSize = size;
                }
            }
        }
        return best;
    }

    private static long sizeOf(ToolExecutionResultMessage result) {
        String text = result.text() == null ? "" : result.text();
        return text.length() + TokenDensity.extraChars(text);
    }

    private static String note(String toolName, long tokens) {
        return ToolResultBudget.errorJson("The earlier " + toolName + " result (about " + tokens + " tokens) "
                + MARKER + " because the prompt would not fit. Call " + toolName + " again with a narrower"
                + " request if you still need it, or aggregate the data and read only the answer.");
    }
}
