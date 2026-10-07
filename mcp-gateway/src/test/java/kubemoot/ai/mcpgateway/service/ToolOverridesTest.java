package kubemoot.ai.mcpgateway.service;

import kubemoot.ai.mcpgateway.model.ToolInfo;
import kubemoot.ai.mcpgateway.model.ToolOverride;
import kubemoot.ai.mcpgateway.model.ToolOverride.ParameterOverride;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;

class ToolOverridesTest {

    private static ToolInfo helmList() {
        Map<String, Object> schema = Map.of("type", "object", "properties", Map.of(
            "namespace", Map.of("type", "string", "description", "upstream ns"),
            "all_namespaces", Map.of("type", "boolean", "description", "upstream all")));
        return new ToolInfo("helm_list", "upstream desc", schema, "id", "k8s");
    }

    private static ToolInfo podsList() {
        return new ToolInfo("pods_list", "pods", Map.of("type", "object"), "id", "k8s");
    }

    @Test
    void noOverridesReturnsTheSameList() {
        List<ToolInfo> tools = List.of(helmList());
        assertSame(tools, ToolOverrides.apply(tools, List.of()));
        assertSame(tools, ToolOverrides.apply(tools, null));
    }

    @Test
    void replacesToolAndParameterDescriptionsOnly() {
        ToolOverride override = new ToolOverride("helm_list", "new desc",
            Map.of("namespace", new ParameterOverride("new ns")));

        List<ToolInfo> result = ToolOverrides.apply(List.of(helmList(), podsList()), List.of(override));

        ToolInfo helm = result.get(0);
        assertEquals("new desc", helm.description());
        Map<?, ?> props = (Map<?, ?>) helm.inputSchema().get("properties");
        Map<?, ?> ns = (Map<?, ?>) props.get("namespace");
        assertEquals("new ns", ns.get("description"));
        assertEquals("string", ns.get("type"));
        assertEquals("upstream all", ((Map<?, ?>) props.get("all_namespaces")).get("description"));
        assertEquals("helm_list", helm.name());
        assertEquals("pods", result.get(1).description());
    }

    @Test
    void doesNotMutateTheUpstreamTool() {
        ToolInfo original = helmList();
        ToolOverrides.apply(List.of(original), List.of(new ToolOverride("helm_list", "x",
            Map.of("namespace", new ParameterOverride("y")))));
        assertEquals("upstream desc", original.description());
        Map<?, ?> ns = (Map<?, ?>) ((Map<?, ?>) original.inputSchema().get("properties")).get("namespace");
        assertEquals("upstream ns", ns.get("description"));
    }

    @Test
    void warningForUnmatchedOverridesDoesNotFail() {
        List<ToolOverride> overrides = List.of(
            new ToolOverride("missing_tool", "x", Map.of()),
            new ToolOverride("helm_list", null, Map.of("missing_param", new ParameterOverride("y"))));
        ToolOverrides.warnUnmatched(List.of(helmList()), overrides, "k8s");
        ToolOverrides.warnUnmatched(List.of(), overrides, "k8s");
        ToolOverrides.warnUnmatched(List.of(helmList()), null, "k8s");
    }

    @Test
    void unknownToolAndUnknownParameterAreSkippedWithoutFailing() {
        List<ToolOverride> overrides = List.of(
            new ToolOverride("missing_tool", "x", Map.of()),
            new ToolOverride("helm_list", null, Map.of("missing_param", new ParameterOverride("y"))));

        List<ToolInfo> result = ToolOverrides.apply(List.of(helmList()), overrides);

        assertEquals("upstream desc", result.get(0).description());
        Map<?, ?> props = (Map<?, ?>) result.get(0).inputSchema().get("properties");
        assertEquals(2, props.size());
    }

    @Test
    void aToolWithoutPropertiesIgnoresParameterOverrides() {
        ToolOverride override = new ToolOverride("pods_list", "d", Map.of("x", new ParameterOverride("y")));
        List<ToolInfo> result = ToolOverrides.apply(List.of(podsList()), List.of(override));
        assertEquals("d", result.get(0).description());
    }
}
