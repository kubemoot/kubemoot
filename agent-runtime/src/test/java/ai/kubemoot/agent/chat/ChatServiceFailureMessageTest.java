package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The tail of a repeated-tool-failure message: the last error the tool returned. */
class ChatServiceFailureMessageTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static String suffix(String result) {
        return ChatService.lastErrorSuffix(MAPPER, result);
    }

    @Test
    void anErrorWrapperIsUnwrappedAndOnOneLine() {
        String result = "{\"error\": \"ASSERT every fieldSelector term is field=value.\\nWHEN the question needs a "
                + "field's value THEN call again with no fieldSelector.\"}";
        assertEquals(". Last error: ASSERT every fieldSelector term is field=value. WHEN the question needs a "
                + "field's value THEN call again with no fieldSelector.", suffix(result));
    }

    @Test
    void everyJsonEscapeIsDecoded() {
        assertEquals(". Last error: namespaces \"all\" not found", suffix("{\"error\": \"namespaces \\\"all\\\" not found\"}"));
        assertEquals(". Last error: path C:\\tmp a b", suffix("{\"error\": \"path C:\\\\tmp\\ta\\tb\"}"));
        assertEquals(". Last error: caf\u00e9 \u2192 ok", suffix("{\"error\": \"caf\\u00e9 \\u2192 ok\"}"));
        assertEquals(". Last error: a/b", suffix("{\"error\": \"a\\/b\"}"));
    }

    @Test
    void aLongErrorIsCutByTheSharedHelper() {
        String text = suffix("{\"error\": \"" + "x".repeat(1000) + "\"}");
        assertEquals(". Last error: " + "x".repeat(ChatService.FAILURE_ERROR_CHARS) + TextCut.MARKER, text);
    }

    @Test
    void aCutNeverSplitsASurrogatePair() {
        String emoji = "\uD83D\uDE00";
        String text = suffix("{\"error\": \"" + "x".repeat(ChatService.FAILURE_ERROR_CHARS - 1) + emoji + "yy\"}");
        assertEquals(". Last error: " + "x".repeat(ChatService.FAILURE_ERROR_CHARS - 1) + TextCut.MARKER, text);
    }

    @Test
    void textThatIsNotTheWrapperIsKeptAsIs() {
        assertEquals(". Last error: connection refused", suffix("connection refused"));
        assertEquals(". Last error: {\"message\": \"boom\"}", suffix("{\"message\": \"boom\"}"));
        assertEquals(". Last error: {\"error\": 5}", suffix("{\"error\": 5}"));
        assertTrue(suffix("{\"error\": \"unterminated").startsWith(". Last error: {"));
    }

    @Test
    void noErrorTextAddsNothing() {
        assertEquals("", suffix(null));
        assertEquals("", suffix("   "));
        assertEquals("", suffix("{\"error\": \"\"}"));
    }
}
