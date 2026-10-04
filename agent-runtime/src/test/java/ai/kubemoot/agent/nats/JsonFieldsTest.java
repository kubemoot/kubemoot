package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

/** Tests for JsonFields, the message field reader. Plain JUnit 5. */
class JsonFieldsTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void presentFieldReturnsItsText() throws Exception {
        var msg = MAPPER.readTree("{\"threadId\":\"t-1\",\"count\":3}");
        assertEquals("t-1", JsonFields.text(msg, "threadId"));
        assertEquals("3", JsonFields.text(msg, "count"));
        assertEquals("t-1", JsonFields.text(msg, "threadId", "general"));
    }

    @Test
    void missingFieldReturnsEmptyOrTheFallback() throws Exception {
        var msg = MAPPER.readTree("{\"threadId\":\"t-1\"}");
        assertEquals("", JsonFields.text(msg, "content"));
        assertEquals("general", JsonFields.text(msg, "channel", "general"));
    }

    @Test
    void explicitNullFieldReturnsNullText() throws Exception {
        var msg = MAPPER.readTree("{\"content\":null}");
        assertEquals("null", JsonFields.text(msg, "content"));
    }
}
