package kubemoot.ai.mcpgateway.service;

import org.junit.jupiter.api.Test;
import org.springframework.core.io.buffer.DataBufferLimitException;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ResponseLimitTest {

    @Test
    void aDirectLimitExceptionIsRecognised() {
        assertTrue(ResponseLimit.isExceeded(new DataBufferLimitException("Exceeded limit")));
    }

    @Test
    void aWrappedLimitExceptionIsRecognised() {
        var wrapped = new RuntimeException("outer", new IllegalStateException(new DataBufferLimitException("x")));

        assertTrue(ResponseLimit.isExceeded(wrapped));
    }

    @Test
    void anUnrelatedExceptionIsNot() {
        assertFalse(ResponseLimit.isExceeded(new RuntimeException("boom")));
        assertFalse(ResponseLimit.isExceeded(new RuntimeException("outer", new RuntimeException("inner"))));
    }

    @Test
    void aDeclaredLengthIsOverOnlyAboveTheLimit() {
        assertFalse(ResponseLimit.isOver(99, 100));
        assertFalse(ResponseLimit.isOver(100, 100));
        assertTrue(ResponseLimit.isOver(101, 100));
        assertFalse(ResponseLimit.isOver(-1, 100));
    }

    @Test
    void describeNamesTheSettingForALimitFailureAndKeepsOtherMessages() {
        assertTrue(ResponseLimit.describe(new DataBufferLimitException("x"), 100)
            .contains("mcp.gateway.max-response-size"));
        assertEquals("boom", ResponseLimit.describe(new RuntimeException("boom"), 100));
    }
}
