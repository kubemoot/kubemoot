package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class RegisterServerRequestTest {

    private final ObjectMapper mapper = new ObjectMapper();

    @Test
    void parsesToolOverrides() throws Exception {
        String json = """
            {"name":"k8s","url":"http://x","transport":"sse","toolOverrides":[
              {"name":"helm_list","description":"d","parameters":{"namespace":{"description":"p"}}}]}""";

        RegisterServerRequest request = mapper.readValue(json, RegisterServerRequest.class);

        assertEquals(1, request.toolOverrides().size());
        ToolOverride override = request.toolOverrides().get(0);
        assertEquals("helm_list", override.name());
        assertEquals("p", override.parameters().get("namespace").description());
    }

    @Test
    void parsesArgumentRulesInOrderAndToleratesTheirAbsence() throws Exception {
        String json = """
            {"name":"k8s","url":"http://x","toolOverrides":[{"name":"resources_list","parameters":{
              "fieldSelector":{"description":"d","rules":[
                {"reject":"[<>]","message":"no operators"},{"reject":"=~","message":"no regex"}]},
              "labelSelector":{"description":"l"}}}]}""";

        RegisterServerRequest request = mapper.readValue(json, RegisterServerRequest.class);

        var parameters = request.toolOverrides().get(0).parameters();
        assertEquals(2, parameters.get("fieldSelector").rules().size());
        assertEquals("[<>]", parameters.get("fieldSelector").rules().get(0).reject());
        assertEquals("no regex", parameters.get("fieldSelector").rules().get(1).message());
        assertTrue(parameters.get("labelSelector").rules().isEmpty());
    }

    @Test
    void aNullRuleInTheListIsDropped() throws Exception {
        RegisterServerRequest request = mapper.readValue("""
            {"name":"k8s","url":"http://x","toolOverrides":[{"name":"t","parameters":{
              "p":{"description":"d","rules":[null,{"reject":"x","message":"m"}]}}}]}""",
            RegisterServerRequest.class);
        assertEquals(1, request.toolOverrides().get(0).parameters().get("p").rules().size());
    }

    @Test
    void anOlderPayloadWithoutOverridesHasAnEmptyList() throws Exception {
        RegisterServerRequest request = mapper.readValue(
            "{\"name\":\"k8s\",\"url\":\"http://x\",\"transport\":\"sse\"}", RegisterServerRequest.class);
        assertTrue(request.toolOverrides().isEmpty());
    }

    @Test
    void aBlankNameIsRejected() {
        assertThrows(IllegalArgumentException.class, () -> new RegisterServerRequest(" ", "http://x", "sse"));
    }
}
