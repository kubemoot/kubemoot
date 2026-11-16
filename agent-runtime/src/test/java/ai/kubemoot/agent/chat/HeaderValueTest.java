package ai.kubemoot.agent.chat;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.File;
import org.junit.jupiter.api.Test;

/** The header value encoding, on the fixture artifact-access (Go) runs too. */
class HeaderValueTest {

    private static JsonNode fixture() throws Exception {
        return new ObjectMapper().readTree(new File("../test/fixtures/header-values.json"));
    }

    @Test
    void everyRoundTripCaseEncodesAndDecodesAsTheFixtureSays() throws Exception {
        JsonNode cases = fixture().get("roundTrip");
        assertTrue(cases.size() > 0, "the fixture has cases");
        for (JsonNode c : cases) {
            String name = c.get("name").asText();
            assertEquals(c.get("encoded").asText(), HeaderValue.encode(c.get("value").asText()), name);
            assertEquals(c.get("value").asText(), HeaderValue.decode(c.get("encoded").asText()), name);
        }
    }

    @Test
    void everyDecodeOnlyCaseReadsAsTheFixtureSays() throws Exception {
        JsonNode cases = fixture().get("decodeOnly");
        assertTrue(cases.size() > 0, "the fixture has cases");
        for (JsonNode c : cases) {
            assertEquals(c.get("value").asText(), HeaderValue.decode(c.get("encoded").asText()), c.get("name").asText());
        }
    }

    @Test
    void anEncodedValueIsPrintableAscii() throws Exception {
        for (JsonNode c : fixture().get("roundTrip")) {
            String encoded = HeaderValue.encode(c.get("value").asText());
            assertTrue(encoded.chars().allMatch(ch -> ch >= 0x20 && ch < 0x7F), encoded);
        }
    }

    @Test
    void aMissingValueStaysMissing() {
        assertNull(HeaderValue.decode(null));
    }
}
