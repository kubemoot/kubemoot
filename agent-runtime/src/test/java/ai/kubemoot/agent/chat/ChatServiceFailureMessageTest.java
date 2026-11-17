package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The tail of a repeated-tool-failure message: the last error the tool returned. */
class ChatServiceFailureMessageTest {

    @Test
    void anErrorWrapperIsUnwrappedAndOnOneLine() {
        String result = "{\"error\": \"ASSERT every fieldSelector term is field=value.\\nWHEN the question needs a "
                + "field's value THEN call again with no fieldSelector.\"}";
        assertEquals(". Last error: ASSERT every fieldSelector term is field=value. WHEN the question needs a "
                + "field's value THEN call again with no fieldSelector.", ChatService.lastErrorSuffix(result));
    }

    @Test
    void escapedQuotesInsideTheErrorAreRestored() {
        assertEquals(". Last error: namespaces \"all\" not found",
                ChatService.lastErrorSuffix("{\"error\": \"namespaces \\\"all\\\" not found\"}"));
    }

    @Test
    void aLongErrorIsCutAndSaysSo() {
        String suffix = ChatService.lastErrorSuffix("{\"error\": \"" + "x".repeat(1000) + "\"}");
        assertTrue(suffix.endsWith("..."), suffix);
        assertEquals(". Last error: ".length() + ChatService.FAILURE_ERROR_CHARS + 3, suffix.length());
    }

    @Test
    void textWithoutTheWrapperIsKeptAsIs() {
        assertEquals(". Last error: connection refused", ChatService.lastErrorSuffix("connection refused"));
    }

    @Test
    void noErrorTextAddsNothing() {
        assertEquals("", ChatService.lastErrorSuffix(null));
        assertEquals("", ChatService.lastErrorSuffix("   "));
        assertEquals("", ChatService.lastErrorSuffix("{\"error\": \"\"}"));
    }
}
