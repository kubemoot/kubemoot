package ai.kubemoot.agent.requester;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class PrincipalTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String ID = "4f9c0b1e7a2d83c65b1f0e9d8c7b6a5948372615c4d3e2f1a0b9c8d7e6f50413";

    private static JsonNode json(String text) throws IOException {
        return MAPPER.readTree(text);
    }

    @Test
    void aSubscriberPrincipalIsRead() throws IOException {
        var principal = Principal.fromJson(json(
                "{\"kind\":\"subscriber\",\"issuer\":\"oreilly\",\"id\":\"" + ID + "\"}")).orElseThrow();
        assertEquals("subscriber", principal.kind());
        assertEquals("oreilly", principal.issuer());
        assertEquals(ID, principal.id());
        assertNull(principal.displayName());
    }

    @Test
    void anAnonymousPrincipalHasNoIdOrIssuer() throws IOException {
        var principal = Principal.fromJson(json("{\"kind\":\"anonymous\"}")).orElseThrow();
        assertEquals("anonymous", principal.kind());
        assertNull(principal.id());
        assertEquals(Map.of("kind", "anonymous"), principal.toBusMetadata());
    }

    @Test
    void aKindTheRuntimeDoesNotKnowIsKeptAsData() throws IOException {
        assertTrue(Principal.fromJson(json("{\"kind\":\"future-kind\",\"id\":\"abc\"}")).isPresent());
    }

    @Test
    void somethingThatIsNotAWellFormedPrincipalReadsAsNone() throws IOException {
        assertTrue(Principal.fromJson(null).isEmpty());
        assertTrue(Principal.fromJson(json("\"subscriber\"")).isEmpty());
        assertTrue(Principal.fromJson(json("[]")).isEmpty());
        assertTrue(Principal.fromJson(json("{}")).isEmpty());
        assertTrue(Principal.fromJson(json("{\"kind\":7}")).isEmpty());
        assertTrue(Principal.fromJson(json("{\"kind\":\"subscriber\",\"id\":42}")).isEmpty());
        assertTrue(Principal.fromJson(json("{\"kind\":\"subscriber\",\"id\":\"has a space\"}")).isEmpty());
        assertTrue(Principal.fromJson(json("{\"kind\":\"subscriber\",\"id\":\"line\\nbreak\"}")).isEmpty());
        assertTrue(Principal.fromJson(json("{\"kind\":\"subscriber\",\"id\":\"\"}")).isEmpty());
        String huge = "a".repeat(Principal.MAX_FIELD_CHARS + 1);
        assertTrue(Principal.fromJson(json("{\"kind\":\"subscriber\",\"id\":\"" + huge + "\"}")).isEmpty());
    }

    @Test
    void aDisplayNameThatIsNotPlainTextIsDropped() throws IOException {
        var principal = Principal.fromJson(json(
                "{\"kind\":\"kubernetes\",\"issuer\":\"kubernetes\",\"id\":\"x\",\"displayName\":\"two words\"}"))
                .orElseThrow();
        assertNull(principal.displayName());
    }

    @Test
    void aTokenFieldOnTheWireIsNeverKept() throws IOException {
        var principal = Principal.fromJson(json(
                "{\"kind\":\"subscriber\",\"id\":\"abc\",\"token\":\"CANARY-TOKEN-1\","
                        + "\"credential\":\"CANARY-TOKEN-1\"}")).orElseThrow();
        assertFalse(principal.toBusMetadata().toString().contains("CANARY-TOKEN-1"));
        assertFalse(principal.toString().contains("CANARY-TOKEN-1"));
    }

    @Test
    void theStringFormLeavesTheIdOut() {
        assertFalse(new Principal("subscriber", "oreilly", ID, null).toString().contains(ID));
    }

    @Test
    void theBusFormRoundTrips() throws IOException {
        var principal = new Principal("subscriber", "oreilly", ID, null);
        var again = Principal.fromJson(MAPPER.valueToTree(principal.toBusMetadata())).orElseThrow();
        assertEquals(principal, again);
    }
}
