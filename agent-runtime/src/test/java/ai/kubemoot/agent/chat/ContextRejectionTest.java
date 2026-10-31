package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/** An engine's context-length refusal is recognised and its reported sizes are read. */
class ContextRejectionTest {

    private static final String AT_LEAST = "This model's maximum context length is 32768 tokens. "
            + "However, your prompt contains at least 32769 input tokens, please reduce your prompt.";
    private static final String REQUESTED = "This model's maximum context length is 8192 tokens. "
            + "However, you requested 9000 tokens (8000 in the messages, 1000 in the completion).";

    @Test
    void recognisesVllmAndOpenAiRejections() {
        assertTrue(ContextRejection.matches(AT_LEAST));
        assertTrue(ContextRejection.matches("{\"code\": \"context_length_exceeded\"}"));
        assertFalse(ContextRejection.matches("HTTP 400: bad schema"));
        assertFalse(ContextRejection.matches(null));
    }

    @Test
    void readsTheReportedSizes() {
        var e = new RuntimeException(AT_LEAST);
        assertEquals(32_768, ContextRejection.reportedLimit(e).getAsLong());
        assertEquals(32_769, ContextRejection.reportedPromptTokens(e).getAsLong());
        assertEquals(8_000, ContextRejection.reportedPromptTokens(new RuntimeException(REQUESTED)).getAsLong());
    }

    @Test
    void readsACauseAndIgnoresOtherErrors() {
        var wrapped = new RuntimeException("call failed", new IllegalStateException(AT_LEAST));
        assertTrue(ContextRejection.isCausedBy(wrapped));
        assertEquals(32_769, ContextRejection.reportedPromptTokens(wrapped).getAsLong());
        var other = new RuntimeException("timeout");
        assertFalse(ContextRejection.isCausedBy(other));
        assertTrue(ContextRejection.reportedLimit(other).isEmpty());
        assertTrue(ContextRejection.reportedPromptTokens(new RuntimeException()).isEmpty());
    }
}
