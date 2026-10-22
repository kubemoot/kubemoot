package ai.kubemoot.agent.gateway;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;

class GatewayClientCallBodyTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void metaIsSentUnderTheMcpKey() throws Exception {
        var body = GatewayClient.callBody(Map.of("q", "k8s"), Map.of("timeZone", "Asia/Tokyo"));
        JsonNode json = MAPPER.readTree(MAPPER.writeValueAsString(body));
        assertEquals("Asia/Tokyo", json.path("_meta").path("timeZone").asText());
        assertEquals("k8s", json.path("arguments").path("q").asText());
    }

    @Test
    void noMetaMeansNoMetaKey() {
        assertFalse(GatewayClient.callBody(Map.of(), Map.of()).containsKey("_meta"));
        assertFalse(GatewayClient.callBody(Map.of(), null).containsKey("_meta"));
    }
}
