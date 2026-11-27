package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.io.File;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;

/** The thread prefixes the gateway forwards are the ones workspace-access and the code sandbox accept. */
class WorkspacePrefixVectorsTest {

    @Test
    void thePrefixesOfTheSharedVectorsAreForwardedOrDropped() throws Exception {
        JsonNode vectors = new ObjectMapper().readTree(new File("../test/fixtures/workspace-prefix-vectors.json"));
        for (JsonNode p : vectors.get("prefixes")) {
            String value = p.get("value").asText();
            boolean forwarded = RequesterMeta.sanitize(Map.of("workspacePrefix", value)).containsKey("workspacePrefix");
            assertEquals(p.get("valid").asBoolean(), forwarded, "prefix [" + value + "]");
        }
    }
}
