package ai.kubemoot.agent.chat;

import dev.langchain4j.agent.tool.ToolExecutionRequest;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.data.message.AiMessage;
import dev.langchain4j.data.message.ChatMessage;
import dev.langchain4j.data.message.SystemMessage;
import dev.langchain4j.data.message.TextContent;
import dev.langchain4j.data.message.ToolExecutionResultMessage;
import dev.langchain4j.data.message.UserMessage;

import java.util.List;

/**
 * Measures the prompt the engine receives for a chat request: every message's
 * text, the tool calls the model made and their results, and the tool
 * specifications sent alongside. A tool loop's prompt grows with each tool
 * result, so the size is estimated before every turn, not only when the provider
 * is chosen.
 */
final class PromptSize {

    private PromptSize() {}

    /** Characters the engine receives for {@code messages} plus {@code specs}. */
    static int chars(List<ChatMessage> messages, List<ToolSpecification> specs) {
        long total = 0;
        for (ChatMessage m : messages) {
            total += chars(m);
        }
        if (specs != null) {
            for (ToolSpecification s : specs) {
                total += length(s.name()) + length(s.description())
                        + (s.parameters() == null ? 0 : s.parameters().toString().length());
            }
        }
        return (int) Math.min(total, Integer.MAX_VALUE);
    }

    /** Characters one message contributes to the prompt. */
    static int chars(ChatMessage m) {
        if (m instanceof SystemMessage sm) return length(sm.text());
        if (m instanceof UserMessage um) return userChars(um);
        if (m instanceof ToolExecutionResultMessage tr) return length(tr.toolName()) + length(tr.text());
        if (m instanceof AiMessage am) {
            int n = length(am.text());
            if (am.hasToolExecutionRequests()) {
                for (ToolExecutionRequest r : am.toolExecutionRequests()) {
                    n += length(r.name()) + length(r.arguments());
                }
            }
            return n;
        }
        return 0;
    }

    /**
     * Estimated tokens in the next prompt of a tool loop. When the engine reported
     * the previous turn's prompt and reply sizes, those are exact (the prompt size
     * counts a reused prefix too), so only the messages added since are estimated.
     * Otherwise the whole prompt is estimated with {@code ratio}.
     */
    static long nextPromptTokens(List<ChatMessage> messages, List<ToolSpecification> specs,
                                 long lastPromptTokens, long lastReplyTokens, int messagesAtLastCall,
                                 CharsPerToken ratio) {
        if (lastPromptTokens <= 0 || messagesAtLastCall > messages.size()) {
            return ratio.tokens(chars(messages, specs));
        }
        long added = 0;
        for (ChatMessage m : messages.subList(messagesAtLastCall, messages.size())) {
            added += chars(m);
        }
        return lastPromptTokens + Math.max(lastReplyTokens, 0) + ratio.tokens(added);
    }

    private static int userChars(UserMessage um) {
        int n = 0;
        for (var content : um.contents()) {
            if (content instanceof TextContent tc) n += length(tc.text());
        }
        return n;
    }

    private static int length(String s) {
        return s == null ? 0 : s.length();
    }
}
