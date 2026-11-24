package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.model.CallerCredentialConfig.Mode;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class CallerCredentialConfigTest {

    private final ObjectMapper mapper = new ObjectMapper();

    @Test
    void aRegistrationWithoutTheFieldIsModeOff() throws Exception {
        RegisterServerRequest request = mapper.readValue("{\"name\":\"k\",\"url\":\"http://x\"}",
            RegisterServerRequest.class);

        assertEquals(Mode.OFF, request.callerCredential().mode());
        assertFalse(request.callerCredential().required());
    }

    @Test
    void aRequiredRegistrationParsesEveryField() throws Exception {
        RegisterServerRequest request = mapper.readValue("""
            {"name":"o","url":"https://api.example.com/expert/mcp","callerCredential":{
              "mode":"Required","leaseUrl":"http://broker.svc:8081/internal/v1/lease","verifier":"oreilly",
              "accountName":"O'Reilly","onForbidden":["https://api.example.com/search/mcp"],"relayContext":true}}""",
            RegisterServerRequest.class);

        CallerCredentialConfig config = request.callerCredential();
        assertTrue(config.required());
        assertEquals("oreilly", config.verifier());
        assertEquals("O'Reilly", config.account());
        assertEquals(List.of("https://api.example.com/search/mcp"), config.onForbidden());
        assertTrue(config.relayContext());
    }

    @Test
    void theAccountNameDefaultsToTheVerifier() {
        assertEquals("oreilly", new CallerCredentialConfig(Mode.REQUIRED, null, "oreilly", null, null, false).account());
    }

    @Test
    void requiredNeedsAVerifierName() {
        assertThrows(IllegalArgumentException.class,
            () -> new CallerCredentialConfig(Mode.REQUIRED, null, null, null, null, false));
        assertThrows(IllegalArgumentException.class,
            () -> new CallerCredentialConfig(Mode.REQUIRED, null, "Not A Label", null, null, false));
    }

    @Test
    void endpointsMustBeHttpOrHttps() {
        assertThrows(IllegalArgumentException.class,
            () -> new CallerCredentialConfig(Mode.REQUIRED, "file:///etc/passwd", "v", null, null, false));
        assertThrows(IllegalArgumentException.class,
            () -> new CallerCredentialConfig(Mode.REQUIRED, null, "v", null, List.of("ftp://x/mcp"), false));
        assertThrows(IllegalArgumentException.class,
            () -> new CallerCredentialConfig(Mode.REQUIRED, null, "v", null, List.of("not a url"), false));
    }

    @Test
    void aFallbackEndpointMustBeHttpsUnlessItIsLoopback() {
        IllegalArgumentException refused = assertThrows(IllegalArgumentException.class,
            () -> new CallerCredentialConfig(Mode.REQUIRED, null, "v", null, List.of("http://api.example.com/mcp"), false));
        assertTrue(refused.getMessage().contains("https"), refused.getMessage());
        assertThrows(IllegalArgumentException.class,
            () -> new CallerCredentialConfig(Mode.REQUIRED, null, "v", null, List.of("http://10.0.0.5/mcp"), false));
        assertThrows(IllegalArgumentException.class,
            () -> new CallerCredentialConfig(Mode.REQUIRED, null, "v", null, List.of("http://127.0.0.1.evil.com/mcp"), false));
        assertEquals(1, new CallerCredentialConfig(Mode.REQUIRED, null, "v", null,
            List.of("https://api.example.com/mcp"), false).onForbidden().size());
    }

    @Test
    void plainHttpIsAllowedToLoopbackHosts() {
        List<String> loopback = List.of("http://localhost:8080/mcp", "http://127.0.0.1:9/mcp",
            "http://127.9.8.7/mcp", "http://[::1]:8080/mcp");
        assertEquals(4, new CallerCredentialConfig(Mode.REQUIRED, null, "v", null, loopback, false)
            .onForbidden().size());
    }

    @Test
    void offIgnoresTheOtherFields() {
        CallerCredentialConfig config = new CallerCredentialConfig(Mode.OFF, "garbage", null, null, null, false);
        assertFalse(config.required());
    }

    @Test
    void anUnknownModeIsRefusedAtParse() {
        assertThrows(Exception.class, () -> mapper.readValue(
            "{\"name\":\"k\",\"url\":\"http://x\",\"callerCredential\":{\"mode\":\"Sometimes\"}}",
            RegisterServerRequest.class));
    }

    @Test
    void identityComesFromMetaOnlyAndMustBeWellFormed() {
        assertEquals(new CallerIdentity("conv-1", "p-1"),
            CallerIdentity.from(Map.of("conversationId", "conv-1", "principalId", "p-1")));
        assertNull(CallerIdentity.from(null));
        assertNull(CallerIdentity.from("text"));
        assertNull(CallerIdentity.from(Map.of("conversationId", "conv-1")));
        assertNull(CallerIdentity.from(Map.of("conversationId", "conv 1", "principalId", "p")));
        assertNull(CallerIdentity.from(Map.of("conversationId", "c", "principalId", "x".repeat(257))));
        assertNull(CallerIdentity.from(Map.of("conversationId", 7, "principalId", "p")));
    }

    @Test
    void aTokenPrintsRedactedAndRefusesAnythingThatCouldSplitAHeader() {
        CallerToken token = new CallerToken("secret-value", Instant.now().plusSeconds(60));

        assertFalse(token.toString().contains("secret-value"));
        assertThrows(IllegalArgumentException.class, () -> new CallerToken("a b", null));
        assertThrows(IllegalArgumentException.class, () -> new CallerToken("a\r\nX: 1", null));
        assertThrows(IllegalArgumentException.class, () -> new CallerToken(" ", null));
        assertTrue(new CallerToken("t", Instant.now().minusSeconds(1)).expired(Instant.now()));
        assertFalse(new CallerToken("t", null).expired(Instant.now()));
    }
}
