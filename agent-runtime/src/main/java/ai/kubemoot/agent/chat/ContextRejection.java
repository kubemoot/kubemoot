package ai.kubemoot.agent.chat;

import java.util.OptionalLong;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Recognises an engine's refusal of a prompt larger than its context window (vLLM and other
 * OpenAI-compatible servers answer HTTP 400 "maximum context length ...") and reads the sizes
 * the refusal reports. The failure classifier and the tool loop share this one definition.
 */
public final class ContextRejection {

    private static final Pattern LIMIT = Pattern.compile("maximum context length is (\\d+)");
    private static final Pattern PROMPT = Pattern.compile(
            "prompt contains (?:at least )?(\\d+) input tokens|\\((\\d+) in the messages");

    private ContextRejection() {}

    /** True when {@code message} is an engine's context-length rejection. */
    public static boolean matches(String message) {
        return message != null
                && (message.contains("maximum context length") || message.contains("context_length_exceeded"));
    }

    /** True when {@code error} or any of its causes is a context-length rejection. */
    static boolean isCausedBy(Throwable error) {
        for (Throwable t = error; t != null; t = next(t)) {
            if (matches(t.getMessage())) {
                return true;
            }
        }
        return false;
    }

    /** The window the server reports, in tokens. */
    static OptionalLong reportedLimit(Throwable error) {
        return find(LIMIT, error);
    }

    /** The prompt size the server reports, in tokens (a lower bound when it says "at least"). */
    static OptionalLong reportedPromptTokens(Throwable error) {
        return find(PROMPT, error);
    }

    private static Throwable next(Throwable t) {
        return t.getCause() == t ? null : t.getCause();
    }

    private static OptionalLong find(Pattern pattern, Throwable error) {
        for (Throwable t = error; t != null; t = next(t)) {
            String message = t.getMessage();
            Matcher m = message == null ? null : pattern.matcher(message);
            if (m != null && m.find()) {
                return firstNumber(m);
            }
        }
        return OptionalLong.empty();
    }

    private static OptionalLong firstNumber(Matcher m) {
        for (int g = 1; g <= m.groupCount(); g++) {
            if (m.group(g) != null) {
                return OptionalLong.of(Long.parseLong(m.group(g)));
            }
        }
        return OptionalLong.empty();
    }
}
